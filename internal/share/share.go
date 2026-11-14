// Package share holds the sharing matrix (design section 4): the shareable items, the
// never-share list, and on-disk link checks.
package share

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
)

// Unit says what a migration compares and moves as one thing.
type Unit int

const (
	// UnitEntry: each top-level entry of a folder (a skill folder is one unit).
	UnitEntry Unit = iota
	// UnitFile: each file, recursively (transcripts).
	UnitFile
	// UnitReplace: no merge; the folder is replaced (plugins, sessions).
	UnitReplace
	// UnitSingleFile: the path itself is a file (keybindings.json, CLAUDE.md).
	UnitSingleFile
)

// PathSpec is one on-disk path of an item.
type PathSpec struct {
	Name string // relative to the config dir
	Dir  bool   // a folder (true) or a file (false)
	Unit Unit
}

// Item is a shareable item.
type Item struct {
	Name     string
	Paths    []PathSpec
	KeepBoth bool // whether --on-conflict keep-both is offered
}

// Items is the shareable table (settings is handled by package settings).
var Items = []Item{
	{Name: "sessions", Paths: []PathSpec{{"sessions", true, UnitReplace}}},
	{Name: "projects", Paths: []PathSpec{{"projects", true, UnitFile}, {"file-history", true, UnitFile}}, KeepBoth: true},
	{Name: "skills", Paths: []PathSpec{{"skills", true, UnitEntry}}},
	{Name: "agents", Paths: []PathSpec{{"agents", true, UnitEntry}}},
	{Name: "commands", Paths: []PathSpec{{"commands", true, UnitEntry}}},
	{Name: "hooks", Paths: []PathSpec{{"hooks", true, UnitEntry}}},
	{Name: "plugins", Paths: []PathSpec{{"plugins", true, UnitReplace}}},
	{Name: "keybindings", Paths: []PathSpec{{"keybindings.json", false, UnitSingleFile}}},
	{Name: "memory", Paths: []PathSpec{{"CLAUDE.md", false, UnitSingleFile}, {"rules", true, UnitEntry}}},
}

// neverShare is the never-share list (design section 4). Entries ending in "*" are prefixes.
var neverShare = []string{
	".claude.json", ".credentials.json", "backups",
	"policy-limits.json*", "remote-settings.json*",
	"daemon", "daemon.lock", "daemon.log", "daemon.status.json", "jobs", "state",
	"mcp-needs-auth-cache.json", "statsig", "telemetry",
}

// leftAlone are always-own entries that are never offered (design section 4).
var leftAlone = []string{"history.jsonl", "cache", "paste-cache", "downloads", "session-env", "shell-snapshots",
	"todos", ".last-cleanup", ".last-update-result.json", "settings.json", "settings.json.bak*"}

func init() {
	config.NeverShare = func(n string) bool { return NeverShare(n) || LeftAlone(n) }
	config.KnownItemPath = func(n string) bool { return ItemForPath(n) != "" }
}

func match(list []string, name string) bool {
	for _, x := range list {
		if strings.HasSuffix(x, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(x, "*")) {
				return true
			}
		} else if x == name {
			return true
		}
	}
	return false
}

// NeverShare reports whether a top-level name must never be shared.
func NeverShare(name string) bool { return match(neverShare, name) }

// NeverShareList returns the never-share entries for display.
func NeverShareList() []string { return append([]string(nil), neverShare...) }

// LeftAlone reports whether a name is always own and never offered.
func LeftAlone(name string) bool { return match(leftAlone, name) }

// Lookup returns the item by name. Extra names get a synthetic item: one entry, merged
// per top-level entry when it is a folder.
func Lookup(name string) (Item, bool) {
	for _, it := range Items {
		if it.Name == name {
			return it, true
		}
	}
	return Item{}, false
}

// ExtraItem builds the item for a share.extra name. isDir decides the unit.
func ExtraItem(name string, isDir bool) Item {
	u := UnitSingleFile
	if isDir {
		u = UnitEntry
	}
	return Item{Name: name, Paths: []PathSpec{{name, isDir, u}}, KeepBoth: true}
}

// ItemForPath returns the item that owns a top-level path, or "".
func ItemForPath(p string) string {
	for _, it := range Items {
		for _, ps := range it.Paths {
			if ps.Name == p {
				return it.Name
			}
		}
	}
	return ""
}

