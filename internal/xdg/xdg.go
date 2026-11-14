// Package xdg resolves claude-hub's file locations (design section 3). XDG paths are
// used on macOS too.
package xdg

import (
	"os"
	"path/filepath"
	"strings"
)

// Name is the one constant every other name derives from (design section 15).
const Name = "claude-hub"

// Paths are the hub's own locations.
type Paths struct {
	Home       string // $HOME
	ConfigDir  string // ${XDG_CONFIG_HOME:-~/.config}/claude-hub
	ConfigFile string // ConfigDir/config.toml, or $CLAUDE_HUB_CONFIG
	DataDir    string // ${XDG_DATA_HOME:-~/.local/share}/claude-hub
	StateDir   string // ${XDG_STATE_HOME:-~/.local/state}/claude-hub
}

// Home returns $HOME, falling back to the OS lookup.
func Home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// Get computes the paths from the environment.
func Get() Paths {
	home := Home()
	base := func(env, def string) string {
		if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(home, def)
	}
	p := Paths{
		Home:      home,
		ConfigDir: filepath.Join(base("XDG_CONFIG_HOME", ".config"), Name),
		DataDir:   filepath.Join(base("XDG_DATA_HOME", ".local/share"), Name),
		StateDir:  filepath.Join(base("XDG_STATE_HOME", ".local/state"), Name),
	}
	p.ConfigFile = filepath.Join(p.ConfigDir, "config.toml")
	if v := os.Getenv("CLAUDE_HUB_CONFIG"); v != "" {
		p.ConfigFile = Expand(v, home)
		p.ConfigDir = filepath.Dir(p.ConfigFile)
	}
	return p
}

// BackupsDir is where migration backups go.
func (p Paths) BackupsDir() string { return filepath.Join(p.DataDir, "backups") }

// ShimDir is the folder that holds the opt-in `claude` shim.
func (p Paths) ShimDir() string { return filepath.Join(p.DataDir, "shim") }

// SecretsDir is the file backend's folder.
func (p Paths) SecretsDir() string { return filepath.Join(p.DataDir, "secrets") }

// OverlayFile is a profile's settings overlay.
func (p Paths) OverlayFile(profile string) string {
	return filepath.Join(p.ConfigDir, "profiles", profile, "settings.overlay.json")
}

// ProfileConfigDir is the per-profile folder under the config dir.
func (p Paths) ProfileConfigDir(profile string) string {
	return filepath.Join(p.ConfigDir, "profiles", profile)
}

// ProfileStateDir is the per-profile folder under the state dir.
func (p Paths) ProfileStateDir(profile string) string {
	return filepath.Join(p.StateDir, "profiles", profile)
}

// RunDir is the run registry.
func (p Paths) RunDir() string { return filepath.Join(p.StateDir, "run") }

// LockDir holds lock files.
func (p Paths) LockDir() string { return filepath.Join(p.StateDir, "locks") }

// JournalFile is the migration journal.
func (p Paths) JournalFile() string { return filepath.Join(p.StateDir, "migrate", "journal.json") }

// Expand expands a leading ~ and $HOME / ${HOME}.
func Expand(path, home string) string {
	if path == "" {
		return ""
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	if strings.HasPrefix(path, "$HOME/") || path == "$HOME" {
		return filepath.Join(home, strings.TrimPrefix(path, "$HOME"))
	}
	if strings.HasPrefix(path, "${HOME}/") || path == "${HOME}" {
		return filepath.Join(home, strings.TrimPrefix(path, "${HOME}"))
	}
	return path
}

// Abbrev writes a path under home as ~/...
func Abbrev(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~/" + path[len(home)+1:]
	}
	return path
}
