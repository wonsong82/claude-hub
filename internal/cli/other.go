package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/doctor"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/jsonx"
	"github.com/wonsong82/claude-hub/internal/launch"
	"github.com/wonsong82/claude-hub/internal/migrate"
	"github.com/wonsong82/claude-hub/internal/resolve"
	"github.com/wonsong82/claude-hub/internal/secrets"
	"github.com/wonsong82/claude-hub/internal/selfupdate"
	"github.com/wonsong82/claude-hub/internal/settings"
	"github.com/wonsong82/claude-hub/internal/shim"
	"github.com/wonsong82/claude-hub/internal/statusline"
	"github.com/wonsong82/claude-hub/internal/version"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

func loginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/zsh"
}

func (x *Ctx) cmdInstallShim(args []string) error {
	o, err := parseArgs(args, []string{"dry-run"}, []string{"shell"})
	if err != nil {
		return err
	}
	if len(o.pos) > 0 {
		return usage("usage: claude-hub install-shim [--shell zsh,bash,fish] [--dry-run]")
	}
	shells := splitList(o.strs["shell"])
	if len(shells) == 0 {
		shells = []string{filepath.Base(loginShell())}
	}
	for _, s := range shells {
		if s != "zsh" && s != "bash" && s != "fish" {
			return usage("unsupported shell %q (zsh, bash, fish)", s)
		}
	}
	target := shim.StablePath(x.Self)
	changes, err := shim.Install(x.P, target, shells, o.bools["dry-run"])
	if err != nil {
		return fail("%v", err)
	}
	for _, ch := range changes {
		verb := map[string]string{"add": "add the block to", "repair": "move the block last in", "create": "link"}[ch.Action]
		if o.bools["dry-run"] {
			x.IO.Printf("would %s %s\n", verb, xdg.Abbrev(ch.File, x.P.Home))
		} else {
			x.IO.Printf("%s %s\n", map[string]string{"add": "added the block to", "repair": "moved the block last in", "create": "linked"}[ch.Action], xdg.Abbrev(ch.File, x.P.Home))
		}
	}
	if o.bools["dry-run"] {
		return nil
	}
	if len(changes) == 0 {
		x.IO.Printf("the shim is installed and its blocks are last\n")
	}
	failed := false
	for _, s := range shells {
		sp := shim.ShellPath(s)
		if sp == "" {
			x.IO.Printf("%s: not installed here; skipped the check\n", s)
			continue
		}
		res, ver, ok := shim.Verify(x.P, sp)
		if ok {
			x.IO.Printf("%s: claude -> %s; claude --version: %s\n", s, xdg.Abbrev(res, x.P.Home), ver)
		} else {
			failed = true
			x.IO.Errf("claude-hub: %s check failed: in a clean login shell claude is %s (version: %s)\n", s, orNone(res), orNone(ver))
		}
	}
	x.IO.Printf("open a new terminal for the shim to apply\n")
	if failed {
		return &silent{code: ExitFail}
	}
	return nil
}

func (x *Ctx) cmdUninstallShim(args []string) error {
	o, err := parseArgs(args, []string{"dry-run"}, nil)
	if err != nil {
		return err
	}
	changes, err := shim.Uninstall(x.P, o.bools["dry-run"])
	if err != nil {
		return fail("%v", err)
	}
	for _, ch := range changes {
		pre := "removed"
		if o.bools["dry-run"] {
			pre = "would remove"
		}
		what := "the block from"
		if ch.File == x.P.ShimDir() {
			what = "the shim folder"
			x.IO.Printf("%s %s %s\n", pre, what, xdg.Abbrev(ch.File, x.P.Home))
			continue
		}
		x.IO.Printf("%s %s %s\n", pre, what, xdg.Abbrev(ch.File, x.P.Home))
	}
	if len(changes) == 0 {
		x.IO.Printf("the shim is not installed\n")
	}
	if !o.bools["dry-run"] {
		if sp := loginShell(); sp != "" {
			if got, err := shim.CleanShell(sp, x.P.Home, "command -v claude"); err == nil {
				x.IO.Printf("claude now resolves to %s\n", orNone(got))
			}
		}
	}
	return nil
}

