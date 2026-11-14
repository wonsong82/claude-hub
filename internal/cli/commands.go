package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (x *Ctx) runCommand(cmd string, args []string) error {
	switch cmd {
	case "init":
		return x.cmdInit(args)
	case "profile":
		return x.cmdProfile(args)
	case "key":
		return x.cmdKey(args)
	case "default":
		return x.cmdDefault(args)
	case "use":
		return x.cmdUse(args)
	case "which":
		return x.cmdWhich(args)
	case "settings":
		return x.cmdSettings(args)
	case "sync":
		return x.cmdSync(args)
	case "exec":
		return x.cmdExec(args)
	case "install-shim":
		return x.cmdInstallShim(args)
	case "uninstall-shim":
		return x.cmdUninstallShim(args)
	case "statusline":
		return x.cmdStatusline(args)
	case "doctor":
		return x.cmdDoctor(args)
	case "backup":
		return x.cmdBackup(args)
	case "migrate":
		return x.cmdMigrate(args)
	case "self-update":
		return x.cmdSelfUpdate(args)
	case "uninstall":
		return x.cmdUninstall(args)
	case "completion":
		return x.cmdCompletion(args)
	case "version":
		return x.cmdVersion(args)
	case "help":
		return x.cmdHelp(args)
	}
	return usage("unknown command %s", cmd)
}

func (x *Ctx) printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	x.IO.Printf("%s\n", b)
	return nil
}

var helpTopics = map[string]string{
	"": `claude-hub: an unofficial profile launcher for Claude Code.

Usage:
  claude-hub [<profile>] [--] [claude args...]   start claude under a profile
  claude-hub <command> [args...]

Setup:        init
Profiles:     profile add|remove|list|show|rename|set-key|unset-key|set|unset|share|unshare
Selection:    default, use, which
Keys:         key add|set|list|rename|remove
Settings:     settings mode|own|diff|resolve, sync
Running:      exec <profile> -- <command>
Shim:         install-shim, uninstall-shim
Other:        statusline, doctor, backup, migrate, self-update, uninstall,
              completion, version, help [command]

The first argument is read as: a claude-hub command; "--" (pass the rest to claude);
a profile name; anything else goes to claude untouched with the selected profile.
Profile selection: argument, CLAUDE_HUB_SELECT, .claude-profile (here or a parent),
[[directory]] rule, hub.default_profile, else plain claude.

Exit codes: 0 ok, 1 failed, 64 usage, 78 config (unknown profile, unreadable key),
126 recursion guard, 127 real claude not found.

Unofficial. Not affiliated with, endorsed by or sponsored by Anthropic.
`,
	"profile": `claude-hub profile add <name> [--dir PATH] [--key KEY | --new-key KEY [--note TEXT] | --login]
                       [--icon X] [--share ITEMS] [--own ITEMS] [--settings MODE] [--adopt]
claude-hub profile remove <name> [--archive | --purge] [--yes]
claude-hub profile list [--json]
claude-hub profile show <name> [--json]
claude-hub profile rename <old> <new> [--move-dir]
claude-hub profile set-key <name> <key>
claude-hub profile set-key <name> --new <key> [--note TEXT]
claude-hub profile unset-key <name>
claude-hub profile set <name> icon <emoji|#rrggbb>
claude-hub profile set <name> env NAME=value [--not-a-secret]
claude-hub profile unset <name> env NAME
claude-hub profile share <name> <item>... [--on-conflict abort|keep-base|keep-profile|keep-both] [--dry-run] [--yes] [--force]
claude-hub profile unshare <name> <item>... [--copy | --empty] [--dry-run] [--yes] [--force]

Authentication: --key uses an existing key, --new-key stores a new one (token from a
no-echo prompt, or stdin), --login uses Claude's own /login in the profile's folder.
Without a terminal one of the three is required.

Items: sessions, projects, skills, agents, commands, hooks, plugins, keybindings, memory,
settings, or any extra top-level name; "all" and "none" in --share/--own.
`,
	"key": `claude-hub key add <name> [--note TEXT]     token from a no-echo prompt, or stdin
claude-hub key set <name>                   replace the token (rotation)
claude-hub key list [--json]
claude-hub key rename <old> <new>
claude-hub key remove <name> [--yes]        --yes also switches its profiles to login
No command takes a token as an argument, and none prints one.
`,
	"settings": `claude-hub settings mode <profile> shared|own|layered [--on-conflict abort|keep-base|keep-profile] [--yes]
claude-hub settings own <profile> add|remove <path>... [--discard | --promote]
claude-hub settings diff <profile> [--json]
claude-hub settings resolve <profile> [--all promote|own|discard]
claude-hub sync [<profile>...]
`,
	"use": `claude-hub default [<name> | --none]
claude-hub use <profile> [--dir PATH]          write .claude-profile
claude-hub use <profile> --rule [--dir PATH]   add a [[directory]] rule instead
claude-hub use --clear [--dir PATH] [--rule]
claude-hub which [--json]
`,
	"shim": `claude-hub install-shim [--shell zsh,bash,fish] [--dry-run]
claude-hub uninstall-shim [--dry-run]
The shim is a link named claude in ~/.local/share/claude-hub/shim, put first on PATH by a
marked block at the end of your shell rc files. Under the name claude, no hub command is
recognised: claude doctor, claude update and claude mcp reach Claude Code unchanged.
`,
	"statusline": `claude-hub statusline                       render from Claude's JSON on stdin
claude-hub statusline install [--force]     set statusLine in the base settings.json
claude-hub statusline uninstall
`,
	"backup": `claude-hub backup list [--json]
claude-hub backup prune --older-than <days> [--yes]
claude-hub migrate resume | rollback
`,
}

