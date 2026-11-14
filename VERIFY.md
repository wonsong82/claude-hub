# Verification of Claude Code behaviour

claude-hub depends on a few behaviours of Claude Code that are not documented. This file records what was checked,
how, and the result, so that each new Claude Code version can be checked the same way.

Checked against Claude Code 2.1.325 (native install) on macOS 15 (x86_64), by reading the JavaScript embedded in the
binary and by running the real `claude` against throwaway config folders (`CLAUDE_CONFIG_DIR` set to a fresh temporary
folder, `DISABLE_AUTOUPDATER=1` for the probes, no login and no token). Every throwaway folder was removed afterwards
and no keychain item was created.

## Where `/login` stores its credentials (design assumption A2): confirmed

- macOS: the login keychain, generic password, service name
  - `Claude Code-credentials` when `CLAUDE_CONFIG_DIR` is unset;
  - `Claude Code-credentials-<first 8 hex of sha256(CLAUDE_CONFIG_DIR)>` when it is set.

  The hash is taken over the variable **exactly as written** (Unicode NFC only): no realpath, no trailing-slash
  trimming. `~/.claude` set explicitly and `CLAUDE_CONFIG_DIR` unset are different items, and so are `/x/dir` and
  `/x/dir/`. On a test machine the existing service names matched the hashes of the configured folder paths.
- If the keychain write fails, Claude Code falls back to `<config dir>/.credentials.json`. On Linux that file is the
  store (read from shared code; not run on Linux).
- `CLAUDE_SECURESTORAGE_CONFIG_DIR`, when set, replaces the string that is hashed (empty means the unsuffixed item).

Consequences for claude-hub: each login profile logs in once per machine; moving or renaming its folder loses the
keychain login. claude-hub always exports the cleaned absolute path of the profile folder, so the item name is stable
from run to run. A profile without `dir` leaves `CLAUDE_CONFIG_DIR` unset and so uses the unsuffixed item, like plain
`claude`.

## Symlinked config files (A4): refuted as stated, with one limit

The assumption was that Claude Code replaces a symlinked `settings.json` or `.claude.json` with a regular file when it
writes. It does not: the write helpers accept an "allow symlink" option, used for the user `settings.json` and for
`.claude.json`. With it they read the link once, write a temporary file next to the **target** and rename it onto
the target, keeping the target's mode.

| Probe | File written | Link | Result |
| --- | --- | --- | --- |
| `claude mcp add --scope user …` | `.claude.json` | absolute | link kept; target updated |
| `claude plugin marketplace add` + `plugin install --scope user` | `settings.json` | absolute | link kept; target updated |
| `claude plugin disable … --scope user` | `settings.json` | relative | link kept; target updated |
| `claude mcp add` with a two-link chain | `.claude.json` | link → link → file | the **middle link was replaced** by a regular file |

- Only one level of link is followed. A shared file must link directly to a regular file; `claude-hub doctor` warns
  when a base file that profiles link to is itself a link.
- `keybindings.json` is only created when absent (exclusive create) and otherwise read, so a link to it is never
  replaced.
- Not verified: a newer storage backend behind a feature flag, whose write path could not be read.

`claude config` no longer exists in this version; the probes used `mcp` and `plugin` commands instead.

## A `claude` shim ahead on `PATH` (A8): confirmed for `claude doctor`; updater checked by reading the code

- `claude doctor` with a stand-in `claude` (a script that execs the real one) first on `PATH`: exit 0 and the same
  output as running the real binary directly.
- The updater works on `~/.local/bin/claude` itself: it installs `versions/<v>` and swaps the launcher link with a
  temporary link plus rename. If that launcher is not its own link into `versions/`, it leaves it alone and warns
  ("not created by the native installer"). The install type comes from the running executable's own path, and the
  "multiple installations" check looks only at fixed locations. Nothing scans `PATH` for other `claude` executables,
  so a shim in another folder is invisible to it. claude-hub must therefore never replace `~/.local/bin/claude`, and
  it does not.
- After an update, Claude Code's restart looks up `claude` on `PATH` and, when that differs from the running binary,
  relaunches through it (the shim) with the current environment. claude-hub treats that as a nested call and keeps
  the session's profile.
- Not verified: a real auto-update with the shim active (it cannot be forced). It stays on the release checklist.

## The effort setting: `effortLevel`

The settings schema has `effortLevel` with the values `low`, `medium`, `high`, `xhigh`; any other value is dropped.
`max` is accepted only by `--effort`, `CLAUDE_CODE_EFFORT_LEVEL` and the separate `maxEffortLevel` cap. A per-model
form `modelSettings.<model>.effortLevel` also exists. claude-hub's default owned paths (`model`, `modelSettings`,
`effortLevel`, `theme`) cover both forms.

## Release checklist (manual)

On macOS arm64, macOS x86_64 and one Linux, with real Claude Code:
- start a key profile and a login profile; a nested `claude -p` from a hook keeps the profile;
- with the shim active, let a real auto-update happen; confirm the new version runs and `claude doctor` is clean;
- check `strings` of the new Claude Code binary for a `CLAUDE_HUB_` name (a clash with claude-hub's variables).