func (x *Ctx) cmdStatusline(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "install":
			return x.statuslineInstall(args[1:])
		case "uninstall":
			return x.statuslineUninstall(args[1:])
		}
		return usage("usage: claude-hub statusline [install [--force] | uninstall]")
	}
	payload, _ := io.ReadAll(io.LimitReader(x.IO.In, 1<<20))
	c, _, err := config.Load(x.P)
	if err != nil {
		c = nil
	}
	x.IO.Printf("%s\n", statusline.Render(c, x.P, os.Getenv(launch.VarProfile), payload))
	return nil
}

func (x *Ctx) statuslineFiles(c *config.Config) []string {
	files := []string{filepath.Join(c.BaseDir(), settings.FileName)}
	for _, n := range c.ProfileNames() {
		if c.SettingsMode(n) == config.ModeOwn {
			files = append(files, filepath.Join(c.ProfileDir(n), settings.FileName))
		}
	}
	return files
}

func (x *Ctx) statuslineInstall(args []string) error {
	o, err := parseArgs(args, []string{"force", "yes"}, nil)
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	cmd := shim.StablePath(x.Self) + " statusline"
	for i, f := range x.statuslineFiles(c) {
		if i > 0 {
			ok, err := x.IO.Confirm("Also set statusLine in "+xdg.Abbrev(f, x.P.Home)+" (own settings)?", o.bools["yes"])
			if err != nil || !ok {
				continue
			}
		}
		obj, exists, err := settings.ReadObject(f)
		if err != nil {
			return fail("%v", err)
		}
		if cur, ok := jsonx.Get(obj, jsonx.Path{"statusLine"}); ok {
			if cc, _ := jsonx.Get(cur, jsonx.Path{"command"}); cc == cmd {
				x.IO.Printf("%s: already set\n", xdg.Abbrev(f, x.P.Home))
				continue
			}
			if !o.bools["force"] {
				return fail("%s already has a different statusLine; add --force to replace it (a backup is kept)", xdg.Abbrev(f, x.P.Home))
			}
		}
		if err := x.backupFile(f, "statusline", exists); err != nil {
			return err
		}
		sl := jsonx.NewObject()
		sl.Set("type", "command")
		sl.Set("command", cmd)
		obj.Set("statusLine", sl)
		if err := fsutil.WriteFileAtomic(f, jsonx.Marshal(obj), 0o644); err != nil {
			return err
		}
		x.IO.Printf("%s: statusLine set\n", xdg.Abbrev(f, x.P.Home))
	}
	return x.syncAll(c)
}

func (x *Ctx) statuslineUninstall(args []string) error {
	o, err := parseArgs(args, []string{"yes"}, nil)
	if err != nil {
		return err
	}
	c, err := x.Config()
	if err != nil {
		return err
	}
	for i, f := range x.statuslineFiles(c) {
		obj, exists, err := settings.ReadObject(f)
		if err != nil || !exists {
			continue
		}
		v, ok := jsonx.Get(obj, jsonx.Path{"statusLine", "command"})
		if !ok {
			continue
		}
		if s, _ := v.(string); !strings.HasSuffix(s, " statusline") || !strings.Contains(s, xdg.Name) {
			x.IO.Printf("%s: statusLine is not claude-hub's; left alone\n", xdg.Abbrev(f, x.P.Home))
			continue
		}
		if i > 0 {
			if ok, err := x.IO.Confirm("Also remove it from "+xdg.Abbrev(f, x.P.Home)+"?", o.bools["yes"]); err != nil || !ok {
				continue
			}
		}
		if err := x.backupFile(f, "statusline", true); err != nil {
			return err
		}
		obj.Delete("statusLine")
		if err := fsutil.WriteFileAtomic(f, jsonx.Marshal(obj), 0o644); err != nil {
			return err
		}
		x.IO.Printf("%s: statusLine removed\n", xdg.Abbrev(f, x.P.Home))
	}
	return x.syncAll(c)
}

