package migrate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/share"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Conflict policies.
const (
	Abort       = "abort"
	KeepBase    = "keep-base"
	KeepProfile = "keep-profile"
	KeepBoth    = "keep-both"
)

// Unit outcome kinds.
const (
	Add       = "add"
	Duplicate = "duplicate"
	Conflict  = "conflict"
)

// UnitPlan is one compared unit.
type UnitPlan struct {
	Path     string // relative to the item path (or the item path itself for a file)
	Kind     string // add, duplicate, conflict
	Size     int64
	Decision string // for conflicts: the policy applied
	NewName  string // keep-both: the name it gets in the base
}

// PathPlan is the plan for one path of an item.
type PathPlan struct {
	Name    string
	State   share.LinkState
	Action  string // "link", "already linked", "pending", "merge", "replace", "copy", "empty", "nothing"
	Units   []UnitPlan
	Missing []string // plugins/marketplaces the base lacks (plugins only)
}

// Plan is a share or unshare plan.
type Plan struct {
	Operation string
	Profile   string
	Item      string
	Paths     []PathPlan
	// Conflicts lists unresolved conflicts (policy abort).
	Conflicts []string
	// Confirm lists reasons the operation needs confirmation (--yes).
	Confirm []string
	// WritesBase is true when the base gains or changes content.
	WritesBase bool
	Journal    *Journal
}

// Options for planning.
type Options struct {
	OnConflict string // abort (default), keep-base, keep-profile, keep-both
	Unshare    string // copy or empty
}

// PlanShare plans own -> shared for one item of a secondary profile.
func PlanShare(p xdg.Paths, c *config.Config, profile string, it share.Item, opt Options) (*Plan, error) {
	if c.IsBase(profile) {
		return nil, fmt.Errorf("profile %q is the base profile; it owns everything", profile)
	}
	if opt.OnConflict == "" {
		opt.OnConflict = Abort
	}
	if opt.OnConflict == KeepBoth && !it.KeepBoth {
		return nil, fmt.Errorf("--on-conflict keep-both is offered for projects and extra items only; a renamed %s would be a duplicate definition", it.Name)
	}
	pdir, bdir := c.ProfileDir(profile), c.BaseDir()
	bk := NewBackupDir(p, profile, it.Name)
	pl := &Plan{Operation: "share", Profile: profile, Item: it.Name}
	j := &Journal{Operation: "share", Profile: profile, Item: it.Name, Backup: bk}
	usedBackup := false
	for _, ps := range it.Paths {
		pp, bp := filepath.Join(pdir, ps.Name), filepath.Join(bdir, ps.Name)
		st := share.State(pdir, bdir, ps.Name)
		pp2 := PathPlan{Name: ps.Name, State: st}
		switch st {
		case share.Linked:
			pp2.Action = "already linked"
		case share.WrongLink:
			return nil, fmt.Errorf("%s is a link to %s, not to the base; fix it by hand first", pp, fsutil.LinkTarget(pp))
		case share.Missing:
			if ps.Dir {
				if !fsutil.Exists(bp) {
					j.Steps = append(j.Steps, Step{Kind: KMkdir, Dst: bp})
				}
				j.Steps = append(j.Steps, Step{Kind: KLink, Src: bp, Dst: pp})
				pp2.Action = "link"
			} else if fsutil.Exists(bp) {
				j.Steps = append(j.Steps, Step{Kind: KLink, Src: bp, Dst: pp})
				pp2.Action = "link"
			} else {
				pp2.Action = "pending"
			}
		case share.Real:
			usedBackup = true
			if err := planReal(pl, j, &pp2, ps, pp, bp, bk, profile, opt); err != nil {
				return nil, err
			}
		}
		pl.Paths = append(pl.Paths, pp2)
	}
	if !usedBackup {
		j.Backup = ""
	}
	old := config.Own
	if isFixed(it.Name) {
		old = ""
		if pr := c.Profiles[profile]; pr.Share != nil {
			old = pr.Share.Get(it.Name)
		}
	}
	j.Steps = append(j.Steps, Step{Kind: KConfig, Config: &ConfigOp{Profile: profile, Item: it.Name, New: config.Shared, Old: old}})
	pl.Journal = j
	return pl, nil
}

