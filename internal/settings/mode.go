package settings

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/jsonx"
	"github.com/wonsong82/claude-hub/internal/migrate"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// ModePlan is a planned settings mode change.
type ModePlan struct {
	From, To  string
	Journal   *migrate.Journal
	NewOwn    []string
	Conflict  string   // non-empty: the change cannot proceed under the chosen policy
	Confirm   []string // reasons it needs confirmation
	Notes     []string
	staged    map[string][]byte // backup-relative path -> content to stage before running
	afterSync bool
}

// PlanMode plans a change of settings mode (design section 6, "settings mode changes").
// onConflict is "abort", "keep-base" or "keep-profile".
func PlanMode(p xdg.Paths, c *config.Config, name, to, onConflict string) (*ModePlan, error) {
	if c.IsBase(name) {
		return nil, fmt.Errorf("profile %q is the base profile; its settings.json is the base", name)
	}
	switch to {
	case config.ModeShared, config.ModeOwn, config.ModeLayered:
	default:
		return nil, fmt.Errorf("mode must be shared, own or layered")
	}
	from := c.SettingsMode(name)
	f := FilesFor(p, c, name)
	mp := &ModePlan{From: from, To: to, staged: map[string][]byte{}}
	if from == to {
		return mp, nil
	}
	pr := c.Profiles[name]
	bk := migrate.NewBackupDir(p, name, "settings")
	j := &migrate.Journal{Operation: "settings-mode", Profile: name, Item: "settings", Backup: bk}
	op := &migrate.ConfigOp{Profile: name, Settings: true, NewMode: to, OldMode: from}
	if pr.Settings != nil {
		op.OldHadSet = true
		op.OldOwn = pr.Settings.Own
	}
	curOwn := c.OwnPaths(name)
	if curOwn == nil {
		curOwn = append([]string(nil), config.DefaultOwn...)
	}
	op.NewOwn = curOwn
	isLink := fsutil.IsSymlink(f.Profile)
	exists := fsutil.Exists(f.Profile)
	moveOut := func(src, rel string) {
		if fsutil.Exists(src) {
			j.Steps = append(j.Steps, migrate.Step{Kind: migrate.KMove, Src: src, Dst: filepath.Join(bk, rel)})
		}
	}
	stage := func(rel string, data []byte, dst string) {
		mp.staged[rel] = data
		j.Steps = append(j.Steps, migrate.Step{Kind: migrate.KCopy, Src: filepath.Join(bk, rel), Dst: dst})
	}
	B, bExists, err := ReadObject(f.Base)
	if err != nil {
		return nil, err
	}

	switch {
	case to == config.ModeShared:
		if !bExists {
			return nil, fmt.Errorf("the base has no settings.json yet; create %s first", f.Base)
		}
		if from == config.ModeLayered {
			O, _, _ := ReadObject(f.Overlay)
			d, _ := LoadDrift(f.Drift)
			if (O.Len() > 0 || len(d) > 0) && onConflict != "keep-base" {
				mp.Conflict = fmt.Sprintf("profile %q has %d owned value(s) and %d drifted path(s); use --on-conflict keep-base to drop them into the backup", name, O.Len(), len(d))
			}
			moveOut(f.Overlay, "overlay/settings.overlay.json")
			moveOut(f.Drift, "state/settings.drift.json")
			moveOut(f.Profile, "profile/settings.json")
		} else if isLink {
			if share := fsutil.LinkTarget(f.Profile); share == f.Base {
				return mp, nil
			}
			return nil, fmt.Errorf("%s is a link to %s, not to the base; fix it by hand first", f.Profile, fsutil.LinkTarget(f.Profile))
		} else if exists {
			pb, _ := os.ReadFile(f.Profile)
			bb, _ := os.ReadFile(f.Base)
			if string(pb) != string(bb) {
				switch onConflict {
				case "keep-base":
				case "keep-profile":
					mp.Confirm = append(mp.Confirm, "keep-profile replaces the base settings.json for every profile that inherits it")
					moveOut(f.Base, "base/settings.json")
					j.Steps = append(j.Steps, migrate.Step{Kind: migrate.KMove, Src: f.Profile, Dst: filepath.Join(bk, "profile/settings.json")})
					j.Steps = append(j.Steps, migrate.Step{Kind: migrate.KCopy, Src: filepath.Join(bk, "profile/settings.json"), Dst: f.Base})
					goto link
				default:
					mp.Conflict = "the profile's settings.json differs from the base; use --on-conflict keep-base or keep-profile"
				}
			}
			moveOut(f.Profile, "profile/settings.json")
		}
	link:
		j.Steps = append(j.Steps, migrate.Step{Kind: migrate.KLink, Src: f.Base, Dst: f.Profile})
		op.NewOwn = op.OldOwn
	case from == config.ModeShared:
		// shared -> own or layered: the link is replaced by a copy of the base file; the
		// overlay starts empty.
		if isLink {
			j.Steps = append(j.Steps, migrate.Step{Kind: migrate.KUnlink, Src: fsutil.LinkTarget(f.Profile), Dst: f.Profile})
		} else {
			moveOut(f.Profile, "profile/settings.json")
		}
		moveOut(f.Overlay, "overlay/settings.overlay.json")
		if bExists {
			bb, _ := os.ReadFile(f.Base)
			stage("staged/settings.json", bb, f.Profile)
		}
		mp.afterSync = to == config.ModeLayered
	case from == config.ModeOwn && to == config.ModeLayered:
		C, _, err := ReadObject(f.Profile)
		if err != nil {
			return nil, err
		}
		own := append([]string(nil), curOwn...)
		O := jsonx.NewObject()
		for _, ch := range jsonx.Diff(B, C) {
			k := ch.Path.String()
			if !IsOwned(k, own) {
				own = append(own, k)
			}
		}
		for _, s := range own {
			pth, err := jsonx.ParsePath(s)
			if err != nil {
				continue
			}
			if v, ok := jsonx.Get(C, pth); ok {
				jsonx.SetPath(O, pth, jsonx.Clone(v))
			}
		}
		G := Generate(B, O, own)
		if !jsonx.Equal(G, C) {
			return nil, fmt.Errorf("internal error: the layered file would differ from the current one")
		}
		mp.NewOwn = own
		op.NewOwn = own
		moveOut(f.Overlay, "overlay/settings.overlay.json")
		moveOut(f.Profile, "profile/settings.json")
		stage("staged/settings.overlay.json", jsonx.Marshal(O), f.Overlay)
		stage("staged/settings.json", jsonx.Marshal(G), f.Profile)
		// Old snapshot and drift belong to an earlier layered period.
		moveOut(f.Last, "state/last.json")
		moveOut(f.Drift, "state/settings.drift.json")
		moveOut(f.Hashes, "state/hashes.json")
		mp.Notes = append(mp.Notes, fmt.Sprintf("owned paths: %v (prune with \"claude-hub settings own %s remove <path>\")", own, name))
		mp.afterSync = true
	case from == config.ModeLayered && to == config.ModeOwn:
		moveOut(f.Overlay, "overlay/settings.overlay.json")
		moveOut(f.Last, "state/last.json")
		moveOut(f.Drift, "state/settings.drift.json")
		moveOut(f.Hashes, "state/hashes.json")
	}
	j.Steps = append(j.Steps, migrate.Step{Kind: migrate.KConfig, Config: op})
	mp.Journal = j
	return mp, nil
}

// Run stages files, runs the journal and, for layered targets, syncs.
func (mp *ModePlan) Run(p xdg.Paths, name string) error {
	if mp.Journal == nil {
		return nil
	}
	for rel, data := range mp.staged {
		if err := fsutil.WriteFileAtomic(filepath.Join(mp.Journal.Backup, rel), data, 0o600); err != nil {
			return err
		}
	}
	if err := migrate.Run(p, mp.Journal); err != nil {
		return err
	}
	if mp.afterSync {
		c, _, err := config.Load(p)
		if err != nil {
			return err
		}
		_, err = Sync(p, c, name)
		return err
	}
	return nil
}
