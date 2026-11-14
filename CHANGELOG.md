# Changelog

## Unreleased

The Go rewrite (v1), replacing the bash launcher.

- Single static binary for macOS and Linux (arm64, x86_64); Homebrew tap and a checksum-verified `curl | sh`
  installer.
- Profiles managed by CLI (`profile add|remove|list|show|rename|set|unset|share|unshare`), with a TOML config that
  holds no secrets.
- Optional long-lived tokens in the OS secret store (`key add|set|list|rename|remove`); profiles without a key use
  Claude's own `/login` in their folder.
- Profile selection by argument, `CLAUDE_HUB_SELECT`, `.claude-profile` in the folder or a parent, directory rules and
  a default. Unknown profiles and unreadable keys are errors (exit 78) instead of a silent fallback.
- `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` are unset for every selected profile; a login profile also clears an
  inherited `CLAUDE_CODE_OAUTH_TOKEN`.
- Configurable sharing with the base folder, with dry runs, backups, conflict policies and a resumable journal.
- Layered `settings.json` with owned paths, drift detection and `settings resolve`.
- Opt-in `claude` shim that keeps Claude Code's installer and auto-update in charge.
- `doctor`, `statusline` with configurable segments, `exec`, `self-update`, `uninstall`, shell completions.