func isFixed(item string) bool {
	for _, x := range config.ShareItems {
		if x == item {
			return true
		}
	}
	return false
}

func planReal(pl *Plan, j *Journal, pp2 *PathPlan, ps share.PathSpec, pp, bp, bk, profile string, opt Options) error {
	bkProfile := filepath.Join(bk, "profile", ps.Name)
	bkBase := filepath.Join(bk, "base", ps.Name)
	pfi, _ := os.Lstat(pp)
	bfi, berr := os.Stat(bp)
	if berr == nil && pfi.IsDir() != bfi.IsDir() {
		return fmt.Errorf("%s and %s are not the same kind (file vs folder); settle it by hand", pp, bp)
	}
	j.Steps = append(j.Steps, Step{Kind: KMove, Src: pp, Dst: bkProfile})

	if !ps.Dir || ps.Unit == share.UnitSingleFile {
		// A single file.
		u := UnitPlan{Path: ps.Name, Size: pfi.Size()}
		switch {
		case berr != nil:
			u.Kind = Add
			pl.WritesBase = true
			pl.Confirm = append(pl.Confirm, fmt.Sprintf("%s exists only in the profile: it moves to the base, and every sharing profile gets it", ps.Name))
			j.Steps = append(j.Steps, Step{Kind: KCopy, Src: bkProfile, Dst: bp})
		case same(pp, bp):
			u.Kind = Duplicate
		default:
			u.Kind = Conflict
			u.Decision = opt.OnConflict
			switch opt.OnConflict {
			case KeepBase:
			case KeepProfile:
				pl.WritesBase = true
				pl.Confirm = append(pl.Confirm, fmt.Sprintf("keep-profile replaces the base's %s for every sharing profile", ps.Name))
				j.Steps = append(j.Steps, Step{Kind: KMove, Src: bp, Dst: bkBase}, Step{Kind: KCopy, Src: bkProfile, Dst: bp})
			default:
				pl.Conflicts = append(pl.Conflicts, ps.Name)
			}
		}
		pp2.Units = []UnitPlan{u}
		pp2.Action = "merge"
		j.Steps = append(j.Steps, Step{Kind: KLink, Src: bp, Dst: pp})
		return nil
	}

	if ps.Unit == share.UnitReplace {
		pp2.Action = "replace"
		if berr != nil {
			j.Steps = append(j.Steps, Step{Kind: KMkdir, Dst: bp})
		}
		if ps.Name == "plugins" {
			pp2.Missing = missingPlugins(pp, bp)
			if len(pp2.Missing) > 0 {
				pl.Confirm = append(pl.Confirm, fmt.Sprintf("the base lacks %d plugin(s) or marketplace(s) this profile had; reinstall them after sharing", len(pp2.Missing)))
			}
		}
		j.Steps = append(j.Steps, Step{Kind: KLink, Src: bp, Dst: pp})
		return nil
	}

	pp2.Action = "merge"
	if berr != nil {
		pl.WritesBase = true
		units, _ := listUnits(pp, ps.Unit)
		for _, u := range units {
			pp2.Units = append(pp2.Units, UnitPlan{Path: u, Kind: Add, Size: fsutil.TreeSize(filepath.Join(pp, u))})
		}
		j.Steps = append(j.Steps, Step{Kind: KCopy, Src: bkProfile, Dst: bp})
		j.Steps = append(j.Steps, Step{Kind: KLink, Src: bp, Dst: pp})
		return nil
	}
	units, err := listUnits(pp, ps.Unit)
	if err != nil {
		return err
	}
	var copies []Step
	var replaced []Step
	renamed := map[string]string{}
	for _, u := range units {
		src, dst := filepath.Join(pp, u), filepath.Join(bp, u)
		up := UnitPlan{Path: u, Size: fsutil.TreeSize(src)}
		blocked := blockedByFile(bp, u)
		switch {
		case !fsutil.Exists(dst) && !blocked:
			up.Kind = Add
			copies = append(copies, Step{Kind: KCopy, Src: filepath.Join(bkProfile, u), Dst: dst})
		case !blocked && same(src, dst):
			up.Kind = Duplicate
		default:
			up.Kind = Conflict
			up.Decision = opt.OnConflict
			switch opt.OnConflict {
			case KeepBase:
			case KeepProfile:
				if blocked {
					pl.Conflicts = append(pl.Conflicts, filepath.Join(ps.Name, u)+" (a file in the base is in the way)")
					break
				}
				pl.Confirm = append(pl.Confirm, fmt.Sprintf("keep-profile replaces the base's %s for every sharing profile", filepath.Join(ps.Name, u)))
				replaced = append(replaced, Step{Kind: KMove, Src: dst, Dst: filepath.Join(bkBase, u)})
				copies = append(copies, Step{Kind: KCopy, Src: filepath.Join(bkProfile, u), Dst: dst})
			case KeepBoth:
				nn := keepBothName(bp, u, profile)
				if blocked {
					nn = renameBlocked(bp, u, profile, renamed)
				}
				up.NewName = nn
				copies = append(copies, Step{Kind: KCopy, Src: filepath.Join(bkProfile, u), Dst: filepath.Join(bp, nn)})
			default:
				pl.Conflicts = append(pl.Conflicts, filepath.Join(ps.Name, u))
			}
		}
		pp2.Units = append(pp2.Units, up)
	}
	if len(copies) > 0 {
		pl.WritesBase = true
	}
	j.Steps = append(j.Steps, replaced...)
	j.Steps = append(j.Steps, copies...)
	j.Steps = append(j.Steps, Step{Kind: KLink, Src: bp, Dst: pp})
	return nil
}

