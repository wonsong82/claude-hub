// Package migrate moves data when sharing changes (design section 6): plans, a journal that
// survives interruption, backups with a MANIFEST.json, and conflict handling. Nothing is
// deleted: whatever leaves its place goes to the backup.
package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/version"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Step kinds.
const (
	KMove   = "move"   // move Src to Dst (Dst must not exist)
	KCopy   = "copy"   // copy Src to Dst, verified (Dst must not exist)
	KLink   = "link"   // symlink at Dst pointing to Src
	KMkdir  = "mkdir"  // create the folder Dst (it did not exist at plan time)
	KUnlink = "unlink" // remove the symlink at Dst (its target is kept in Src)
	KConfig = "config" // change the config
)

// Step is one journaled step.
type Step struct {
	Kind   string    `json:"kind"`
	Src    string    `json:"src,omitempty"`
	Dst    string    `json:"dst,omitempty"`
	Config *ConfigOp `json:"config,omitempty"`
	// Created lists the parent folders a copy or move created, deepest first, so that a
	// rollback can remove them again.
	Created []string `json:"created,omitempty"`
	// Hash is the tree hash of a move's source, recorded before the move starts, so that an
	// interrupted cross-device move can tell the complete copy from the partial one.
	Hash string `json:"hash,omitempty"`
	Done bool   `json:"done"`
}

// partial is where a copy is written before it is renamed into place, so that an existing
// destination is never mistaken for a half-written copy.
func partial(dst string) string { return dst + ".claude-hub-partial" }

// missingParents returns the ancestors of p that do not exist, deepest first.
func missingParents(p string) []string {
	var out []string
	for d := filepath.Dir(p); d != "/" && d != "." && !fsutil.Exists(d); d = filepath.Dir(d) {
		out = append(out, d)
	}
	return out
}

func removeEmpty(dirs []string) {
	for _, d := range dirs {
		_ = os.Remove(d) // only when empty
	}
}

// ConfigOp is a config change, with what is needed to undo it.
type ConfigOp struct {
	Profile string `json:"profile"`
	// Share: set share.<Item> (or add/remove an extra name) to New; Old is the previous value.
	Item string `json:"item,omitempty"`
	New  string `json:"new,omitempty"`
	Old  string `json:"old,omitempty"`
	// Settings: set settings mode and own list.
	Settings  bool     `json:"settings,omitempty"`
	NewMode   string   `json:"new_mode,omitempty"`
	OldMode   string   `json:"old_mode,omitempty"`
	NewOwn    []string `json:"new_own,omitempty"`
	OldOwn    []string `json:"old_own,omitempty"`
	OldHadSet bool     `json:"old_had_settings,omitempty"`
}

// Journal is the migration journal in the state dir.
type Journal struct {
	Operation string `json:"operation"`
	Profile   string `json:"profile"`
	Item      string `json:"item"`
	Backup    string `json:"backup,omitempty"`
	Created   string `json:"created"`
	Version   string `json:"hub_version"`
	Steps     []Step `json:"steps"`
}

// Manifest is MANIFEST.json in a backup folder.
type Manifest struct {
	Operation string   `json:"operation"`
	Profile   string   `json:"profile"`
	Item      string   `json:"item"`
	Created   string   `json:"created"`
	Version   string   `json:"hub_version"`
	Sources   []Source `json:"sources"`
}

// Source is one path a backup holds, with its tree hash.
type Source struct {
	Path   string `json:"path"`
	Backup string `json:"backup"`
	Hash   string `json:"hash"`
}

// AfterStep is a test hook called after each step is marked done; an error stops the run
// as if the process had been killed.
var AfterStep func(i int) error

// ErrInterrupted is returned when AfterStep stops a run.
var ErrInterrupted = errors.New("migration interrupted")

// Pending loads an unfinished journal, or nil.
func Pending(p xdg.Paths) (*Journal, error) {
	b, err := os.ReadFile(p.JournalFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("%s is unreadable: %v", p.JournalFile(), err)
	}
	return &j, nil
}

