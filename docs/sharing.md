# Sharing

Every profile other than the base keeps, per item, either its **own** copy or **shares** the base's (a symlink to
the same path in the base folder).

| Item | Paths | Default |
| --- | --- | --- |
| `sessions` | `sessions/` | shared |
| `projects` | `projects/`, `file-history/` | shared |
| `skills`, `agents`, `commands`, `hooks` | the folder of that name | shared |
| `plugins` | `plugins/` | shared |
| `keybindings` | `keybindings.json` | shared |
| `memory` | `CLAUDE.md`, `rules/` | shared |
| `settings` | `settings.json` | layered ([settings.md](settings.md)) |
| `extra` | top-level names you list, for example `themes` | own |

`profile add` writes every item into the config, so a later change of these defaults never changes an existing
profile. Choose at creation with `--share` / `--own` (comma lists, `all`, `none`), or later with `profile share` and
`profile unshare`.

## Trust consequences

- **Shared `sessions`**: `sessions/` holds per-session key files, so the sessions of every sharing profile can see and
  reach each other, across accounts. All profiles that share it are one trust domain.
- **Shared `projects`**: transcripts and project memory written under one account are readable, and resumable, from
  the others. Which account ran a turn is not recorded by claude-hub.
- **Shared `plugins`**: keep `enabledPlugins` and `extraKnownMarketplaces` inherited (doctor warns otherwise).

Keep a profile apart: `claude-hub profile add client --login --own sessions,projects`, or
`claude-hub profile unshare client sessions projects --empty`.

## Never shared

`.claude.json` (account identity, per-project trust), `.credentials.json`, `backups/`, `policy-limits.json*`,
`remote-settings.json*`, `daemon/`, `daemon.lock`, `daemon.log`, `daemon.status.json`, `jobs/`, `state/`,
`mcp-needs-auth-cache.json`, `statsig/`, `telemetry/`. claude-hub refuses to link them (also in `extra` and with
`--adopt`) and `doctor` fails on any that is a link. Prompt history (`history.jsonl`), caches and anything else
claude-hub does not know are left alone: it touches only the items above.

## Changing what is shared

```sh
claude-hub profile share work skills --dry-run
claude-hub profile share work skills --on-conflict keep-base
claude-hub profile unshare work projects --copy
```

own → shared compares each unit (a skill folder, a transcript file) with the base: **add** (only in the profile),
**duplicate** (identical), **conflict** (different). Conflicts stop the change unless a policy is given:

| `--on-conflict` | Result |
| --- | --- |
| `abort` (default without a terminal) | nothing changes; the conflicts are listed; exit 1 |
| `keep-base` | the base's version stays; the profile's is in the backup |
| `keep-profile` | the profile's version replaces the base's for every sharing profile (needs `--yes`); the base's is in the backup |
| `keep-both` | the profile's version is added as `<name>.<profile>` (before the extension for files); `projects` and `extra` only |

`plugins` is replaced, not merged: claude-hub lists the plugins and marketplaces the base lacks so you can reinstall
them. `sessions` holds only stale runtime state when no session runs, so it is replaced.

shared → own: `--copy` gives the profile a copy of the base's current content (the default), `--empty` starts empty.
`sessions` always starts empty; `projects` needs a choice. The base keeps everything.

Rules for every change:
- **Nothing is deleted.** Whatever leaves its place goes to
  `~/.local/share/claude-hub/backups/<UTC time>-<profile>-<item>/` with a `MANIFEST.json`. Only
  `claude-hub backup prune --older-than <days>` deletes backups.
- **No live sessions.** The change is refused while a session of the profile started by claude-hub runs (`--force`
  overrides). Sessions started without claude-hub are not seen.
- **Journaled.** Each step is recorded before the next; after an interruption `claude-hub migrate resume` finishes the
  change and `claude-hub migrate rollback` undoes it.
- Copies use APFS clones or reflinks when available, are verified by hash, and copy symlinks as links.

## Links that stop being links

At each start claude-hub checks that shared items are still links (without reading them) and warns when one is not.
`claude-hub doctor --fix` merges such a copy back through the same rules, so nothing is dropped. Claude Code writes
through a symlinked `settings.json` or `.claude.json`, but follows only one level: a shared file must link directly to
a regular file in the base, and `doctor` warns when the base file is itself a link.
