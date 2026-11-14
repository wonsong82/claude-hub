// Package config loads, validates and writes config.toml (design section 3).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Schema is the config schema this binary reads and writes.
const Schema = 1

// Share states.
const (
	Shared = "shared"
	Own    = "own"
)

// Settings modes.
const (
	ModeShared  = "shared"
	ModeOwn     = "own"
	ModeLayered = "layered"
)

// DefaultBaseDir is Claude Code's default config dir.
const DefaultBaseDir = "~/.claude"

// DefaultOwn is the default list of owned settings paths (decision 4).
var DefaultOwn = []string{"model", "modelSettings", "effortLevel", "theme"}

// ShareItems are the shareable items in table order (design section 4), "extra" excluded.
var ShareItems = []string{"sessions", "projects", "skills", "agents", "commands", "hooks", "plugins", "keybindings", "memory"}

// HubCommands are reserved words: the hub's own commands (design section 7).
var HubCommands = []string{"init", "profile", "key", "default", "use", "which", "settings", "sync", "exec",
	"install-shim", "statusline", "doctor", "backup", "migrate", "self-update", "uninstall",
	"completion", "version", "help"}

// ClaudeCommands are Claude Code subcommands (from `claude --help`); a profile may not be
// named after one, so `claude-hub mcp list` always reaches Claude.
var ClaudeCommands = []string{"agents", "api-key", "attach", "auth", "auto-mode", "config", "doctor", "gateway",
	"help", "import", "install", "kill", "logs", "mcp", "migrate-installer", "plugin", "plugins", "purge",
	"respawn", "rm", "setup-token", "stop", "ultrareview", "update", "upgrade"}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is config.toml.
type Config struct {
	Schema     int                 `toml:"schema"`
	Hub        Hub                 `toml:"hub"`
	Secrets    Secrets             `toml:"secrets"`
	Keys       map[string]*Key     `toml:"keys"`
	Profiles   map[string]*Profile `toml:"profiles"`
	Directory  []DirRule           `toml:"directory"`
	Statusline *Statusline         `toml:"statusline"`
	Tmp        *Tmp                `toml:"tmp"`

	// Path is the file this config was loaded from or will be written to.
	Path string `toml:"-"`
	// Home is used to expand ~.
	Home string `toml:"-"`
}

// Hub is the [hub] table.
type Hub struct {
	DefaultProfile string `toml:"default_profile"`
	BaseDir        string `toml:"base_dir"`
	ClaudeBin      string `toml:"claude_bin"`
	Announce       string `toml:"announce"`
}

// Secrets is the [secrets] table.
type Secrets struct {
	Backend string `toml:"backend"`
}

// Key is key metadata; the token itself is in the secret store.
type Key struct {
	Note string `toml:"note"`
}

// Profile is one [profiles.<name>] table.
type Profile struct {
	Dir      string            `toml:"dir"`
	Key      string            `toml:"key"`
	Icon     string            `toml:"icon"`
	Share    *Share            `toml:"share"`
	Settings *SettingsCfg      `toml:"settings"`
	Env      map[string]string `toml:"env"`
}

// Share is [profiles.<name>.share].
type Share struct {
	Sessions    string   `toml:"sessions"`
	Projects    string   `toml:"projects"`
	Skills      string   `toml:"skills"`
	Agents      string   `toml:"agents"`
	Commands    string   `toml:"commands"`
	Hooks       string   `toml:"hooks"`
	Plugins     string   `toml:"plugins"`
	Keybindings string   `toml:"keybindings"`
	Memory      string   `toml:"memory"`
	Extra       []string `toml:"extra"`
}

// SettingsCfg is [profiles.<name>.settings].
type SettingsCfg struct {
	Mode string   `toml:"mode"`
	Own  []string `toml:"own"`
}

// DirRule is one [[directory]] rule.
type DirRule struct {
	Path    string `toml:"path"`
	Profile string `toml:"profile"`
}

// Statusline is the [statusline] table.
type Statusline struct {
	Segments []string    `toml:"segments"`
	Disks    []Disk      `toml:"disks"`
	Append   []AppendCmd `toml:"append"`
}

// Disk is one disk segment entry.
type Disk struct {
	Label string `toml:"label"`
	Path  string `toml:"path"`
}

// AppendCmd is one appended status-line command.
type AppendCmd struct {
	Command   string `toml:"command"`
	TimeoutMS int    `toml:"timeout_ms"`
}

