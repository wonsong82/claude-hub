# settings.json

| Mode | The profile's `settings.json` | Use when |
| --- | --- | --- |
| `layered` (default) | a regular file generated from the base's, with the profile's owned paths from its overlay | the usual case |
| `shared` | a link to the base file | profiles must be identical; `/model` in one changes all |
| `own` | a regular file claude-hub never touches | full manual control |

```toml
[profiles.work.settings]
mode = "layered"
own  = ["model", "modelSettings", "effortLevel", "theme"]   # dot paths; "env.FOO" is valid
```

## Layered

The generated file is the base `settings.json` with every owned path replaced by the value in
`~/.config/claude-hub/profiles/<name>/settings.overlay.json`, or removed when the overlay has none (so Claude's
default applies). Arrays are replaced whole, never merged. The result is an ordinary file, so it works for any
launcher that sets `CLAUDE_CONFIG_DIR`.

At every start of a layered profile, and on `claude-hub sync`, claude-hub compares the file with the snapshot of what
it last generated:
- a changed **owned** path (for example after `/model`) is copied into the overlay, silently;
- a changed **inherited** path is **drift**: it is kept in the file and in the profile's state, never discarded and
  never written to the base on its own, and claude-hub prints one line:
  `claude-hub: work has 1 local change to inherited settings; run "claude-hub settings resolve work"`.

```sh
claude-hub settings diff work                       # owned and drifted paths, env values masked
claude-hub settings resolve work                    # per path: promote, own, or discard
claude-hub settings resolve work --all promote      # non-interactive
claude-hub settings own work add tui                # own another path (its current value is kept)
claude-hub settings own work remove tui --discard   # inherit it again
claude-hub settings mode work own|shared|layered
```

`promote` writes the profile's value into the base, so every layered profile gets it at its next sync (the old base
file is backed up). `own` adds the path to the owned list. `discard` takes the base's value and saves the profile's
under `backups/`.

A base change reaches a running secondary session only after `claude-hub sync`.

The effort setting is `effortLevel` (`low`, `medium`, `high`, `xhigh`); Claude Code also keeps a per-model form under
`modelSettings`, which is owned by default too.

## Mode changes

| From → to | What happens |
| --- | --- |
| own → layered | every path where the file differs from the base becomes owned, with its value in the overlay; the generated file equals the old one |
| layered → own | the file stays; overlay and snapshot go to the backup |
| shared → own or layered | the link is replaced by a copy of the base file; the overlay starts empty |
| own → shared | identical: link; different: a conflict (`--on-conflict keep-base` or `keep-profile --yes`) |
| layered → shared | with an empty overlay and no drift: link; otherwise `--on-conflict keep-base` moves them to the backup |