func save(p xdg.Paths, j *Journal) error {
	b, _ := json.MarshalIndent(j, "", "  ")
	return fsutil.WriteFileAtomic(p.JournalFile(), b, 0o600)
}

// NewBackupDir picks a fresh backup folder name (not created).
func NewBackupDir(p xdg.Paths, profile, item string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	base := filepath.Join(p.BackupsDir(), fmt.Sprintf("%s-%s-%s", ts, profile, item))
	d := base
	for i := 2; fsutil.Exists(d); i++ {
		d = fmt.Sprintf("%s-%d", base, i)
	}
	return d
}

// WriteManifest writes MANIFEST.json, hashing the sources that are about to move.
func WriteManifest(dir string, j *Journal) error {
	m := Manifest{Operation: j.Operation, Profile: j.Profile, Item: j.Item, Created: j.Created, Version: version.Version}
	for _, s := range j.Steps {
		if s.Kind == KMove && isUnder(s.Dst, dir) {
			h, err := fsutil.HashTree(s.Src)
			if err != nil {
				return err
			}
			m.Sources = append(m.Sources, Source{Path: s.Src, Backup: s.Dst, Hash: h})
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return fsutil.WriteFileAtomic(filepath.Join(dir, "MANIFEST.json"), b, 0o600)
}

func isUnder(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && (len(rel) < 3 || rel[:3] != "../")
}

// Run journals and executes j. The journal is written before the first step and each step
// is marked when done; it is removed when the run completes.
func Run(p xdg.Paths, j *Journal) error {
	if old, err := Pending(p); err != nil {
		return err
	} else if old != nil {
		return fmt.Errorf("an unfinished migration (%s %s %s) is pending; run \"claude-hub migrate resume\" or \"claude-hub migrate rollback\"", old.Operation, old.Profile, old.Item)
	}
	if j.Created == "" {
		j.Created = time.Now().UTC().Format(time.RFC3339)
	}
	j.Version = version.Version
	if j.Backup != "" {
		if err := WriteManifest(j.Backup, j); err != nil {
			return err
		}
	}
	if err := save(p, j); err != nil {
		return err
	}
	return Resume(p, j)
}

// Resume executes the steps not yet done.
func Resume(p xdg.Paths, j *Journal) error {
	for i := range j.Steps {
		s := &j.Steps[i]
		if s.Done {
			continue
		}
		if s.Kind == KMove && s.Hash == "" && fsutil.Exists(s.Src) && !fsutil.Exists(s.Dst) {
			h, err := fsutil.HashTree(s.Src)
			if err != nil {
				return err
			}
			s.Hash = h
			if err := save(p, j); err != nil {
				return err
			}
		}
		if err := do(p, s); err != nil {
			return fmt.Errorf("step %d (%s %s): %v; the journal is kept: run \"claude-hub migrate resume\" or \"claude-hub migrate rollback\"", i+1, s.Kind, s.Dst, err)
		}
		s.Done = true
		if err := save(p, j); err != nil {
			return err
		}
		if AfterStep != nil {
			if err := AfterStep(i); err != nil {
				return ErrInterrupted
			}
		}
	}
	return os.Remove(p.JournalFile())
}

func do(p xdg.Paths, s *Step) error {
	switch s.Kind {
	case KMove:
		srcOK, dstOK := fsutil.Exists(s.Src), fsutil.Exists(s.Dst)
		switch {
		case srcOK && dstOK:
			// An interrupted cross-device move: finish it only when the destination is the
			// complete copy; when the source is intact, redo it from the start.
			switch whole := wholeSide(s); whole {
			case "dst":
				return fsutil.RemoveTree(s.Src)
			case "src":
				if err := fsutil.RemoveTree(s.Dst); err != nil {
					return err
				}
				return fsutil.MoveTree(s.Src, s.Dst)
			}
			return fmt.Errorf("both %s and %s exist and neither matches the recorded hash; settle it by hand", s.Src, s.Dst)
		case !srcOK && dstOK:
			return nil
		case !srcOK:
			return fmt.Errorf("%s is missing", s.Src)
		}
		s.Created = append(s.Created, missingParents(s.Dst)...)
		return fsutil.MoveTree(s.Src, s.Dst)
	case KCopy:
		if fsutil.Exists(s.Dst) {
			a, _ := fsutil.HashTree(s.Src)
			b, _ := fsutil.HashTree(s.Dst)
			if a == b {
				return nil
			}
			return fmt.Errorf("%s already exists", s.Dst)
		}
		s.Created = append(s.Created, missingParents(s.Dst)...)
		tmp := partial(s.Dst)
		if err := fsutil.RemoveTree(tmp); err != nil {
			return err
		}
		if err := fsutil.CopyTree(s.Src, tmp); err != nil {
			return err
		}
		return os.Rename(tmp, s.Dst)
	case KLink:
		if fsutil.IsSymlink(s.Dst) && fsutil.LinkTarget(s.Dst) == s.Src {
			return nil
		}
		if fsutil.Exists(s.Dst) {
			return fmt.Errorf("%s exists", s.Dst)
		}
		if err := os.MkdirAll(filepath.Dir(s.Dst), 0o700); err != nil {
			return err
		}
		return os.Symlink(s.Src, s.Dst)
	case KMkdir:
		return os.MkdirAll(s.Dst, 0o700)
	case KUnlink:
		if !fsutil.Exists(s.Dst) {
			return nil
		}
		if !fsutil.IsSymlink(s.Dst) {
			return fmt.Errorf("%s is not a link", s.Dst)
		}
		return os.Remove(s.Dst)
	case KConfig:
		return applyConfig(p, s.Config, false)
	}
	return fmt.Errorf("unknown step %q", s.Kind)
}

// wholeSide says which side of an interrupted move holds the complete content: "src",
// "dst", or "" when neither matches the hash recorded before the move.
func wholeSide(s *Step) string {
	a, _ := fsutil.HashTree(s.Src)
	b, _ := fsutil.HashTree(s.Dst)
	switch {
	case s.Hash != "" && a == s.Hash:
		return "src"
	case s.Hash != "" && b == s.Hash:
		return "dst"
	case s.Hash == "" && a == b:
		return "dst"
	}
	return ""
}

// Rollback undoes the done steps of a journal in reverse order and removes the journal.
// A copy that changed since it was made is moved into the backup, never deleted.
func Rollback(p xdg.Paths, j *Journal) error {
	// Settle the first unfinished step, which may have been interrupted midway.
	for i := range j.Steps {
		s := &j.Steps[i]
		if s.Done {
			continue
		}
		switch s.Kind {
		case KMove:
			if !fsutil.Exists(s.Src) && fsutil.Exists(s.Dst) {
				s.Done = true
			} else if fsutil.Exists(s.Src) && fsutil.Exists(s.Dst) {
				// An interrupted cross-device move: keep whichever side is complete.
				switch wholeSide(s) {
				case "src":
					_ = fsutil.RemoveTree(s.Dst)
				case "dst":
					_ = fsutil.RemoveTree(s.Src)
					s.Done = true
				default:
					return fmt.Errorf("both %s and %s exist and neither matches the recorded hash; settle it by hand", s.Src, s.Dst)
				}
			}
		case KCopy:
			// Only the partial file is ours; an existing destination that differs was not
			// written by this step and is left alone.
			_ = fsutil.RemoveTree(partial(s.Dst))
			if fsutil.Exists(s.Dst) {
				a, _ := fsutil.HashTree(s.Src)
				b, _ := fsutil.HashTree(s.Dst)
				if a == b {
					s.Done = true
				}
			}
		case KLink:
			if fsutil.IsSymlink(s.Dst) && fsutil.LinkTarget(s.Dst) == s.Src {
				s.Done = true
			}
		case KMkdir:
			if fsutil.Exists(s.Dst) {
				s.Done = true
			}
		case KUnlink:
			if !fsutil.Exists(s.Dst) {
				s.Done = true
			}
		}
		break
	}
	for i := len(j.Steps) - 1; i >= 0; i-- {
		s := &j.Steps[i]
		if !s.Done {
			continue
		}
		if err := undo(p, j, s); err != nil {
			_ = save(p, j)
			return fmt.Errorf("rollback of step %d (%s %s): %v", i+1, s.Kind, s.Dst, err)
		}
		s.Done = false
		if err := save(p, j); err != nil {
			return err
		}
	}
	return os.Remove(p.JournalFile())
}

func undo(p xdg.Paths, j *Journal, s *Step) error {
	switch s.Kind {
	case KMove:
		if fsutil.Exists(s.Src) {
			if !fsutil.Exists(s.Dst) {
				return nil
			}
			return fmt.Errorf("%s exists again; move %s back by hand", s.Src, s.Dst)
		}
		if err := fsutil.MoveTree(s.Dst, s.Src); err != nil {
			return err
		}
		removeEmpty(s.Created)
		return nil
	case KCopy:
		if !fsutil.Exists(s.Dst) {
			return nil
		}
		a, _ := fsutil.HashTree(s.Src)
		b, _ := fsutil.HashTree(s.Dst)
		if a == b {
			if err := fsutil.RemoveTree(s.Dst); err != nil {
				return err
			}
			removeEmpty(s.Created)
			return nil
		}
		// Changed since the copy: keep it.
		keep := filepath.Join(j.Backup, "rollback-kept", filepath.Base(s.Dst))
		if j.Backup == "" {
			keep = s.Dst + ".claude-hub-rollback"
		}
		for n := 2; fsutil.Exists(keep); n++ {
			keep = fmt.Sprintf("%s-%d", keep, n)
		}
		return fsutil.MoveTree(s.Dst, keep)
	case KLink:
		if fsutil.IsSymlink(s.Dst) {
			return os.Remove(s.Dst)
		}
		return nil
	case KMkdir:
		_ = os.Remove(s.Dst) // only when empty
		return nil
	case KUnlink:
		if fsutil.Exists(s.Dst) {
			return nil
		}
		return os.Symlink(s.Src, s.Dst)
	case KConfig:
		return applyConfig(p, s.Config, true)
	}
	return nil
}

func applyConfig(p xdg.Paths, op *ConfigOp, inverse bool) error {
	c, _, err := config.Load(p)
	if err != nil {
		return err
	}
	pr := c.Profiles[op.Profile]
	if pr == nil {
		return fmt.Errorf("profile %q is no longer in the config", op.Profile)
	}
	if op.Settings {
		if inverse {
			if !op.OldHadSet {
				pr.Settings = nil
			} else {
				pr.Settings = &config.SettingsCfg{Mode: op.OldMode, Own: op.OldOwn}
			}
		} else {
			pr.Settings = &config.SettingsCfg{Mode: op.NewMode, Own: op.NewOwn}
		}
		return c.Save(p.LockDir())
	}
	val := op.New
	if inverse {
		val = op.Old
	}
	SetShare(pr, op.Item, val)
	return c.Save(p.LockDir())
}

// SetShare sets an item's share state in a profile; extra names are added or removed.
func SetShare(pr *config.Profile, item, val string) {
	if pr.Share == nil {
		pr.Share = &config.Share{}
	}
	for _, it := range config.ShareItems {
		if it == item {
			pr.Share.Set(item, val)
			return
		}
	}
	var out []string
	for _, x := range pr.Share.Extra {
		if x != item {
			out = append(out, x)
		}
	}
	if val == config.Shared {
		out = append(out, item)
	}
	pr.Share.Extra = out
}