// LinkState is the on-disk state of one path of a shared item.
type LinkState int

const (
	// Missing: nothing at the path.
	Missing LinkState = iota
	// Linked: a symlink to the base path.
	Linked
	// WrongLink: a symlink somewhere else.
	WrongLink
	// Real: a real file or folder.
	Real
)

func (s LinkState) String() string {
	return [...]string{"missing", "linked", "linked elsewhere", "real"}[s]
}

// State lstats profileDir/rel and reports how it relates to baseDir/rel. No content is read.
func State(profileDir, baseDir, rel string) LinkState {
	p := filepath.Join(profileDir, rel)
	fi, err := os.Lstat(p)
	if err != nil {
		return Missing
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return Real
	}
	if SameTarget(fsutil.LinkTarget(p), filepath.Dir(p), filepath.Join(baseDir, rel)) {
		return Linked
	}
	return WrongLink
}

// SameTarget compares a link target (relative to dir when not absolute) with want.
func SameTarget(target, dir, want string) bool {
	if target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	if filepath.Clean(target) == filepath.Clean(want) {
		return true
	}
	a, err1 := filepath.EvalSymlinks(target)
	b, err2 := filepath.EvalSymlinks(want)
	return err1 == nil && err2 == nil && a == b
}

// ItemsFor returns the items configured for a profile: the fixed items plus extras.
func ItemsFor(c *config.Config, profile string) []Item {
	out := append([]Item(nil), Items...)
	pr := c.Profiles[profile]
	if pr != nil && pr.Share != nil {
		for _, x := range pr.Share.Extra {
			out = append(out, ExtraItem(x, isDirIn(c.BaseDir(), c.ProfileDir(profile), x)))
		}
	}
	return out
}

func isDirIn(base, prof, name string) bool {
	if fi, err := os.Stat(filepath.Join(base, name)); err == nil {
		return fi.IsDir()
	}
	if fi, err := os.Lstat(filepath.Join(prof, name)); err == nil {
		return fi.IsDir()
	}
	return true
}

// Problem is a link-check finding.
type Problem struct {
	Item  string
	Path  string
	State LinkState
}

// CheckLinks lstats each shared path of a secondary profile and returns those that are not
// links to the base. A missing shared file whose base file is also missing is "pending" and
// not a problem.
func CheckLinks(c *config.Config, profile string) []Problem {
	if c.IsBase(profile) {
		return nil
	}
	base, dir := c.BaseDir(), c.ProfileDir(profile)
	var out []Problem
	for _, it := range ItemsFor(c, profile) {
		if c.ShareState(profile, it.Name) != config.Shared {
			continue
		}
		for _, ps := range it.Paths {
			st := State(dir, base, ps.Name)
			if st == Linked {
				continue
			}
			if st == Missing && !ps.Dir && !fsutil.Exists(filepath.Join(base, ps.Name)) {
				continue // pending
			}
			out = append(out, Problem{Item: it.Name, Path: filepath.Join(dir, ps.Name), State: st})
		}
	}
	return out
}

// EnsureLinks creates missing links for shared items where the profile has nothing. A
// shared folder the base lacks is created empty in the base; a shared file the base lacks
// stays pending. Real files are never touched.
func EnsureLinks(c *config.Config, name string) (made, pending []string) {
	if c.IsBase(name) {
		return nil, nil
	}
	base, dir := c.BaseDir(), c.ProfileDir(name)
	for _, it := range ItemsFor(c, name) {
		if c.ShareState(name, it.Name) != config.Shared {
			continue
		}
		for _, ps := range it.Paths {
			if State(dir, base, ps.Name) != Missing {
				continue
			}
			bp := filepath.Join(base, ps.Name)
			if ps.Dir {
				if !fsutil.Exists(bp) {
					if err := os.MkdirAll(bp, 0o700); err != nil {
						continue
					}
				}
			} else if !fsutil.Exists(bp) {
				pending = append(pending, ps.Name)
				continue
			}
			if os.Symlink(bp, filepath.Join(dir, ps.Name)) == nil {
				made = append(made, ps.Name)
			}
		}
	}
	return made, pending
}
