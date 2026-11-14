// Package doctor checks the hub's setup (design section 12). It reads only; fixes are
// returned as functions the caller runs with --fix. It never prints or reads out a token.
package doctor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/jsonx"
	"github.com/wonsong82/claude-hub/internal/launch"
	"github.com/wonsong82/claude-hub/internal/migrate"
	"github.com/wonsong82/claude-hub/internal/resolve"
	"github.com/wonsong82/claude-hub/internal/secrets"
	"github.com/wonsong82/claude-hub/internal/settings"
	"github.com/wonsong82/claude-hub/internal/share"
	"github.com/wonsong82/claude-hub/internal/shim"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// Status of a check.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// Check is one finding.
type Check struct {
	Area    string `json:"area"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Advice  string `json:"advice,omitempty"`
	// Fix repairs the finding (safe, local repairs only). Nil when there is none.
	Fix func() (string, error) `json:"-"`
	// DataFix marks a repair that moves data and goes through the share rules.
	DataFix bool `json:"-"`
}

// Env is what the doctor looks at.
type Env struct {
	P        xdg.Paths
	C        *config.Config // nil when the config did not load
	LoadErr  error
	Self     string
	PathEnv  string
	Shell    string // login shell path
	Getenv   func(string) string
	SkipExec bool // tests: do not run claude --version or shells
}

type run struct {
	e   Env
	out []Check
}

func (r *run) add(area, status, msg, advice string) *Check {
	r.out = append(r.out, Check{Area: area, Status: status, Message: msg, Advice: advice})
	return &r.out[len(r.out)-1]
}

// Run performs every check.
func Run(e Env) []Check {
	r := &run{e: e}
	r.config()
	r.realClaude()
	r.shim()
	if e.C != nil {
		r.secrets()
		r.auth()
		r.profiles()
		r.settings()
	}
	r.environment()
	r.state()
	return r.out
}

func (r *run) config() {
	e := r.e
	if e.LoadErr != nil {
		r.add("config", Fail, e.LoadErr.Error(), "fix the file; claude-hub never guesses")
		return
	}
	if e.C == nil {
		return
	}
	if !fsutil.Exists(e.C.Path) {
		r.add("config", Warn, "no config at "+xdg.Abbrev(e.C.Path, e.P.Home), "run \"claude-hub init\"")
		return
	}
	r.add("config", OK, fmt.Sprintf("%s parses (schema %d, %d profile(s), %d key(s))", xdg.Abbrev(e.C.Path, e.P.Home), config.Schema, len(e.C.Profiles), len(e.C.Keys)), "")
	if d := e.C.Hub.DefaultProfile; d != "" {
		if _, ok := e.C.Profiles[d]; !ok {
			r.add("config", Fail, fmt.Sprintf("default_profile '%s' is not defined", d), "run \"claude-hub default <name>\" or \"--none\"")
		}
	}
	for _, rule := range e.C.Directory {
		if _, ok := e.C.Profiles[rule.Profile]; !ok {
			r.add("config", Fail, fmt.Sprintf("directory rule %s names undefined profile '%s'", rule.Path, rule.Profile), "fix or remove the rule")
		}
	}
}

func (r *run) realClaude() {
	e := r.e
	real := resolve.FindClaude(e.C, e.Self, e.P.ShimDir(), e.PathEnv, e.P.Home)
	if real == "" {
		r.add("claude", Fail, "no real claude found", "install Claude Code, or set hub.claude_bin")
		return
	}
	msg := "real claude: " + xdg.Abbrev(real, e.P.Home)
	if !e.SkipExec {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, real, "--version")
		cmd.Env = cleanEnv()
		if out, err := cmd.Output(); err == nil {
			msg += " (" + strings.TrimSpace(firstLine(string(out))) + ")"
		}
	}
	r.add("claude", OK, msg, "")
	if fsutil.IsSymlink(real) {
		t := fsutil.LinkTarget(real)
		if strings.Contains(t, "/versions/") {
			r.add("claude", OK, "it is a link Claude's installer manages ("+xdg.Abbrev(t, e.P.Home)+")", "")
		}
	}
}

func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case launch.VarToken, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func (r *run) shim() {
	e := r.e
	link := filepath.Join(e.P.ShimDir(), "claude")
	installed := shim.Installed(e.P)
	sh := filepath.Base(e.Shell)
	if !installed {
		c := r.add("shim", Fail, "not installed: `claude` bypasses claude-hub", "run \"claude-hub install-shim\"")
		c.Fix = r.shimFix()
	} else {
		t := fsutil.LinkTarget(link)
		if _, err := os.Stat(t); err != nil {
			c := r.add("shim", Fail, "shim target "+t+" is missing", "run \"claude-hub install-shim\"")
			c.Fix = r.shimFix()
		} else {
			r.add("shim", OK, "installed: "+xdg.Abbrev(link, e.P.Home)+" -> "+xdg.Abbrev(t, e.P.Home), "")
		}
		ents, _ := os.ReadDir(e.P.ShimDir())
		if len(ents) != 1 {
			r.add("shim", Fail, fmt.Sprintf("%s holds %d entries; it must hold only claude", e.P.ShimDir(), len(ents)), "remove the extra entries")
		}
		for _, f := range shim.RCFiles(sh, e.P.Home) {
			b, err := os.ReadFile(f)
			present, last := shim.BlockIsLast(string(b))
			switch {
			case err != nil || !present:
				c := r.add("shim", Warn, "no shim block in "+xdg.Abbrev(f, e.P.Home), "run \"claude-hub install-shim\"")
				c.Fix = r.shimFix()
			case !last:
				c := r.add("shim", Warn, "the shim block is not last in "+xdg.Abbrev(f, e.P.Home)+" (another installer appended after it)", "run \"claude-hub install-shim\" to move it last")
				c.Fix = r.shimFix()
			default:
				r.add("shim", OK, "block last in "+xdg.Abbrev(f, e.P.Home), "")
			}
		}
		if !e.SkipExec && e.Shell != "" {
			got, err := shim.CleanShell(e.Shell, e.P.Home, "command -v claude")
			if err != nil || filepath.Clean(got) != link {
				c := r.add("shim", Warn, "in a clean "+sh+" login shell, claude is "+orNone(got)+", not the shim", "run \"claude-hub install-shim\"")
				c.Fix = r.shimFix()
			} else {
				r.add("shim", OK, "a clean "+sh+" login shell finds the shim", "")
			}
		}
	}
	// Stray claude executables: neither the shim nor the real claude.
	real := resolve.FindClaude(e.C, e.Self, e.P.ShimDir(), e.PathEnv, e.P.Home)
	seen := map[string]bool{}
	for _, d := range filepath.SplitList(e.PathEnv) {
		cand := filepath.Join(d, "claude")
		if seen[cand] {
			continue
		}
		seen[cand] = true
		fi, err := os.Stat(cand)
		if err != nil || fi.IsDir() {
			continue
		}
		if filepath.Clean(cand) == link || cand == real {
			continue
		}
		if rp, _ := filepath.EvalSymlinks(cand); rp != "" {
			if sp, _ := filepath.EvalSymlinks(e.Self); sp != "" && rp == sp {
				r.add("shim", Warn, "another claude-hub link named claude: "+xdg.Abbrev(cand, e.P.Home), "remove it; \"claude-hub install-shim\" manages the shim")
				continue
			}
			if rr, _ := filepath.EvalSymlinks(real); rr != "" && rp == rr {
				continue
			}
		}
		r.add("shim", Warn, "stray claude on PATH: "+xdg.Abbrev(cand, e.P.Home), "an old launcher or shim? remove it if it is not wanted")
	}
}

func orNone(s string) string {
	if s == "" {
		return "not found"
	}
	return s
}

func (r *run) shimFix() func() (string, error) {
	e := r.e
	return func() (string, error) {
		shells := shim.Shells()
		if sh := filepath.Base(e.Shell); len(shells) == 0 && (sh == "zsh" || sh == "bash" || sh == "fish") {
			shells = []string{sh}
		}
		_, err := shim.Install(e.P, shim.StablePath(e.Self), shells, false)
		return "installed the shim and put its blocks last", err
	}
}

func (r *run) secrets() {
	e := r.e
	c := e.C
	if len(c.Keys) == 0 {
		r.add("secrets", OK, "no keys, all profiles use Claude's login", "")
		return
	}
	be, err := secrets.Open(c.Secrets.Backend, e.P)
	if err != nil {
		r.add("secrets", Fail, err.Error(), "set secrets.backend")
		return
	}
	r.add("secrets", OK, "backend: "+be.Name(), "")
	if fb, ok := be.(*secrets.File); ok {
		if err := fb.CheckModes(""); err != nil {
			r.add("secrets", Fail, err.Error(), "chmod 700 the folder")
		}
	}
	for _, k := range c.KeyNames() {
		users := c.ProfilesUsingKey(k)
		t, err := be.Get(k)
		if err == nil {
			r.add("secrets", OK, fmt.Sprintf("key %s: %s (used by %s)", k, secrets.Fingerprint(t), joinOr(users, "none")), "")
			continue
		}
		status := Warn
		if len(users) > 0 {
			status = Fail
		}
		why := "not in the store"
		if !errors.Is(err, secrets.ErrNotFound) {
			why = "unreadable: " + err.Error()
		}
		r.add("secrets", status, fmt.Sprintf("key %s is %s (used by %s)", k, why, joinOr(users, "none")), "run \"claude-hub key set "+k+"\"")
	}
	if names, ok, _ := be.List(); ok {
		for _, n := range names {
			if _, known := c.Keys[n]; !known {
				r.add("secrets", Warn, "the store holds key "+n+", which the config does not know", "add it under [keys], or delete it from the store")
			}
		}
	}
}

func joinOr(xs []string, empty string) string {
	if len(xs) == 0 {
		return empty
	}
	return strings.Join(xs, ", ")
}

func (r *run) auth() {
	c := r.e.C
	for _, n := range c.ProfileNames() {
		pr := c.Profiles[n]
		if pr.Key != "" {
			r.add("auth", OK, fmt.Sprintf("%s: key %s", n, pr.Key), "")
			continue
		}
		msg := n + ": login"
		if runtime.GOOS == "linux" {
			if fsutil.Exists(filepath.Join(c.ProfileDir(n), ".credentials.json")) {
				msg += " (logged in)"
			} else {
				msg += " (not logged in yet: start the profile and run /login)"
			}
		}
		r.add("auth", OK, msg, "")
	}
}

func (r *run) profiles() {
	e := r.e
	c := e.C
	for _, n := range c.ProfileNames() {
		dir := c.ProfileDir(n)
		fi, err := os.Stat(dir)
		if err != nil {
			r.add("profiles", Warn, fmt.Sprintf("%s: %s does not exist yet", n, xdg.Abbrev(dir, e.P.Home)), "it is created when Claude first runs under the profile")
			continue
		}
		if fi.Mode().Perm()&0o077 != 0 && !c.IsBase(n) {
			r.add("profiles", Warn, fmt.Sprintf("%s: %s has mode %o", n, xdg.Abbrev(dir, e.P.Home), fi.Mode().Perm()), "chmod 700 is suggested")
		}
		// Never-share entries must never be links.
		ents, _ := os.ReadDir(dir)
		for _, ent := range ents {
			if share.NeverShare(ent.Name()) && ent.Type()&os.ModeSymlink != 0 {
				r.add("profiles", Fail, fmt.Sprintf("%s: %s is a link, but it must never be shared", n, ent.Name()), "replace the link with the profile's own copy")
			}
		}
		if c.IsBase(n) {
			r.add("profiles", OK, fmt.Sprintf("%s: base profile at %s", n, xdg.Abbrev(dir, e.P.Home)), "")
			continue
		}
		bad := 0
		for _, pb := range share.CheckLinks(c, n) {
			bad++
			name := n
			item := pb.Item
			switch pb.State {
			case share.Missing:
				ch := r.add("profiles", Warn, fmt.Sprintf("%s: shared %s has no link at %s", n, pb.Item, xdg.Abbrev(pb.Path, e.P.Home)), "\"claude-hub doctor --fix\" creates it")
				ch.Fix = func() (string, error) {
					made, _ := share.EnsureLinks(c, name)
					return "linked " + joinOr(made, "nothing"), nil
				}
			case share.Real:
				ch := r.add("profiles", Warn, fmt.Sprintf("%s: shared %s is a real copy at %s (not a link)", n, pb.Item, xdg.Abbrev(pb.Path, e.P.Home)), fmt.Sprintf("\"claude-hub profile share %s %s\" merges it back", n, pb.Item))
				ch.DataFix = true
				ch.Fix = func() (string, error) {
					if runs := launch.LiveFor(e.P, name); len(runs) > 0 {
						return "", fmt.Errorf("profile '%s' has %d running session(s); stop them, then run \"claude-hub profile share %s %s\"", name, len(runs), name, item)
					}
					cc, _, err := config.Load(e.P)
					if err != nil {
						return "", err
					}
					it, ok := share.Lookup(item)
					if !ok {
						it = share.ExtraItem(item, true)
					}
					pl, err := migrate.PlanShare(e.P, cc, name, it, migrate.Options{})
					if err != nil {
						return "", err
					}
					if len(pl.Conflicts) > 0 || len(pl.Confirm) > 0 {
						return "", fmt.Errorf("%s needs a decision: run \"claude-hub profile share %s %s\"", item, name, item)
					}
					return "merged " + item + " back into the base", migrate.Run(e.P, pl.Journal)
				}
			case share.WrongLink:
				r.add("profiles", Fail, fmt.Sprintf("%s: %s links to %s, not the base", n, xdg.Abbrev(pb.Path, e.P.Home), fsutil.LinkTarget(pb.Path)), "fix the link by hand")
			}
		}
		// Pending shared files, and links the config does not know.
		for _, it := range share.ItemsFor(c, n) {
			shared := c.ShareState(n, it.Name) == config.Shared
			for _, ps := range it.Paths {
				st := share.State(dir, c.BaseDir(), ps.Name)
				if shared && st == share.Missing && !ps.Dir && !fsutil.Exists(filepath.Join(c.BaseDir(), ps.Name)) {
					r.add("profiles", OK, fmt.Sprintf("%s: %s pending (the base has none yet)", n, ps.Name), "")
				}
				if !shared && st == share.Linked {
					r.add("profiles", Warn, fmt.Sprintf("%s: %s is linked to the base but %s is own in the config", n, ps.Name, it.Name), fmt.Sprintf("\"claude-hub profile share %s %s\" records it", n, it.Name))
				}
				// A4: Claude follows one link level; a base file that is itself a link breaks writes.
				if shared && !ps.Dir && st == share.Linked && fsutil.IsSymlink(filepath.Join(c.BaseDir(), ps.Name)) {
					r.add("profiles", Warn, fmt.Sprintf("%s: base %s is itself a link; Claude Code follows only one link level, so writes from '%s' would replace a link", n, ps.Name, n), "make the base file a regular file")
				}
			}
		}
		if bad == 0 {
			r.add("profiles", OK, fmt.Sprintf("%s: %s, shared links in place", n, xdg.Abbrev(dir, e.P.Home)), "")
		}
	}
}

func (r *run) settings() {
	e := r.e
	c := e.C
	for _, n := range c.ProfileNames() {
		mode := c.SettingsMode(n)
		if mode == "" {
			continue
		}
		f := settings.FilesFor(e.P, c, n)
		if mode == config.ModeLayered {
			name := n
			if !fsutil.Exists(f.Last) {
				ch := r.add("settings", Warn, n+": layered, but never synced", "run \"claude-hub sync "+n+"\"")
				ch.Fix = func() (string, error) { _, err := settings.Sync(e.P, c, name); return "synced " + name, err }
			}
			if d, err := settings.LoadDrift(f.Drift); err == nil && len(d) > 0 {
				r.add("settings", Warn, fmt.Sprintf("%s: %d drifted path(s): %s", n, len(d), strings.Join(d.Keys(), ", ")), "run \"claude-hub settings resolve "+n+"\"")
			} else {
				r.add("settings", OK, n+": layered, no drift", "")
			}
		} else {
			r.add("settings", OK, n+": "+mode, "")
		}
		if c.ShareState(n, "plugins") == config.Shared {
			for _, p := range c.OwnPaths(n) {
				if p == "enabledPlugins" || p == "extraKnownMarketplaces" {
					r.add("settings", Warn, fmt.Sprintf("%s shares plugins but owns %s", n, p), "inherit it: \"claude-hub settings own "+n+" remove "+p+"\"")
				}
			}
		}
	}
	// The status line command resolves.
	base := filepath.Join(c.BaseDir(), settings.FileName)
	if o, _, err := settings.ReadObject(base); err == nil {
		if v, ok := jsonx.Get(o, jsonx.Path{"statusLine", "command"}); ok {
			if s, ok := v.(string); ok && s != "" {
				first := strings.Fields(s)[0]
				first = xdg.Expand(first, e.P.Home)
				_, err := exec.LookPath(first)
				if filepath.IsAbs(first) {
					_, err = os.Stat(first)
				}
				if err != nil {
					r.add("settings", Warn, "statusLine command "+first+" does not resolve", "run \"claude-hub statusline install --force\"")
				} else {
					r.add("settings", OK, "statusLine command resolves", "")
				}
			}
		}
	}
}

// tokenRE matches setup-token-shaped strings (built from parts so the source holds no token prefix).
var tokenRE = regexp.MustCompile("sk" + "-" + "ant" + `-[A-Za-z0-9_-]{8,}`)

func (r *run) environment() {
	e := r.e
	inSession := e.Getenv(launch.VarProfile) != ""
	if !inSession {
		for _, k := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
			if e.Getenv(k) != "" {
				adv := "claude-hub overrides it for every profile it selects; plain claude still sees it"
				if strings.HasPrefix(k, "ANTHROPIC_") {
					adv = "claude-hub unsets it for every selected profile; plain claude would bill through it"
				}
				r.add("environment", Warn, k+" is set in your environment", adv)
			}
		}
	}
	files := []string{}
	for _, f := range shim.AllRCFiles(e.P.Home) {
		files = append(files, f)
	}
	files = append(files, filepath.Join(e.P.Home, ".zshenv"), filepath.Join(e.P.Home, ".bashrc"))
	sort.Strings(files)
	seen := map[string]bool{}
	found := false
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		ln := 0
		for sc.Scan() {
			ln++
			if tokenRE.MatchString(sc.Text()) {
				r.add("environment", Warn, fmt.Sprintf("a token-shaped string is in %s line %d", xdg.Abbrev(f, e.P.Home), ln), "move it into the secret store with \"claude-hub key add\"")
				found = true
			}
		}
		fh.Close()
	}
	if !found {
		r.add("environment", OK, "no token-shaped strings in shell rc files", "")
	}
}

func (r *run) state() {
	e := r.e
	if j, err := migrate.Pending(e.P); err != nil {
		r.add("state", Fail, err.Error(), "")
	} else if j != nil {
		r.add("state", Fail, fmt.Sprintf("unfinished migration: %s %s of '%s'", j.Operation, j.Item, j.Profile), "run \"claude-hub migrate resume\" or \"claude-hub migrate rollback\"")
	}
	if n := launch.Stale(e.P); n > 0 {
		ch := r.add("state", Warn, fmt.Sprintf("%d stale run registry entr%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]), "\"claude-hub doctor --fix\" prunes them")
		ch.Fix = func() (string, error) { launch.Live(e.P); return "pruned stale run entries", nil }
	}
	ents, _ := os.ReadDir(e.P.BackupsDir())
	var size int64
	for _, ent := range ents {
		size += fsutil.TreeSize(filepath.Join(e.P.BackupsDir(), ent.Name()))
	}
	r.add("state", OK, fmt.Sprintf("%d backup(s), %s", len(ents), migrate.Human(size)), "")
	r.add("state", OK, "live sessions are known only for sessions started through claude-hub", "")
}
