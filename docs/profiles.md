# Profiles

A profile is a name for a Claude Code config folder plus how to start Claude in it:

```toml
[profiles.work]
dir  = "~/.claude-work"   # exported as CLAUDE_CONFIG_DIR; without dir, CLAUDE_CONFIG_DIR is unset
key  = "key-work"         # optional; without it the profile uses Claude's own /login in its folder
icon = "#eab308"          # an emoji, or #rrggbb drawn as a colour block in the status line

[profiles.work.env]       # extra non-secret environment
CLAUDE_CODE_USE_BEDROCK = "1"
```

Names use `a-z`, `0-9`, `-` and `_` (at most 32 characters) and may not be a claude-hub command or a Claude Code
subcommand (`mcp`, `doctor`, `update`, ...), so `claude-hub mcp list` always reaches Claude.

The **base** profile is the one whose folder is `hub.base_dir` (default `~/.claude`); it owns everything and is what
other profiles share from. `claude-hub init` registers the existing `~/.claude` as the profile `main` *without* a
`dir`, so it keeps exactly the state plain `claude` uses (setting `CLAUDE_CONFIG_DIR=~/.claude` explicitly is a
different state for Claude Code).

## Signing in

| Profile has | At start | Credentials live in |
| --- | --- | --- |
| `key = "<name>"` | the token is read from the secret store and exported as `CLAUDE_CODE_OAUTH_TOKEN` | the secret store |
| no key | `CLAUDE_CODE_OAUTH_TOKEN` is unset, so Claude uses its own login for that folder | wherever Claude Code keeps them for that folder |

`ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` are unset for every selected profile, so a variable in your shell
cannot switch the account or the billing.

Claude Code keys its stored `/login` by the config folder path (see [VERIFY.md](../VERIFY.md)): each login profile
logs in once per machine, and moving or renaming its folder (`profile rename --move-dir`) may need a new `/login`.
claude-hub never reads, copies or shares those credentials.

```sh
claude-hub key add key-work --note "work account"   # paste the token; or: pbpaste | claude-hub key add key-work
claude-hub key list                                  # name, note, backend, fingerprint, profiles using it
claude-hub key set key-work                          # rotate
claude-hub profile set-key work key-work             # use a key
claude-hub profile unset-key work                    # back to Claude's own login
```

Secret backends (`[secrets] backend`): `keychain` (macOS login keychain, service `claude-hub`), `secret-tool` (Linux
Secret Service), `file` (`~/.local/share/claude-hub/secrets/<name>`, mode 0600). `auto` picks keychain on macOS,
secret-tool on Linux when a Secret Service answers, else file; the choice is written to the config at the first
`key add`. Choose `file` on a Mac reached only over SSH, where the login keychain may be locked.

## Selection

First match wins: the first argument; `CLAUDE_HUB_SELECT`; `.claude-profile` in the current folder or a parent, up to
your home folder; the longest `[[directory]]` rule; `hub.default_profile`; else plain `claude`.

```sh
claude-hub use work                 # writes ./.claude-profile (add it to .gitignore)
claude-hub use work --rule          # adds a [[directory]] rule instead, nothing in the repo
claude-hub use --clear [--rule]
claude-hub default work | --none
claude-hub which [--json]
```

Inside a session started by claude-hub (`CLAUDE_HUB_PROFILE` is set), a nested `claude` keeps the session's profile
untouched; if it arrives without the token (Claude Code does not pass it to hooks), the session profile's key is put
back. Only an explicit profile name switches.

## The config file

`~/.config/claude-hub/config.toml` holds no secrets and can be synced or committed. claude-hub owns its layout: every
write re-serialises it (keeping `config.toml.bak`), so comments do not survive a CLI write. Hand edits are fine and
are validated at the next run; an unknown key is an error naming it.

```toml
schema = 1

[hub]
default_profile = "personal"
base_dir        = "~/.claude"
claude_bin      = ""        # "" = find claude on PATH
announce        = "tty"     # the "profile X (from Y)" line on stderr: tty, always, never

[secrets]
backend = "auto"

[keys.key-personal]
note = "personal account"

[[directory]]
path    = "~/workspace/client"
profile = "work"

[statusline]
segments = ["profile", "cpu", "ram", "disks", "append"]
disks    = [{ label = "Storage", path = "/System/Volumes/Data" }]
append   = [{ command = "~/bin/my-status.sh", timeout_ms = 1000 }]

[tmp]
dir          = ""   # exported as CLAUDE_CODE_TMPDIR when set
max_age_days = 0    # > 0: files there older than this are deleted in the background at start
```