// Tmp is the [tmp] table.
type Tmp struct {
	Dir        string `toml:"dir"`
	MaxAgeDays int    `toml:"max_age_days"`
}

// Error is a config error (exit 78).
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// New returns an empty config with defaults.
func New(path, home string) *Config {
	c := &Config{Schema: Schema, Path: path, Home: home}
	c.applyDefaults()
	return c
}

func (c *Config) applyDefaults() {
	if c.Hub.BaseDir == "" {
		c.Hub.BaseDir = DefaultBaseDir
	}
	if c.Hub.Announce == "" {
		c.Hub.Announce = "tty"
	}
	if c.Secrets.Backend == "" {
		c.Secrets.Backend = "auto"
	}
	if c.Keys == nil {
		c.Keys = map[string]*Key{}
	}
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
}

// Load reads the config at p.ConfigFile. A missing file gives an empty config and
// exists=false.
func Load(p xdg.Paths) (*Config, bool, error) {
	data, err := os.ReadFile(p.ConfigFile)
	if errors.Is(err, fs.ErrNotExist) {
		return New(p.ConfigFile, p.Home), false, nil
	}
	if err != nil {
		return nil, false, errf("cannot read %s: %v", p.ConfigFile, err)
	}
	c, err := Parse(data, p.ConfigFile, p.Home)
	if err != nil {
		return nil, true, err
	}
	return c, true, nil
}

