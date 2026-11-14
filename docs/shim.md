# The `claude` shim

The shim is mandatory: it is what makes plain `claude`, hooks, subagents and tools that start `claude` from a shell
run under claude-hub's profile instead of reaching Claude Code directly. It is installed

- by the installer script, right after the binary (the install fails if the shim cannot be installed);
- by every claude-hub command, which puts back a missing link or block and moves a block back to the end of its file
  when another installer appended after it. For Homebrew, which may not edit shell files, this is how the shim
  arrives: run `claude-hub init` once. `statusline`, `completion`, `version`, `help` and `doctor` leave it alone
  (doctor reports instead, and `doctor --fix` repairs);
- by `claude-hub install-shim [--shell zsh,bash,fish]`, the same by hand, with a check in a clean shell.

There is no way to keep claude-hub without the shim; `claude-hub uninstall` removes both.

Installing:
1. creates `~/.local/share/claude-hub/shim/claude`, a link to claude-hub's stable path (the Homebrew `bin` link,
   never a Cellar path, or the script-installed file); the folder holds nothing else;
2. appends a marked block to the rc files of every shell found (zsh, bash, fish) that puts that folder first on `PATH`: zsh `~/.zprofile` and `~/.zshrc`,
   bash `~/.bash_profile` (or `~/.profile`) and `~/.bashrc`, fish `~/.config/fish/conf.d/claude-hub.fish`. Each
   file is backed up once as `<file>.claude-hub.bak`;
3. (`install-shim` only) checks in a clean login shell that `claude` is the shim and that `claude --version` reaches
   the real Claude; a `claude --version` that does not run (Claude Code not installed yet) is a note, not a failure.

```sh
# >>> claude-hub shim >>> managed by "claude-hub install-shim"; keep this block last
case "$PATH" in "$HOME/.local/share/claude-hub/shim":*) ;; *) export PATH="$HOME/.local/share/claude-hub/shim:$PATH" ;; esac
# <<< claude-hub shim <<<
```

Other installers sometimes append a line that puts `~/.local/bin` first again. The next claude-hub command moves the
block back to the end; `doctor` reports a missing shim as a failure and a block that is not last as a warning.

## How it stays out of Claude Code's way

- Under the name `claude`, no claude-hub command is recognised: `claude update`, `claude install`, `claude doctor`
  and every other subcommand reach Claude Code unchanged.
- claude-hub runs the first other `claude` on `PATH` (or `~/.local/bin/claude`) **as found**, without resolving the
  link, so after an auto-update repoints `~/.local/bin/claude` the next start runs the new version.
- It never writes `~/.local/bin/claude`, anything under `~/.local/share/claude/`, or any other file Claude's installer
  owns, and never sets `DISABLE_AUTOUPDATER`.
- `claude-hub uninstall` removes the blocks and the shim folder, which restores the previous behaviour.

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