// blockedByFile reports whether an ancestor of rel inside base is not a folder.
func blockedByFile(base, rel string) bool {
	parts := strings.Split(rel, string(filepath.Separator))
	cur := base
	for _, p := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if err != nil {
			return false
		}
		if !fi.IsDir() {
			return true
		}
	}
	return false
}

// renameBlocked renames the first ancestor of rel that is a file in base (keep-both), so
// that the unit can live under <ancestor>.<profile>/...; units under the same ancestor share
// the new name.
func renameBlocked(base, rel, profile string, memo map[string]string) string {
	parts := strings.Split(rel, string(filepath.Separator))
	cur := base
	for i, p := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if err != nil || fi.IsDir() {
			continue
		}
		prefix := filepath.Join(parts[:i+1]...)
		nn, ok := memo[prefix]
		if !ok {
			nn = keepBothName(base, prefix, profile)
			memo[prefix] = nn
		}
		return filepath.Join(append([]string{nn}, parts[i+1:]...)...)
	}
	return keepBothName(base, rel, profile)
}

func same(a, b string) bool {
	ha, e1 := fsutil.HashTree(a)
	hb, e2 := fsutil.HashTree(b)
	return e1 == nil && e2 == nil && ha == hb && ha != ""
}

// keepBothName returns <name>.<profile> (before the extension for files with one, so a
// transcript stays a .jsonl), made unique.
func keepBothName(base, rel, profile string) string {
	dir, name := filepath.Split(rel)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	cand := filepath.Join(dir, stem+"."+profile+ext)
	for n := 2; fsutil.Exists(filepath.Join(base, cand)); n++ {
		cand = filepath.Join(dir, fmt.Sprintf("%s.%s-%d%s", stem, profile, n, ext))
	}
	return cand
}

