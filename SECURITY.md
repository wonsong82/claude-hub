# Security

## What claude-hub protects, and what it does not

- Tokens (from `claude setup-token`) live in the OS secret store: the macOS login keychain, the Linux Secret Service,
  or, with the `file` backend, `~/.local/share/claude-hub/secrets/<name>` with mode 0600 in a 0700 folder.
  `config.toml` holds key names and notes only, so it can be synced or committed.
- Tokens enter only through a hidden prompt or stdin, and leave only as `CLAUDE_CODE_OAUTH_TOKEN` in the environment
  of the process claude-hub starts. No command takes a token as an argument or prints one; output shows a
  fingerprint (`sha256:<first 8 hex> len=<n>`). The keychain backend sends its write command to `security` on stdin,
  so the token is never in a process argument list. Tokens are never written to the config, state, journal or backup
  files, and never put in error messages; the output of the secret-store tools is not echoed.
- A key that is configured but cannot be read stops the start (exit 78) instead of running under another account.

Limits:
- The secret store protects tokens at rest and keeps them out of files, backups and repositories. It does **not**
  protect against other processes running as the same user: they can query the same store, and they can read the
  environment of a running `claude`, which holds the token.
- The `file` backend relies on file permissions only.
- Profiles that share `sessions` or `projects` form one trust domain (see [docs/sharing.md](docs/sharing.md)).
- Claude Code's own `/login` credentials are Claude Code's business; claude-hub never reads, copies or backs them up.
- Releases have SHA-256 checksums and no signatures in v1: integrity rests on HTTPS to GitHub and `checksums.txt`.

## Reporting a vulnerability

Please report it privately through GitHub: the repository's **Security** tab, **Report a vulnerability**. Do not open
a public issue for a security problem, and never include a real token in a report.