func init() {
	helpTopics["default"] = helpTopics["use"]
	helpTopics["which"] = helpTopics["use"]
	helpTopics["install-shim"] = helpTopics["shim"]
	helpTopics["uninstall-shim"] = helpTopics["shim"]
	helpTopics["sync"] = helpTopics["settings"]
	helpTopics["migrate"] = helpTopics["backup"]
	helpTopics["exec"] = "claude-hub exec <profile> -- <command> [args...]\nRun any command with the profile's environment.\n"
	helpTopics["doctor"] = "claude-hub doctor [--fix [--yes]] [--json]\nRead-only unless --fix; merging a replaced link back needs a terminal or --yes. Exit 1 on any failure.\n"
	helpTopics["init"] = "claude-hub init [--yes]\nCreate config.toml; offers to register ~/.claude as the base profile 'main' (Claude's own login).\n"
	helpTopics["self-update"] = "claude-hub self-update [--version vX.Y.Z]\nCurl installs only; Homebrew installs use brew upgrade.\n"
	helpTopics["uninstall"] = "claude-hub uninstall [--purge] [--yes]\nRemoves the shim and the binary. Profile folders are never removed.\n"
	helpTopics["completion"] = "claude-hub completion zsh|bash|fish\n"
	helpTopics["version"] = "claude-hub version\n"
}

func (x *Ctx) cmdHelp(args []string) error {
	topic := ""
	if len(args) > 0 {
		topic = args[0]
	}
	t, ok := helpTopics[topic]
	if !ok {
		return usage("no help for %q; try \"claude-hub help\"", topic)
	}
	x.IO.Printf("%s", t)
	if topic == "" {
		x.IO.Printf("\nMore: claude-hub help %s\n", strings.Join([]string{"profile", "key", "settings", "use", "shim", "statusline", "backup"}, "|"))
	}
	return nil
}

func need(o *opts, n int, syntax string) error {
	if len(o.pos) != n {
		return usage("usage: claude-hub %s", syntax)
	}
	return nil
}

func needAtLeast(o *opts, n int, syntax string) error {
	if len(o.pos) < n {
		return usage("usage: claude-hub %s", syntax)
	}
	return nil
}

var _ = fmt.Sprintf
