// Package cli is claude-hub's command line: first-argument dispatch, the launch path, and
// the hub commands (design section 7).
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/launch"
	"github.com/wonsong82/claude-hub/internal/resolve"
	"github.com/wonsong82/claude-hub/internal/secrets"
	"github.com/wonsong82/claude-hub/internal/settings"
	"github.com/wonsong82/claude-hub/internal/share"
	"github.com/wonsong82/claude-hub/internal/ui"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Exit codes (design section 7).
const (
	ExitOK        = 0
	ExitFail      = 1
	ExitUsage     = 64
	ExitConfig    = 78
	ExitRecursion = 126
	ExitNoClaude  = 127
)

// ExitError carries an exit code and a message.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

func usage(format string, a ...any) error {
	return &ExitError{Code: ExitUsage, Msg: fmt.Sprintf(format, a...)}
}

func fail(format string, a ...any) error {
	return &ExitError{Code: ExitFail, Msg: fmt.Sprintf(format, a...)}
}

func cfgErr(format string, a ...any) error {
	return &ExitError{Code: ExitConfig, Msg: fmt.Sprintf(format, a...)}
}

// silent is an error whose message was already printed.
type silent struct{ code int }

func (s *silent) Error() string { return "" }

// Ctx is shared by commands.
type Ctx struct {
	P      xdg.Paths
	IO     *ui.IO
	Self   string
	cfg    *config.Config
	exists bool
}

// Config loads the config once.
func (x *Ctx) Config() (*config.Config, error) {
	if x.cfg != nil {
		return x.cfg, nil
	}
	c, ok, err := config.Load(x.P)
	if err != nil {
		return nil, err
	}
	x.cfg, x.exists = c, ok
	return c, nil
}

// Save writes the config.
func (x *Ctx) Save(c *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	if err := c.Save(x.P.LockDir()); err != nil {
		return err
	}
	x.cfg, x.exists = c, true
	return nil
}

// Reload drops the cached config.
func (x *Ctx) Reload() (*config.Config, error) {
	x.cfg = nil
	return x.Config()
}

// Main runs claude-hub with argv and returns the exit code (when it does not exec).
func Main(argv []string) int {
	io := ui.Std()
	asClaude := filepath.Base(argv[0]) == "claude"
	if g := os.Getenv(launch.VarGuard); g != "" && g == strconv.Itoa(os.Getpid()) {
		fmt.Fprintln(io.Err, "claude-hub: recursion: claude-hub was exec'ed by itself (check hub.claude_bin and PATH)")
		return ExitRecursion
	}
	self, _ := os.Executable()
	x := &Ctx{P: xdg.Get(), IO: io, Self: self}
	args := argv[1:]
	var err error
	switch {
	case !asClaude && len(args) > 0 && args[0] == "__clean-tmp":
		if len(args) == 3 {
			days, _ := strconv.Atoi(args[2])
			_ = launch.CleanTmp(args[1], days, x.P.Home)
		}
		return 0
	case !asClaude && len(args) > 0 && isHubCommand(args[0]):
		launch.Live(x.P) // prune stale run entries
		err = x.runCommand(args[0], args[1:])
	default:
		err = x.launch(args, asClaude)
	}
	return report(io, err)
}

func report(io *ui.IO, err error) int {
	if err == nil {
		return ExitOK
	}
	var s *silent
	if errors.As(err, &s) {
		return s.code
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Msg != "" {
			fmt.Fprintln(io.Err, "claude-hub: "+ee.Msg)
		}
		return ee.Code
	}
	var ce *config.Error
	if errors.As(err, &ce) {
		fmt.Fprintln(io.Err, "claude-hub: "+ce.Msg)
		return ExitConfig
	}
	if errors.Is(err, ui.ErrNotInteractive) {
		fmt.Fprintln(io.Err, "claude-hub: this needs a terminal (or --yes)")
		return ExitUsage
	}
	fmt.Fprintln(io.Err, "claude-hub: "+err.Error())
	return ExitFail
}

func isHubCommand(s string) bool {
	for _, c := range config.HubCommands {
		if c == s {
			return true
		}
	}
	return false
}

