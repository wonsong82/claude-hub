# claude-hub

**Unofficial.** Not affiliated with, endorsed by or sponsored by Anthropic. Claude and Claude Code are trademarks of
Anthropic, PBC. See [TRADEMARKS.md](TRADEMARKS.md).

claude-hub is an unofficial profile launcher for the [Claude Code](https://docs.claude.com/en/docs/claude-code) CLI.
It keeps one person's separate contexts (personal, work, a client) side by side as **profiles**. Each profile is a
Claude Code config folder (`CLAUDE_CONFIG_DIR`) with an icon, optional extra environment, and a way to sign in:
Claude's own `/login` in that folder, or a long-lived token from `claude setup-token` kept in the OS secret store.
Skills, agents, hooks, plugins, history and settings are shared between profiles the way you choose, and nothing is
lost when you change your mind.

claude-hub is a single static binary for macOS and Linux (arm64 and x86_64). It acts before Claude starts and adds
nothing to a session but environment variables (and an optional status line). It never installs, updates or patches
Claude Code. Using several accounts, and moving data between them, is subject to Anthropic's terms and your
employer's policies; checking that is up to you.

## Install

Homebrew:

```sh
brew install wonsong82/tap/claude-hub
```

Or the installer script (no sudo; installs `~/.local/bin/claude-hub` after checking its SHA-256):

```sh
curl -fsSL https://raw.githubusercontent.com/wonsong82/claude-hub/main/install.sh | sh
```

`CLAUDE_HUB_VERSION=vX.Y.Z` pins a version and `CLAUDE_HUB_INSTALL_DIR` picks another folder. Upgrade with
`brew upgrade claude-hub`, or `claude-hub self-update` for a script install.

## Quick start

```sh
claude-hub init                      # creates the config; offers ~/.claude as the profile "main" (your current login)
claude-hub profile add work --login  # a second profile in ~/.claude-work, signed in with Claude's own /login
claude-hub work                      # start Claude under "work"; run /login once inside it
claude-hub                           # start Claude under the default profile
claude-hub use work                  # this folder (and its subfolders) now start under "work"
```

Everything after the profile goes to Claude unchanged: `claude-hub work -p "summarise this"`,
`claude-hub work mcp list`. A first word that is not a profile or a claude-hub command goes to Claude too:
`claude-hub "fix the bug"`. Use `--` to hand Claude a word claude-hub would take: `claude-hub -- doctor`.

## Profiles

```sh
claude-hub profile add <name> [--dir PATH] [--key KEY | --new-key KEY [--note TEXT] | --login]
                              [--icon X] [--share ITEMS] [--own ITEMS] [--settings MODE] [--adopt]
claude-hub profile list | show <name>
claude-hub profile rename <old> <new> [--move-dir]
claude-hub profile set <name> icon 🟦 | env NAME=value
claude-hub profile remove <name> [--archive | --purge]
```

**Signing in.** A profile either names a key or uses Claude's own login:

| Choice | Flag | What happens |
| --- | --- | --- |
| an existing key | `--key key-work` | the token is read from the secret store at every start and passed as `CLAUDE_CODE_OAUTH_TOKEN` |
| a new key | `--new-key key-work` | paste the token from `claude setup-token` at a hidden prompt (or pipe it on stdin); it goes to the secret store |
| Claude's own login | `--login` | no token; run `/login` once in that profile |

In a terminal `profile add` asks; in a script one of the three flags is required (otherwise exit 64). Change it
later with `profile set-key` and `profile unset-key`. Keys are managed with `claude-hub key add|set|list|rename|remove`;
no command takes a token as an argument and none prints one, only a fingerprint (`sha256:1a2b3c4d len=108`).

A key that is configured but cannot be read (missing, keychain locked) stops the start with exit 78. claude-hub never
falls back quietly to another account. Whenever claude-hub selects a profile, it unsets `ANTHROPIC_API_KEY` and
`ANTHROPIC_AUTH_TOKEN`, so the profile's key or login is what Claude uses.

**Which profile starts**, first match wins:
1. the first argument, when it names a profile;
2. `CLAUDE_HUB_SELECT` in the environment;
3. a `.claude-profile` file in the current folder or a parent (up to your home folder), written by `claude-hub use`;
4. a directory rule in the config (`claude-hub use <profile> --rule`), the longest matching path winning;
5. the default profile (`claude-hub default <name>`);
6. otherwise plain `claude`.

An unknown name from 2 to 5 is an error (exit 78) that names where it came from. `claude-hub which` shows the
choice and why. Inside a running session (hooks, subagents, `claude -p` from a tool) nested calls keep the session's
profile.

## Sharing

**Everything is shared with the base folder by default** (`~/.claude`): `sessions`, `projects` (with
`file-history`), `skills`, `agents`, `commands`, `hooks`, `plugins`, `keybindings`, `memory` (`CLAUDE.md`, `rules/`),
and `settings` is layered (below). A shared item is a symlink to the base's copy.

Shared `sessions` and `projects` make all sharing profiles **one trust domain**: sessions of one account can see and
message the others, and transcripts written under one account are readable from the others. Keep a profile apart
(for example an employer account with confidentiality rules) with `--own sessions,projects` when you create it, or
later with `claude-hub profile unshare <name> sessions projects --empty`.

```sh
claude-hub profile share <name> skills [--on-conflict abort|keep-base|keep-profile|keep-both] [--dry-run]
claude-hub profile unshare <name> projects --copy|--empty
```

Changing what is shared never deletes anything: whatever leaves its place goes to a dated backup under
`~/.local/share/claude-hub/backups/`, the plan is shown first (`--dry-run` shows only the plan), conflicts stop the
change unless you pick a policy, and an interrupted change can be finished or undone with `claude-hub migrate
resume|rollback`. Some entries are never shared: `.claude.json`, `.credentials.json`, `backups/`, policy and remote
settings, daemon and job state, telemetry. Details: [docs/sharing.md](docs/sharing.md).

## Settings

A secondary profile's `settings.json` is `layered` by default: claude-hub generates it from the base's
`settings.json`, except for the paths the profile owns (`model`, `modelSettings`, `effortLevel`, `theme`), which come
from the profile's own overlay. A `/model` change inside the profile sticks to that profile; a hook, plugin or
`deniedMcpServers` change made once in `~/.claude/settings.json` reaches every layered profile at its next start or
`claude-hub sync`. A change to an inherited path inside a profile is kept and reported as drift until you settle it
with `claude-hub settings resolve <profile>` (promote it to the base, own it, or discard it). The other modes are
`shared` (a link to the base file) and `own` (claude-hub never touches it). Details:
[docs/settings.md](docs/settings.md).

## The `claude` shim (optional)

```sh
claude-hub install-shim      # then open a new terminal
```

This puts a `claude` link to claude-hub first on your `PATH`, so plain `claude` starts under the selected profile and
tools that run `claude` from your shell get it too. Under the name `claude` no claude-hub command is recognised:
`claude doctor`, `claude update` and `claude mcp list` reach Claude Code unchanged, and claude-hub runs the real
`claude` found later on `PATH`, so Claude's own installer and auto-update keep working. Running `install-shim` again
moves its `PATH` block back to the end of your rc files if another installer appended after it;
`claude-hub uninstall-shim` removes it. Details: [docs/shim.md](docs/shim.md).

For a tool that starts `claude` with its own `PATH`, run the tool itself under a profile:
`claude-hub exec work -- some-agent-app`.

## Status line

`claude-hub statusline install` sets Claude's `statusLine` to show the profile (icon and name), CPU, RAM and disks.
Segments, disks and extra commands whose output is appended are set in the `[statusline]` table of the config.

## doctor

`claude-hub doctor` checks the config, the real `claude`, the shim, the secret store (fingerprints only), every
profile's links and settings, your shell environment and any unfinished migration. `--fix` repairs what is safe
(re-appends the shim block, re-creates missing links, prunes stale entries, syncs settings, merges a replaced link
back through the sharing rules).

## Files

| What | Where |
| --- | --- |
| Config (no secrets) | `~/.config/claude-hub/config.toml` (`$XDG_CONFIG_HOME`; `CLAUDE_HUB_CONFIG` overrides) |
| Settings overlays | `~/.config/claude-hub/profiles/<name>/settings.overlay.json` |
| Backups, shim, file-backend secrets | `~/.local/share/claude-hub/` (`$XDG_DATA_HOME`) |
| Run registry, journal, settings snapshots | `~/.local/state/claude-hub/` (`$XDG_STATE_HOME`) |
| Tokens | macOS Keychain (service `claude-hub`), Linux Secret Service, or the file backend |

Exit codes: 0 success; 1 failed or aborted; 64 usage; 78 config error, unknown profile or unreadable key; 126
recursion guard; 127 real `claude` not found. A start otherwise returns Claude's own exit code.

## Uninstall

`claude-hub uninstall` removes the shim and the binary and lists what it leaves (config, secrets, backups).
`--purge` also deletes those and the hub's keys in the secret store. Profile folders are never removed, and shared
links keep working without claude-hub. For Homebrew: `claude-hub uninstall-shim && brew uninstall claude-hub`.

## Development

```sh
go test ./...                    # unit tests and end-to-end tests against a stand-in claude
go build -o bin/claude-hub ./cmd/claude-hub
```

The previous bash launcher (`claude-hub` and `test.sh` at the repository root) stays in the repository for now, for
existing installs built from it; the Go version replaces it.

More: [docs/](docs/README.md) · [SECURITY.md](SECURITY.md) · [CHANGELOG.md](CHANGELOG.md) · [VERIFY.md](VERIFY.md)

## License

MIT. See [LICENSE](LICENSE).
