# claude-hub

**Unofficial.** Not affiliated with or endorsed by Anthropic. "Claude" and "Claude Code" are Anthropic's trademarks.

A small launcher for the [Claude Code](https://docs.claude.com/en/docs/claude-code) CLI that keeps several accounts
and setups side by side as **profiles**. Each profile has its own config folder (`CLAUDE_CONFIG_DIR`) and its own
long-lived token (from `claude setup-token`), picked per command or per project folder.

> Status: early. This is the script as used privately so far; a redesigned release (profile management commands,
> tokens in the OS keychain, configurable sharing between profiles, Homebrew and curl installers) is being designed.

## What it does

```
claude-hub [profile] [claude args...]
```

- **Picks a profile**: the first argument when it names a profile; else `./.claude-profile` in the current folder;
  else `DEFAULT_PROFILE`. Anything else as the first word (`mcp list`, a prompt) goes to `claude` untouched.
- **Sets the environment** (`CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_HUB_PROFILE`) and runs the real
  `claude`: the next `claude` on `PATH` that is not claude-hub itself, so Claude's own installer and auto-update keep
  managing it.
- **Nested calls** (hooks, subagents, `claude -p` from a tool, with `CLAUDE_HUB_PROFILE` already set) keep the
  running session's profile. Claude Code does not pass the token on to hooks, so claude-hub puts that profile's key
  back.
- **Status line**: `claude-hub statusline` prints the profile, CPU, RAM and disks. Use it in `settings.json` as
  `"statusLine": {"type": "command", "command": "claude-hub statusline"}`.

## Use it as `claude` (optional)

Link it as `claude` in a folder that comes before the real one on `PATH`. Put the `PATH` line last in both
`~/.zprofile` and `~/.zshrc`, because some installers prepend `~/.local/bin` again:

```sh
mkdir -p ~/.local/hub/bin && ln -s ~/.local/bin/claude-hub ~/.local/hub/bin/claude
echo 'export PATH="$HOME/.local/hub/bin:$PATH"' >> ~/.zprofile
echo 'export PATH="$HOME/.local/hub/bin:$PATH"' >> ~/.zshrc
```

## Configure

Settings sit at the top of the script, between `>>> claude-hub config` and `<<< claude-hub config`:
- `KEY_*` tokens;
- `PROFILES`;
- per profile: `<name>_CLAUDE_CONFIG_DIR`, `<name>_KEY` and `<name>_ICON`;
- the `HUB_*` options.

The tokens live in the file, so keep it private:

```sh
install -m 700 claude-hub ~/.local/bin/claude-hub
```

## Test

```sh
./test.sh [file]
```

`test.sh` runs every check against a stand-in `claude` and never prints a token.

## License

MIT. See [LICENSE](LICENSE).