// Parse decodes and validates config text.
func Parse(data []byte, path, home string) (*Config, error) {
	c := &Config{}
	md, err := toml.Decode(string(data), c)
	if err != nil {
		return nil, errf("%s: %v", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		names := make([]string, len(und))
		for i, k := range und {
			names[i] = k.String()
		}
		return nil, errf("%s: unknown key %s", path, strings.Join(names, ", "))
	}
	c.Path, c.Home = path, home
	if !md.IsDefined("schema") {
		return nil, errf("%s: missing \"schema\"", path)
	}
	if c.Schema > Schema {
		return nil, errf("%s: schema %d is newer than this claude-hub understands (%d); upgrade claude-hub", path, c.Schema, Schema)
	}
	if c.Schema < 1 {
		return nil, errf("%s: invalid schema %d", path, c.Schema)
	}
	c.applyDefaults()
	for name, pr := range c.Profiles {
		if pr == nil {
			c.Profiles[name] = &Profile{}
		}
	}
	for name, k := range c.Keys {
		if k == nil {
			c.Keys[name] = &Key{}
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ValidName checks a profile or key name format.
func ValidName(n string) bool { return nameRE.MatchString(n) }

// ReservedProfileName reports whether n is a hub command or a Claude subcommand.
func ReservedProfileName(n string) bool {
	for _, x := range HubCommands {
		if x == n {
			return true
		}
	}
	for _, x := range ClaudeCommands {
		if x == n {
			return true
		}
	}
	return false
}

// CheckProfileName validates a new profile name.
func CheckProfileName(n string) error {
	if !ValidName(n) {
		return fmt.Errorf("invalid profile name %q: use a-z, 0-9, '-' and '_', starting with a letter or digit, at most 32 characters", n)
	}
	if ReservedProfileName(n) {
		return fmt.Errorf("%q is a claude-hub or Claude Code command and cannot be a profile name", n)
	}
	return nil
}

// CheckKeyName validates a key name.
func CheckKeyName(n string) error {
	if !ValidName(n) {
		return fmt.Errorf("invalid key name %q: use a-z, 0-9, '-' and '_', starting with a letter or digit, at most 32 characters", n)
	}
	return nil
}

// ValidIcon reports whether s is a usable icon: "#rrggbb" or a short non-space string.
func ValidIcon(s string) bool {
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "#") {
		return colorRE.MatchString(s)
	}
	return len([]rune(s)) <= 8 && !strings.ContainsAny(s, " \t\n\"\\")
}

// ValidEnvName reports whether n is a valid environment variable name.
func ValidEnvName(n string) bool { return envNameRE.MatchString(n) }

// ForbiddenEnv reports names a profile's env table may never set: the hub owns them.
func ForbiddenEnv(n string) bool {
	switch n {
	case "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN":
		return true
	}
	return strings.HasPrefix(n, "CLAUDE_HUB_")
}

// SecretLikeEnv reports names that look like secrets (refused unless --not-a-secret).
func SecretLikeEnv(n string) bool {
	u := strings.ToUpper(n)
	return strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET") || strings.Contains(u, "KEY")
}

// Validate checks everything that can be checked without the file system.
func (c *Config) Validate() error {
	switch c.Hub.Announce {
	case "tty", "always", "never":
	default:
		return errf("%s: hub.announce must be \"tty\", \"always\" or \"never\", not %q", c.Path, c.Hub.Announce)
	}
	switch c.Secrets.Backend {
	case "auto", "keychain", "secret-tool", "file":
	default:
		return errf("%s: secrets.backend must be \"auto\", \"keychain\", \"secret-tool\" or \"file\", not %q", c.Path, c.Secrets.Backend)
	}
	if c.Hub.ClaudeBin != "" && !filepath.IsAbs(c.Expand(c.Hub.ClaudeBin)) {
		return errf("%s: hub.claude_bin must be an absolute path", c.Path)
	}
	if c.Hub.DefaultProfile != "" && !ValidName(c.Hub.DefaultProfile) {
		return errf("%s: hub.default_profile %q is not a valid profile name", c.Path, c.Hub.DefaultProfile)
	}
	for name := range c.Keys {
		if err := CheckKeyName(name); err != nil {
			return errf("%s: keys.%s: %v", c.Path, name, err)
		}
	}
	dirs := map[string]string{}
	for _, name := range c.ProfileNames() {
		pr := c.Profiles[name]
		if err := CheckProfileName(name); err != nil {
			return errf("%s: profiles.%s: %v", c.Path, name, err)
		}
		if pr.Key != "" {
			if _, ok := c.Keys[pr.Key]; !ok {
				return errf("%s: profile %q uses key %q, which is not defined under [keys]", c.Path, name, pr.Key)
			}
		}
		if !ValidIcon(pr.Icon) {
			return errf("%s: profiles.%s.icon: use an emoji or \"#rrggbb\"", c.Path, name)
		}
		d := c.ProfileDir(name)
		if other, ok := dirs[d]; ok {
			return errf("%s: profiles %q and %q use the same dir %s", c.Path, other, name, d)
		}
		dirs[d] = name
		base := c.IsBase(name)
		if base && (pr.Share != nil || pr.Settings != nil) {
			return errf("%s: profile %q is the base profile (its dir is hub.base_dir) and cannot have share or settings tables", c.Path, name)
		}
		if pr.Share != nil {
			for _, item := range ShareItems {
				v := pr.Share.Get(item)
				if v != "" && v != Shared && v != Own {
					return errf("%s: profiles.%s.share.%s must be \"shared\" or \"own\", not %q", c.Path, name, item, v)
				}
			}
			for _, x := range pr.Share.Extra {
				if err := CheckExtraName(x); err != nil {
					return errf("%s: profiles.%s.share.extra: %v", c.Path, name, err)
				}
			}
		}
		if pr.Settings != nil {
			switch pr.Settings.Mode {
			case "", ModeShared, ModeOwn, ModeLayered:
			default:
				return errf("%s: profiles.%s.settings.mode must be \"shared\", \"own\" or \"layered\"", c.Path, name)
			}
			for _, p := range pr.Settings.Own {
				if !validDotPath(p) {
					return errf("%s: profiles.%s.settings.own: invalid path %q", c.Path, name, p)
				}
			}
		}
		for k := range pr.Env {
			if !ValidEnvName(k) {
				return errf("%s: profiles.%s.env: invalid variable name %q", c.Path, name, k)
			}
			if ForbiddenEnv(k) {
				return errf("%s: profiles.%s.env: %s is set by claude-hub and cannot be in a profile's env", c.Path, name, k)
			}
		}
	}
	for i, r := range c.Directory {
		if r.Path == "" || r.Profile == "" {
			return errf("%s: [[directory]] rule %d needs both path and profile", c.Path, i+1)
		}
	}
	if c.Statusline != nil {
		for _, s := range c.Statusline.Segments {
			switch s {
			case "profile", "cpu", "ram", "disks", "append":
			default:
				return errf("%s: statusline.segments: unknown segment %q", c.Path, s)
			}
		}
	}
	if c.Tmp != nil && c.Tmp.MaxAgeDays < 0 {
		return errf("%s: tmp.max_age_days cannot be negative", c.Path)
	}
	return nil
}

func validDotPath(p string) bool {
	if p == "" || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") || strings.Contains(p, "..") {
		return false
	}
	return true
}

// Get returns the share state of a fixed item ("" = not written = own).
func (s *Share) Get(item string) string {
	if s == nil {
		return ""
	}
	switch item {
	case "sessions":
		return s.Sessions
	case "projects":
		return s.Projects
	case "skills":
		return s.Skills
	case "agents":
		return s.Agents
	case "commands":
		return s.Commands
	case "hooks":
		return s.Hooks
	case "plugins":
		return s.Plugins
	case "keybindings":
		return s.Keybindings
	case "memory":
		return s.Memory
	}
	return ""
}

// Set sets the share state of a fixed item.
func (s *Share) Set(item, v string) {
	switch item {
	case "sessions":
		s.Sessions = v
	case "projects":
		s.Projects = v
	case "skills":
		s.Skills = v
	case "agents":
		s.Agents = v
	case "commands":
		s.Commands = v
	case "hooks":
		s.Hooks = v
	case "plugins":
		s.Plugins = v
	case "keybindings":
		s.Keybindings = v
	case "memory":
		s.Memory = v
	}
}

// CheckExtraName validates a share.extra entry. The never-share check lives in package share;
// it is wired in through NeverShare to avoid an import cycle.
func CheckExtraName(n string) error {
	if n == "" || strings.ContainsAny(n, "/\\") || n == "." || n == ".." {
		return fmt.Errorf("%q is not a top-level entry name", n)
	}
	if NeverShare != nil && NeverShare(n) {
		return fmt.Errorf("%q is on the never-share list", n)
	}
	for _, it := range ShareItems {
		if it == n {
			return fmt.Errorf("%q is a built-in item; set share.%s instead", n, n)
		}
	}
	if KnownItemPath != nil && KnownItemPath(n) {
		return fmt.Errorf("%q belongs to a built-in item", n)
	}
	return nil
}

// NeverShare is set by package share.
var NeverShare func(name string) bool

// KnownItemPath is set by package share: whether a name is a path of a built-in item.
var KnownItemPath func(name string) bool

// Expand expands ~ in a configured path.
func (c *Config) Expand(p string) string { return xdg.Expand(p, c.Home) }

// BaseDir is the expanded hub.base_dir.
func (c *Config) BaseDir() string { return filepath.Clean(c.Expand(c.Hub.BaseDir)) }

// ProfileDir is the profile's effective config dir: its dir, or ~/.claude without one.
func (c *Config) ProfileDir(name string) string {
	pr := c.Profiles[name]
	if pr == nil || pr.Dir == "" {
		return filepath.Clean(c.Expand(DefaultBaseDir))
	}
	return filepath.Clean(c.Expand(pr.Dir))
}

// IsBase reports whether the profile is the base profile.
func (c *Config) IsBase(name string) bool { return c.ProfileDir(name) == c.BaseDir() }

// ProfileNames returns the profile names, sorted.
func (c *Config) ProfileNames() []string {
	out := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// KeyNames returns key names, sorted.
func (c *Config) KeyNames() []string {
	out := make([]string, 0, len(c.Keys))
	for n := range c.Keys {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ProfilesUsingKey lists profiles whose key is k.
func (c *Config) ProfilesUsingKey(k string) []string {
	var out []string
	for _, n := range c.ProfileNames() {
		if c.Profiles[n].Key == k {
			out = append(out, n)
		}
	}
	return out
}

// SettingsMode returns the profile's settings mode ("" for the base profile).
func (c *Config) SettingsMode(name string) string {
	if c.IsBase(name) {
		return ""
	}
	pr := c.Profiles[name]
	if pr.Settings == nil || pr.Settings.Mode == "" {
		return ModeOwn
	}
	return pr.Settings.Mode
}

// OwnPaths returns the owned settings paths.
func (c *Config) OwnPaths(name string) []string {
	pr := c.Profiles[name]
	if pr == nil || pr.Settings == nil {
		return nil
	}
	return append([]string(nil), pr.Settings.Own...)
}

// ShareState returns the share state of item for a secondary profile: "shared" or "own".
// For "extra" names, it is "shared" when listed.
func (c *Config) ShareState(name, item string) string {
	pr := c.Profiles[name]
	if pr == nil || pr.Share == nil || c.IsBase(name) {
		return Own
	}
	if v := pr.Share.Get(item); v == Shared {
		return Shared
	}
	for _, x := range pr.Share.Extra {
		if x == item {
			return Shared
		}
	}
	return Own
}

// TmpDir returns the expanded [tmp] dir, or "".
func (c *Config) TmpDir() string {
	if c.Tmp == nil {
		return ""
	}
	return c.Expand(c.Tmp.Dir)
}

// Save writes the config in canonical form: previous file kept as config.toml.bak, temp
// file plus rename, under a lock. It validates first, so a config it cannot read back is
// never written.
func (c *Config) Save(lockDir string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	text := c.Canonical()
	if _, err := Parse([]byte(text), c.Path, c.Home); err != nil {
		return fmt.Errorf("internal error: canonical config does not parse: %v", err)
	}
	l, err := fsutil.LockFile(filepath.Join(lockDir, "config.lock"))
	if err != nil {
		return err
	}
	defer l.Unlock()
	if old, err := os.ReadFile(c.Path); err == nil {
		if string(old) == text {
			return nil
		}
		if err := fsutil.WriteFileAtomic(c.Path+".bak", old, 0o644); err != nil {
			return err
		}
	}
	return fsutil.WriteFileAtomic(c.Path, []byte(text), 0o644)
}

// Canonical serialises the config in canonical form.
func (c *Config) Canonical() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("schema = %d\n\n", Schema)
	w("[hub]\n")
	w("default_profile = %s\n", q(c.Hub.DefaultProfile))
	w("base_dir = %s\n", q(c.Hub.BaseDir))
	w("claude_bin = %s\n", q(c.Hub.ClaudeBin))
	w("announce = %s\n", q(c.Hub.Announce))
	w("\n[secrets]\n")
	w("backend = %s\n", q(c.Secrets.Backend))
	for _, k := range c.KeyNames() {
		w("\n[keys.%s]\n", k)
		w("note = %s\n", q(c.Keys[k].Note))
	}
	for _, n := range c.ProfileNames() {
		pr := c.Profiles[n]
		w("\n[profiles.%s]\n", n)
		if pr.Dir != "" {
			w("dir = %s\n", q(pr.Dir))
		}
		if pr.Key != "" {
			w("key = %s\n", q(pr.Key))
		}
		if pr.Icon != "" {
			w("icon = %s\n", q(pr.Icon))
		}
		if pr.Share != nil {
			w("\n[profiles.%s.share]\n", n)
			for _, item := range ShareItems {
				if v := pr.Share.Get(item); v != "" {
					w("%s = %s\n", item, q(v))
				}
			}
			w("extra = %s\n", qlist(pr.Share.Extra))
		}
		if pr.Settings != nil {
			w("\n[profiles.%s.settings]\n", n)
			mode := pr.Settings.Mode
			if mode == "" {
				mode = ModeOwn
			}
			w("mode = %s\n", q(mode))
			w("own = %s\n", qlist(pr.Settings.Own))
		}
		if len(pr.Env) > 0 {
			w("\n[profiles.%s.env]\n", n)
			keys := make([]string, 0, len(pr.Env))
			for k := range pr.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				w("%s = %s\n", k, q(pr.Env[k]))
			}
		}
	}
	for _, r := range c.Directory {
		w("\n[[directory]]\n")
		w("path = %s\n", q(r.Path))
		w("profile = %s\n", q(r.Profile))
	}
	if c.Statusline != nil {
		s := c.Statusline
		w("\n[statusline]\n")
		w("segments = %s\n", qlist(s.Segments))
		var disks []string
		for _, d := range s.Disks {
			disks = append(disks, fmt.Sprintf("{ label = %s, path = %s }", q(d.Label), q(d.Path)))
		}
		w("disks = [%s]\n", strings.Join(disks, ", "))
		var apps []string
		for _, a := range s.Append {
			apps = append(apps, fmt.Sprintf("{ command = %s, timeout_ms = %d }", q(a.Command), a.TimeoutMS))
		}
		w("append = [%s]\n", strings.Join(apps, ", "))
	}
	if c.Tmp != nil {
		w("\n[tmp]\n")
		w("dir = %s\n", q(c.Tmp.Dir))
		w("max_age_days = %d\n", c.Tmp.MaxAgeDays)
	}
	return b.String()
}

// q quotes a TOML basic string.
func q(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(`\u` + fmt.Sprintf("%04X", r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func qlist(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = q(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Itoa is a small helper for callers formatting counts.
func Itoa(n int) string { return strconv.Itoa(n) }