func (x *Ctx) backupFile(f, what string, exists bool) error {
	if !exists {
		return nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	bk := migrate.NewBackupDir(x.P, "base", what)
	return fsutil.WriteFileAtomic(filepath.Join(bk, filepath.Base(f)), b, 0o600)
}

func (x *Ctx) syncAll(c *config.Config) error {
	for _, n := range c.ProfileNames() {
		if c.SettingsMode(n) == config.ModeLayered {
			if _, err := settings.Sync(x.P, c, n); err != nil {
				x.IO.Errf("claude-hub: sync %s: %v\n", n, err)
			}
		}
	}
	return nil
}

func (x *Ctx) cmdDoctor(args []string) error {
	o, err := parseArgs(args, []string{"fix", "json"}, nil)
	if err != nil {
		return err
	}
	c, _, loadErr := config.Load(x.P)
	env := doctor.Env{P: x.P, C: c, LoadErr: loadErr, Self: x.Self, PathEnv: os.Getenv("PATH"), Shell: loginShell(), Getenv: os.Getenv,
		SkipExec: os.Getenv("CLAUDE_HUB_DOCTOR_NO_EXEC") == "1"}
	checks := doctor.Run(env)
	if o.bools["fix"] {
		fixed := 0
		for _, ch := range checks {
			if ch.Fix == nil {
				continue
			}
			if ch.DataFix && x.IO.Interactive {
				if ok, _ := x.IO.Confirm(ch.Message+": merge it back?", false); !ok {
					continue
				}
			}
			msg, err := ch.Fix()
			if err != nil {
				x.IO.Errf("claude-hub: fix failed: %v\n", err)
				continue
			}
			x.IO.Printf("fixed: %s\n", msg)
			fixed++
		}
		if c != nil && loadErr == nil {
			_ = x.syncAll(c)
		}
		checks = doctor.Run(env)
	}
	failed := false
	for _, ch := range checks {
		if ch.Status == doctor.Fail {
			failed = true
		}
	}
	if o.bools["json"] {
		if err := x.printJSON(checks); err != nil {
			return err
		}
	} else {
		for _, ch := range checks {
			x.IO.Printf("%-5s %-12s %s\n", ch.Status, ch.Area, ch.Message)
			if ch.Advice != "" && ch.Status != doctor.OK {
				x.IO.Printf("      %-12s -> %s\n", "", ch.Advice)
			}
		}
	}
	if failed {
		return &silent{code: ExitFail}
	}
	return nil
}

func (x *Ctx) cmdBackup(args []string) error {
	if len(args) == 0 {
		return usage("usage: claude-hub backup list | prune --older-than <days> [--yes]")
	}
	type bj struct {
		Name      string `json:"name"`
		Operation string `json:"operation,omitempty"`
		Created   string `json:"created,omitempty"`
		Size      int64  `json:"size"`
	}
	list := func() []bj {
		ents, _ := os.ReadDir(x.P.BackupsDir())
		var out []bj
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			d := filepath.Join(x.P.BackupsDir(), e.Name())
			b := bj{Name: e.Name(), Size: fsutil.TreeSize(d)}
			var m migrate.Manifest
			if raw, err := os.ReadFile(filepath.Join(d, "MANIFEST.json")); err == nil && json.Unmarshal(raw, &m) == nil {
				b.Operation, b.Created = m.Operation, m.Created
			}
			out = append(out, b)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	switch args[0] {
	case "list":
		o, err := parseArgs(args[1:], []string{"json"}, nil)
		if err != nil {
			return err
		}
		l := list()
		if o.bools["json"] {
			if l == nil {
				l = []bj{}
			}
			return x.printJSON(l)
		}
		if len(l) == 0 {
			x.IO.Printf("no backups\n")
		}
		for _, b := range l {
			x.IO.Printf("%-60s %10s %s\n", b.Name, migrate.Human(b.Size), b.Operation)
		}
		return nil
	case "prune":
		o, err := parseArgs(args[1:], []string{"yes"}, []string{"older-than"})
		if err != nil {
			return err
		}
		days, err := strconv.Atoi(o.strs["older-than"])
		if err != nil || days < 0 {
			return usage("usage: claude-hub backup prune --older-than <days> [--yes]")
		}
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		var victims []string
		for _, b := range list() {
			fi, err := os.Stat(filepath.Join(x.P.BackupsDir(), b.Name))
			if err == nil && fi.ModTime().Before(cutoff) {
				victims = append(victims, b.Name)
			}
		}
		if len(victims) == 0 {
			x.IO.Printf("nothing older than %d day(s)\n", days)
			return nil
		}
		for _, v := range victims {
			x.IO.Printf("  %s\n", v)
		}
		ok, err := x.IO.Confirm(fmt.Sprintf("Delete these %d backup(s)?", len(victims)), o.bools["yes"])
		if err != nil {
			return usage("backup prune needs a terminal or --yes")
		}
		if !ok {
			return fail("aborted")
		}
		for _, v := range victims {
			if err := fsutil.RemoveTree(filepath.Join(x.P.BackupsDir(), v)); err != nil {
				return fail("%v", err)
			}
		}
		x.IO.Printf("deleted %d backup(s)\n", len(victims))
		return nil
	}
	return usage("unknown backup command %q", args[0])
}

func (x *Ctx) cmdMigrate(args []string) error {
	if len(args) != 1 || (args[0] != "resume" && args[0] != "rollback") {
		return usage("usage: claude-hub migrate resume | rollback")
	}
	j, err := migrate.Pending(x.P)
	if err != nil {
		return fail("%v", err)
	}
	if j == nil {
		x.IO.Printf("no unfinished migration\n")
		return nil
	}
	if args[0] == "resume" {
		if err := migrate.Resume(x.P, j); err != nil {
			return fail("%v", err)
		}
		x.IO.Printf("finished %s %s of '%s'\n", j.Operation, j.Item, j.Profile)
		return nil
	}
	if err := migrate.Rollback(x.P, j); err != nil {
		return fail("%v", err)
	}
	x.IO.Printf("rolled back %s %s of '%s'\n", j.Operation, j.Item, j.Profile)
	return nil
}

func (x *Ctx) cmdSelfUpdate(args []string) error {
	o, err := parseArgs(args, nil, []string{"version"})
	if err != nil {
		return err
	}
	if shim.IsHomebrew(x.Self) {
		return fail("this is a Homebrew install; run \"brew upgrade claude-hub\"")
	}
	base := selfupdate.Base()
	tag := o.strs["version"]
	if tag == "" {
		tag, err = selfupdate.Latest(base)
		if err != nil {
			return fail("cannot find the latest release: %v", err)
		}
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if "v"+strings.TrimPrefix(version.Version, "v") == tag {
		x.IO.Printf("already at %s\n", tag)
		return nil
	}
	bin, err := selfupdate.Download(base, tag)
	if err != nil {
		return fail("%v", err)
	}
	target, err := filepath.EvalSymlinks(x.Self)
	if err != nil {
		target = x.Self
	}
	if err := selfupdate.Replace(target, bin); err != nil {
		return fail("replacing %s failed: %v", target, err)
	}
	x.IO.Printf("updated %s to %s\n", xdg.Abbrev(target, x.P.Home), tag)
	return nil
}

func (x *Ctx) cmdUninstall(args []string) error {
	o, err := parseArgs(args, []string{"purge", "yes"}, nil)
	if err != nil {
		return err
	}
	brew := shim.IsHomebrew(x.Self)
	if o.bools["purge"] {
		ok, err := x.IO.Confirm("Also delete claude-hub's config, state, backups and its keys in the secret store? Profile folders stay.", o.bools["yes"])
		if err != nil {
			return usage("uninstall --purge needs a terminal or --yes")
		}
		if !ok {
			return fail("aborted")
		}
	}
	if _, err := shim.Uninstall(x.P, false); err != nil {
		return fail("removing the shim: %v", err)
	}
	x.IO.Printf("removed the shim and its rc blocks\n")
	var c *config.Config
	if cc, _, err := config.Load(x.P); err == nil {
		c = cc
	}
	if o.bools["purge"] {
		if c != nil && len(c.Keys) > 0 {
			if be, err := secrets.Open(c.Secrets.Backend, x.P); err == nil {
				for _, k := range c.KeyNames() {
					if err := be.Delete(k); err == nil {
						x.IO.Printf("deleted key %s from the store\n", k)
					}
				}
			}
		}
		for _, d := range []string{x.P.ConfigDir, x.P.StateDir, x.P.DataDir} {
			if fsutil.Exists(d) {
				if err := fsutil.RemoveTree(d); err != nil {
					return fail("%v", err)
				}
				x.IO.Printf("deleted %s\n", xdg.Abbrev(d, x.P.Home))
			}
		}
	} else {
		x.IO.Printf("left in place: config %s, data and backups %s, state %s\n", xdg.Abbrev(x.P.ConfigDir, x.P.Home), xdg.Abbrev(x.P.DataDir, x.P.Home), xdg.Abbrev(x.P.StateDir, x.P.Home))
	}
	if c != nil {
		var dirs []string
		for _, n := range c.ProfileNames() {
			dirs = append(dirs, xdg.Abbrev(c.ProfileDir(n), x.P.Home))
		}
		if len(dirs) > 0 {
			x.IO.Printf("profile folders are never removed: %s\n", strings.Join(dirs, ", "))
		}
	}
	if brew {
		x.IO.Printf("now run: brew uninstall claude-hub\n")
		return nil
	}
	target, err := filepath.EvalSymlinks(x.Self)
	if err != nil {
		target = x.Self
	}
	if err := os.Remove(target); err != nil {
		return fail("removing %s: %v", target, err)
	}
	x.IO.Printf("removed %s\n", xdg.Abbrev(target, x.P.Home))
	return nil
}

func (x *Ctx) cmdVersion(args []string) error {
	x.IO.Printf("claude-hub %s", version.Version)
	if version.Commit != "" {
		x.IO.Printf(" (%s)", version.Commit)
	}
	x.IO.Printf(" %s/%s\n", runtime.GOOS, runtime.GOARCH)
	c, _, err := config.Load(x.P)
	if err != nil {
		c = nil
	}
	real := resolve.FindClaude(c, x.Self, x.P.ShimDir(), os.Getenv("PATH"), x.P.Home)
	if real == "" {
		x.IO.Printf("real claude: not found\n")
		return nil
	}
	x.IO.Printf("real claude: %s\n", real)
	return nil
}

func (x *Ctx) cmdCompletion(args []string) error {
	if len(args) != 1 {
		return usage("usage: claude-hub completion zsh|bash|fish")
	}
	cmds := strings.Join(config.HubCommands, " ")
	switch args[0] {
	case "bash":
		x.IO.Printf(`_claude_hub() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "%s $(claude-hub profile list --names 2>/dev/null)" -- "$cur") )
  elif [ "$COMP_CWORD" -eq 2 ]; then
    case "${COMP_WORDS[1]}" in
      profile) COMPREPLY=( $(compgen -W "add remove list show rename set-key unset-key set unset share unshare" -- "$cur") ) ;;
      key) COMPREPLY=( $(compgen -W "add set list rename remove" -- "$cur") ) ;;
      settings) COMPREPLY=( $(compgen -W "mode own diff resolve" -- "$cur") ) ;;
      backup) COMPREPLY=( $(compgen -W "list prune" -- "$cur") ) ;;
      migrate) COMPREPLY=( $(compgen -W "resume rollback" -- "$cur") ) ;;
      default|use|exec|sync) COMPREPLY=( $(compgen -W "$(claude-hub profile list --names 2>/dev/null)" -- "$cur") ) ;;
    esac
  fi
}
complete -o default -F _claude_hub claude-hub
`, cmds)
	case "zsh":
		x.IO.Printf(`#compdef claude-hub
_claude_hub() {
  if (( CURRENT == 2 )); then
    compadd -- %s ${(f)"$(claude-hub profile list --names 2>/dev/null)"}
  elif (( CURRENT == 3 )); then
    case "${words[2]}" in
      profile) compadd -- add remove list show rename set-key unset-key set unset share unshare ;;
      key) compadd -- add set list rename remove ;;
      settings) compadd -- mode own diff resolve ;;
      backup) compadd -- list prune ;;
      migrate) compadd -- resume rollback ;;
      default|use|exec|sync) compadd -- ${(f)"$(claude-hub profile list --names 2>/dev/null)"} ;;
    esac
  fi
}
compdef _claude_hub claude-hub
`, cmds)
	case "fish":
		x.IO.Printf(`complete -c claude-hub -f
complete -c claude-hub -n "__fish_use_subcommand" -a "%s"
complete -c claude-hub -n "__fish_use_subcommand" -a "(claude-hub profile list --names 2>/dev/null)"
complete -c claude-hub -n "__fish_seen_subcommand_from profile" -a "add remove list show rename set-key unset-key set unset share unshare"
complete -c claude-hub -n "__fish_seen_subcommand_from key" -a "add set list rename remove"
complete -c claude-hub -n "__fish_seen_subcommand_from settings" -a "mode own diff resolve"
complete -c claude-hub -n "__fish_seen_subcommand_from backup" -a "list prune"
complete -c claude-hub -n "__fish_seen_subcommand_from migrate" -a "resume rollback"
`, cmds)
	default:
		return usage("usage: claude-hub completion zsh|bash|fish")
	}
	return nil
}