// listUnits returns the merge units of a folder: top-level entries, or every non-folder
// recursively (links are units, never followed).
func listUnits(dir string, u share.Unit) ([]string, error) {
	var out []string
	if u == share.UnitEntry {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return out, nil
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// missingPlugins lists plugins and marketplaces in the profile's registries that the base's
// registries lack.
func missingPlugins(profilePlugins, basePlugins string) []string {
	var out []string
	for _, f := range []string{"installed_plugins.json", "known_marketplaces.json"} {
		pk := registryNames(filepath.Join(profilePlugins, f))
		bk := registryNames(filepath.Join(basePlugins, f))
		for _, k := range pk {
			found := false
			for _, b := range bk {
				if b == k {
					found = true
				}
			}
			if !found {
				kind := "plugin"
				if f == "known_marketplaces.json" {
					kind = "marketplace"
				}
				out = append(out, kind+" "+k)
			}
		}
	}
	return out
}

func registryNames(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	// installed_plugins.json keeps plugins under "plugins"; known_marketplaces.json is a map.
	if inner, ok := m["plugins"].(map[string]any); ok {
		m = inner
	}
	var out []string
	for k := range m {
		if k == "version" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PlanUnshare plans shared -> own for one item.
func PlanUnshare(c *config.Config, profile string, it share.Item, opt Options) (*Plan, error) {
	if c.IsBase(profile) {
		return nil, fmt.Errorf("profile %q is the base profile; it owns everything", profile)
	}
	mode := opt.Unshare
	if it.Name == "sessions" {
		mode = "empty"
	}
	if mode == "" && it.Name != "projects" {
		mode = "copy"
	}
	if mode == "" {
		return nil, fmt.Errorf("unsharing projects needs --copy or --empty")
	}
	pdir, bdir := c.ProfileDir(profile), c.BaseDir()
	pl := &Plan{Operation: "unshare", Profile: profile, Item: it.Name}
	j := &Journal{Operation: "unshare", Profile: profile, Item: it.Name}
	for _, ps := range it.Paths {
		pp, bp := filepath.Join(pdir, ps.Name), filepath.Join(bdir, ps.Name)
		st := share.State(pdir, bdir, ps.Name)
		p2 := PathPlan{Name: ps.Name, State: st}
		switch st {
		case share.Linked:
			j.Steps = append(j.Steps, Step{Kind: KUnlink, Src: fsutil.LinkTarget(pp), Dst: pp})
			switch {
			case mode == "copy" && fsutil.Exists(bp):
				p2.Action = "copy"
				p2.Units = []UnitPlan{{Path: ps.Name, Kind: Add, Size: fsutil.TreeSize(bp)}}
				j.Steps = append(j.Steps, Step{Kind: KCopy, Src: bp, Dst: pp})
			case ps.Dir:
				p2.Action = "empty"
				j.Steps = append(j.Steps, Step{Kind: KMkdir, Dst: pp})
			default:
				p2.Action = "empty"
			}
		case share.WrongLink:
			return nil, fmt.Errorf("%s is a link to %s, not to the base; fix it by hand first", pp, fsutil.LinkTarget(pp))
		default:
			p2.Action = "nothing"
		}
		pl.Paths = append(pl.Paths, p2)
	}
	old := config.Shared
	j.Steps = append(j.Steps, Step{Kind: KConfig, Config: &ConfigOp{Profile: profile, Item: it.Name, New: config.Own, Old: old}})
	pl.Journal = j
	return pl, nil
}

// Describe renders a plan for people.
func (pl *Plan) Describe(home string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s for profile %q:\n", pl.Operation, pl.Item, pl.Profile)
	for _, p := range pl.Paths {
		fmt.Fprintf(&b, "  %s: %s (now %s)\n", p.Name, p.Action, p.State)
		var adds, dups, confs int
		var size int64
		for _, u := range p.Units {
			switch u.Kind {
			case Add:
				adds++
				size += u.Size
			case Duplicate:
				dups++
			case Conflict:
				confs++
				fmt.Fprintf(&b, "    conflict %s (%s) -> %s\n", u.Path, human(u.Size), orDash(u.Decision))
				if u.NewName != "" {
					fmt.Fprintf(&b, "      kept as %s\n", u.NewName)
				}
			}
		}
		if len(p.Units) > 0 {
			fmt.Fprintf(&b, "    %d to add (%s), %d duplicate, %d conflict\n", adds, human(size), dups, confs)
		}
		for _, m := range p.Missing {
			fmt.Fprintf(&b, "    base lacks %s\n", m)
		}
	}
	if pl.Journal != nil && pl.Journal.Backup != "" {
		fmt.Fprintf(&b, "  backup: %s\n", xdg.Abbrev(pl.Journal.Backup, home))
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Human formats a byte count.
func Human(n int64) string { return human(n) }