// launch implements design section 9.
func (x *Ctx) launch(args []string, asClaude bool) error {
	c, err := x.Config()
	if err != nil {
		return err
	}
	real := resolve.FindClaude(c, x.Self, x.P.ShimDir(), os.Getenv("PATH"), x.P.Home)
	if real == "" {
		if c.Hub.ClaudeBin != "" {
			return &ExitError{Code: ExitNoClaude, Msg: "hub.claude_bin " + c.Hub.ClaudeBin + " is not an executable claude"}
		}
		return &ExitError{Code: ExitNoClaude, Msg: "no real claude found on PATH or in ~/.local/bin (only claude-hub itself)"}
	}
	named := ""
	if len(args) > 0 {
		if !asClaude && args[0] == "--" {
			args = args[1:]
		} else if _, ok := c.Profiles[args[0]]; ok {
			named = args[0]
			args = args[1:]
			if !asClaude && len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
		}
	}
	env := launch.NewEnv(os.Environ())

	// Nested pass-through: a hook, subagent or `claude -p` inside a session keeps the
	// session's profile. Claude Code does not pass the token on to hooks, so a nested call
	// without one gets its own profile's key back.
	if cur, ok := env.Get(launch.VarProfile); ok && cur != "" && named == "" {
		if tok, _ := env.Get(launch.VarToken); tok == "" {
			if pr, ok := c.Profiles[cur]; ok && pr.Key != "" {
				t, err := x.readToken(c, cur)
				if err != nil {
					return err
				}
				env.Set(launch.VarToken, t)
			}
		}
		env.Set(launch.VarGuard, strconv.Itoa(os.Getpid()))
		return x.exec(real, args, env)
	}

	if !asClaude {
		x.shimWarning()
	}
	name := named
	src := resolve.Source{Kind: "argument"}
	if name == "" {
		cwd, _ := os.Getwd()
		sel, _ := env.Get(launch.VarSelect)
		name, src, err = resolve.Select(c, cwd, x.P.Home, sel)
		if err != nil {
			return cfgErr("%v", err)
		}
	}
	if name == "" {
		env.Set(launch.VarGuard, strconv.Itoa(os.Getpid()))
		return x.exec(real, args, env)
	}
	token, hasKey, tmp, err := x.prepare(c, name)
	if err != nil {
		return err
	}
	launch.Apply(env, c, name, token, hasKey, tmp, true)
	launch.Register(x.P, name)
	x.announce(c, name, src)
	if c.Tmp != nil && tmp != "" && c.Tmp.MaxAgeDays > 0 {
		launch.StartTmpClean(x.Self, tmp, c.Tmp.MaxAgeDays, x.P.Home)
	}
	return x.exec(real, args, env)
}

func (x *Ctx) exec(path string, args []string, env *launch.Env) error {
	err := launch.Exec(path, args, env.Pairs())
	return &ExitError{Code: ExitRecursion, Msg: fmt.Sprintf("cannot run %s: %v", path, err)}
}

// prepare checks links, syncs layered settings and reads the token (design section 9, step 6).
func (x *Ctx) prepare(c *config.Config, name string) (token string, hasKey bool, tmp string, err error) {
	for _, pb := range share.CheckLinks(c, name) {
		x.IO.Errf("claude-hub: profile '%s': %s should link to the base but is %s; run \"claude-hub doctor --fix\"\n", name, xdg.Abbrev(pb.Path, x.P.Home), pb.State)
	}
	if c.SettingsMode(name) == config.ModeLayered {
		res, err := settings.Sync(x.P, c, name)
		if err != nil {
			x.IO.Errf("claude-hub: settings sync for '%s' failed: %v\n", name, err)
		} else if res.Drift > 0 {
			x.IO.Errf("claude-hub: %s has %d local change%s to inherited settings; run \"claude-hub settings resolve %s\"\n", name, res.Drift, plural(res.Drift), name)
		}
	}
	if c.Profiles[name].Key != "" {
		token, err = x.readToken(c, name)
		if err != nil {
			return "", false, "", err
		}
		hasKey = true
	}
	if d := c.TmpDir(); d != "" {
		if t, err := launch.PrepareTmp(d); err == nil {
			tmp = t
		} else {
			x.IO.Errf("claude-hub: %s unavailable; using the default temp folder\n", d)
		}
	}
	return token, hasKey, tmp, nil
}

// readToken reads a profile's key, failing closed with exit 78 (design section 8).
func (x *Ctx) readToken(c *config.Config, name string) (string, error) {
	key := c.Profiles[name].Key
	be, err := secrets.Open(c.Secrets.Backend, x.P)
	if err != nil {
		return "", cfgErr("%v", err)
	}
	t, err := be.Get(key)
	if err != nil {
		why := "which the " + be.Name() + " backend did not return"
		if errors.Is(err, secrets.ErrNotFound) {
			why = "which is not in the " + be.Name() + " store"
		} else {
			why += " (" + err.Error() + ")"
		}
		return "", cfgErr("profile '%s' uses key '%s', %s. Fix the store, or run \"claude-hub profile set-key %s <key>\" or \"claude-hub profile unset-key %s\".", name, key, why, name, name)
	}
	return t, nil
}

func authOf(c *config.Config, name string) string {
	if k := c.Profiles[name].Key; k != "" {
		return "key " + k
	}
	return "login"
}

func (x *Ctx) announce(c *config.Config, name string, src resolve.Source) {
	switch c.Hub.Announce {
	case "never":
		return
	case "tty":
		if !ui.IsTerminal(int(os.Stderr.Fd())) {
			return
		}
	}
	x.IO.Errf("claude-hub: profile '%s' (from %s, %s)\n", name, src.Short(), authOf(c, name))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func joinOr(xs []string, empty string) string {
	if len(xs) == 0 {
		return empty
	}
	return strings.Join(xs, ", ")
}
