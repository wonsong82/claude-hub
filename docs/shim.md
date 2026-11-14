# The `claude` shim

`claude-hub install-shim [--shell zsh,bash,fish]`:
1. creates `~/.local/share/claude-hub/shim/claude`, a link to claude-hub's stable path (the Homebrew `bin` link,
   never a Cellar path, or the script-installed file); the folder holds nothing else;
2. appends a marked block to your rc files that puts that folder first on `PATH`: zsh `~/.zprofile` and `~/.zshrc`,
   bash `~/.bash_profile` (or `~/.profile`) and `~/.bashrc`, fish `~/.config/fish/conf.d/claude-hub.fish`. Each
   file is backed up once as `<file>.claude-hub.bak`;
3. checks in a clean login shell that `claude` is the shim and that `claude --version` reaches the real Claude.

```sh
# >>> claude-hub shim >>> managed by "claude-hub install-shim"; keep this block last
case "$PATH" in "$HOME/.local/share/claude-hub/shim":*) ;; *) export PATH="$HOME/.local/share/claude-hub/shim:$PATH" ;; esac
# <<< claude-hub shim <<<
```

Other installers sometimes append a line that puts `~/.local/bin` first again. Running `install-shim` again moves the
block back to the end; `doctor` reports when it is not last, and claude-hub warns (at most once a day) when the shim
is installed but `claude` resolves elsewhere. `uninstall-shim` removes the blocks and the folder.

## How it stays out of Claude Code's way

- Under the name `claude`, no claude-hub command is recognised: `claude update`, `claude install`, `claude doctor`
  and every other subcommand reach Claude Code unchanged.
- claude-hub runs the first other `claude` on `PATH` (or `~/.local/bin/claude`) **as found**, without resolving the
  link, so after an auto-update repoints `~/.local/bin/claude` the next start runs the new version.
- It never writes `~/.local/bin/claude`, anything under `~/.local/share/claude/`, or any other file Claude's installer
  owns, and never sets `DISABLE_AUTOUPDATER`.
- Removing the shim folder restores the previous behaviour.

Claude Code's updater works on `~/.local/bin/claude` directly and does not look for other `claude` executables on
`PATH`; `claude doctor` reports the same with and without the shim ([VERIFY.md](../VERIFY.md)). After an update,
Claude Code's own restart may start `claude` from `PATH`, which is the shim; it inherits the session's environment, so
it keeps the session's profile.

## Tools that start `claude` themselves

| How the tool starts `claude` | Result |
| --- | --- |
| through your login or interactive shell | the shim applies; normal selection |
| directly, with your `PATH` | the same |
| with its own `PATH`, or an absolute path to the real binary | claude-hub is bypassed; run the tool as `claude-hub exec <profile> -- <tool>`, or point its "claude path" setting at the shim |
| from inside a claude-hub session | the session's profile is kept |

Layered settings and shared links are plain files, so they hold even when claude-hub is bypassed, as long as the
tool sets `CLAUDE_CONFIG_DIR`.
