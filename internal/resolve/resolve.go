// Package resolve finds the real `claude` and selects a profile (design section 9).
package resolve

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wonsong82/claude-hub/internal/config"
)

// FindClaude returns the real claude to exec, as found (never resolved), or "".
// self is the hub's own executable; shimDir is the hub's shim folder.
func FindClaude(c *config.Config, self, shimDir, pathEnv, home string) string {
	selfReal := realpath(self)
	shimReal := realpath(shimDir)
	ok := func(cand string) bool {
		fi, err := os.Stat(cand)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			return false
		}
		r := realpath(cand)
		if r == "" || r == selfReal {
			return false
		}
		if d := realpath(filepath.Dir(cand)); d != "" && shimReal != "" && d == shimReal {
			return false
		}
		return true
	}
	if c != nil && c.Hub.ClaudeBin != "" {
		cand := c.Expand(c.Hub.ClaudeBin)
		if ok(cand) {
			return cand
		}
		return ""
	}
	for _, d := range filepath.SplitList(pathEnv) {
		if d == "" {
			d = "."
		}
		cand := filepath.Join(d, "claude")
		if !filepath.IsAbs(cand) {
			if a, err := filepath.Abs(cand); err == nil {
				cand = a
			}
		}
		if ok(cand) {
			return cand
		}
	}
	cand := filepath.Join(home, ".local", "bin", "claude")
	if ok(cand) {
		return cand
	}
	return ""
}

func realpath(p string) string {
	if p == "" {
		return ""
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	a, err := filepath.Abs(r)
	if err != nil {
		return r
	}
	return a
}

// Source says why a profile was selected.
type Source struct {
	Kind string // argument, env, file, rule, default, none
	Path string // the .claude-profile file or the rule's path
}

func (s Source) String() string {
	switch s.Kind {
	case "argument":
		return "argument"
	case "env":
		return "CLAUDE_HUB_SELECT"
	case "file":
		return s.Path
	case "rule":
		return "directory rule " + s.Path
	case "default":
		return "default_profile"
	}
	return "nothing selected"
}

// Short is the source as shown in the launch announcement.
func (s Source) Short() string {
	switch s.Kind {
	case "file":
		return ".claude-profile"
	case "rule":
		return "directory rule"
	}
	return s.String()
}

// UnknownError is a selected name that is not a defined profile (exit 78).
type UnknownError struct {
	Name   string
	Source Source
}

func (e *UnknownError) Error() string {
	return fmt.Sprintf("profile '%s' (from %s) is not defined; run \"claude-hub profile list\"", e.Name, e.Source)
}

// Select picks the profile for a launch where no profile was named as the first argument:
// CLAUDE_HUB_SELECT, .claude-profile (cwd up to $HOME or /), directory rules, default.
// It returns "" for plain claude.
func Select(c *config.Config, cwd, home, selectEnv string) (string, Source, error) {
	check := func(name string, src Source) (string, Source, error) {
		if _, ok := c.Profiles[name]; !ok {
			return "", src, &UnknownError{Name: name, Source: src}
		}
		return name, src, nil
	}
	if selectEnv != "" {
		return check(selectEnv, Source{Kind: "env"})
	}
	if name, file := FindProfileFile(cwd, home); file != "" {
		return check(name, Source{Kind: "file", Path: file})
	}
	if r, ok := MatchRule(c, cwd); ok {
		return check(r.Profile, Source{Kind: "rule", Path: r.Path})
	}
	if c.Hub.DefaultProfile != "" {
		return check(c.Hub.DefaultProfile, Source{Kind: "default"})
	}
	return "", Source{Kind: "none"}, nil
}

// FindProfileFile looks for .claude-profile in dir and its ancestors, stopping after $HOME
// or at the root. A file with no non-empty line is skipped.
func FindProfileFile(dir, home string) (string, string) {
	dir = filepath.Clean(dir)
	home = filepath.Clean(home)
	for {
		f := filepath.Join(dir, ".claude-profile")
		if name := firstLine(f); name != "" {
			return name, f
		}
		if dir == home || dir == "/" || dir == "." {
			return "", ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			return t
		}
	}
	return ""
}

// MatchRule returns the [[directory]] rule with the longest path that is a prefix of cwd.
func MatchRule(c *config.Config, cwd string) (config.DirRule, bool) {
	cands := []string{filepath.Clean(cwd)}
	if r := realpath(cwd); r != "" && r != cands[0] {
		cands = append(cands, r)
	}
	best, bestLen := config.DirRule{}, -1
	for _, r := range c.Directory {
		rp := filepath.Clean(c.Expand(r.Path))
		rps := []string{rp}
		if rr := realpath(rp); rr != "" && rr != rp {
			rps = append(rps, rr)
		}
		for _, p := range rps {
			for _, d := range cands {
				if under(d, p) && len(p) > bestLen {
					best, bestLen = r, len(p)
				}
			}
		}
	}
	return best, bestLen >= 0
}

func under(d, p string) bool {
	return d == p || p == "/" || strings.HasPrefix(d, p+"/")
}
