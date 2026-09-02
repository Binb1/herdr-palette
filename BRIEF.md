# herdr-palette — project brief

A beautiful command palette for [Herdr](https://herdr.dev), the terminal
multiplexer for AI coding agents. Go + Bubble Tea, rendered in a Herdr
popup pane. The existing options (JanTvrdik/herdr-command-palette = raw
fzf; herdr-plus Quick Actions; herdr-navigator) are functional but ugly —
this one should feel like VS Code's Cmd+Shift+P living in the terminal.

Repo: `Binb1/herdr-palette` (public, MIT). Plugin id: `binb1.palette`.
The name deliberately avoids clashing with `herdr-command-palette`.

## What it does (MVP)

One keybind opens a centered, rounded, themed popup with a fuzzy-filterable
list in three groups:

1. **Jump** — Herdr workspaces and agents, with live status dots and
   labels ("2. awesome-tips · Ghostty config merge"). Enter focuses the
   workspace.
2. **Actions** — every action from every installed Herdr plugin
   (`herdr plugin action list`), invoked on Enter.
3. **Herdr** — built-in verbs: new workspace, rename workspace, detach,
   reload config, toggle sidebar.

Keyboard: type to filter (fuzzy, with match highlighting), ↑↓ navigate,
Enter runs, Esc closes. Mouse: click to run, scroll. Selection starts on
the first result.

## Verified platform facts (Herdr 0.8.2 — all tested in a prior session)

**Plugin model** — a plugin is a directory with `herdr-plugin.toml`
(required: `id`, `name`, `version`, `min_herdr_version`; declares actions,
event hooks, startup hooks, and panes) plus any executable — language-
agnostic. Pane placements include `popup` (session-modal overlay): whatever
the executable draws in that terminal is the UI, full TUI freedom. Herdr
passes `HERDR_BIN_PATH` for CLI callbacks. Authoring docs:
`https://raw.githubusercontent.com/herdrdev/herdr/v0.8.2/docs/next/website/src/content/docs/plugins.mdx`
(bump the version tag as needed); general docs https://herdr.dev/docs/ and
https://herdr.dev/llms.txt.

**Dev loop** — `herdr plugin link <path>` (local dev, no install),
`herdr plugin list`, `herdr plugin action invoke <id.action>`,
`herdr plugin log list --plugin <id>`, `herdr plugin pane <open|focus|close>`.
Reload config after keybinding changes: `herdr server reload-config`.

**Data** — `herdr api snapshot` returns `.result.snapshot` with:
- `workspaces[]`: `workspace_id`, `number` (positional), `label`,
  `agent_status` (rollup), `focused`, `tokens{dir,name,path,panes}`
- `agents[]`: `agent` ("claude"), `agent_status`, `cwd`, `pane_id`,
  `tab_id`, `workspace_id`, `terminal_title_stripped`, `focused`
- `tabs`, `panes`, `layouts`, `focused_workspace_id` / `_tab_id` / `_pane_id`
- Status enum: `idle | working | blocked | done | unknown`

`herdr api schema --json` documents the full socket protocol (v20),
including `subscription_event` — push updates exist if polling ever feels
slow. Verbs: `herdr workspace focus|create|rename|close`, `herdr pane
focus|zoom`, `herdr session list`.

**Marketplace** — auto-indexed (every 30 min) from GitHub repos with the
`herdr-plugin` topic + a valid `herdr-plugin.toml` on the default branch.
Unreviewed. Cards show repo name/description/stars + manifest name/version.
Installs: `herdr plugin install owner/repo [--ref TAG]`. Don't add the
topic until the plugin is presentable.

## Design

- **Stack**: Go, Bubble Tea, Lipgloss. Single static binary, zero runtime
  deps. Prebuilt binaries via GitHub Releases (goreleaser) later so users
  skip the toolchain (herdr-reviewr sets this precedent).
- **Look**: centered rounded-border panel over a dimmed/transparent
  backdrop, section headers, subtle prompt line, fuzzy-match character
  highlighting, no scrollbar chrome. Density over decoration.
- **Theme**: Catppuccin Mocha (dark) / Latte (light) palettes, ideally
  auto-picked from terminal background. Status dot colors (keep consistent
  with Robin's SwiftBar plugin): working `#E8A33D`, blocked `#E35D6A`,
  done `#5BB974`, idle `#98989D`. Robin's terminal themes are custom
  Catppuccin variants (dark bg `#151517`, light bg `#E0E0E3`, orange
  accent `#F8BC82`) — see `Binb1/awesome-tips` → `ghostty/themes/`.

## Suggested layout

```
herdr-plugin.toml    # manifest at repo root (marketplace scans it)
main.go              # or cmd/herdr-palette/main.go if it grows
go.mod
README.md            # with a VHS-recorded GIF once it looks good
LICENSE              # MIT
```

Manifest sketch (verify field names against plugins.mdx before writing):
id `binb1.palette`, an `open` action, popup pane entry point, platforms
macos+linux, `min_herdr_version = "0.8.0"`, a build command (`go build`).

## User keybinding (documented in README)

```toml
[[keys.command]]
key = "prefix+f"     # NOT prefix+p — that's herdr's previous_tab default
type = "plugin_action"
command = "binb1.palette.open"
description = "Command palette"
```

Robin's Herdr config lives at `~/.config/herdr/config.toml`, mirrored in
`Binb1/awesome-tips` → `herdr/config.toml` (keep both in sync; prefix is
Ctrl+B). A Ghostty-side `Cmd+K → text:\x02f` chord can come later
(Ghostty config also mirrored in awesome-tips).

## Milestones

1. **Skeleton**: manifest + hello-world Bubble Tea popup opening via
   `herdr plugin link` and an invoked action. Proves the pane contract.
2. **MVP**: snapshot-driven Jump section with fuzzy filter and focus-on-
   Enter. Already useful daily.
3. **Actions + Herdr sections**, mouse support, match highlighting.
4. **Polish**: theming, light/dark, empty states, sub-50ms open feel
   (cache the snapshot between opens if needed).
5. **Ship**: README + GIF, goreleaser binaries, tag v0.1.0, add the
   `herdr-plugin` topic, one-line pointer from awesome-tips' herdr README.

Later ideas: `@blocked` / `!claude` filter syntax, tmux-style jump-back,
herdr-plus Quick Actions passthrough, reading the active Ghostty theme.

## Working notes

- Test from inside a Herdr session (popups are session-modal); Robin runs
  Herdr inside Ghostty on macOS (Tahoe). `HERDR_ENV=1` marks Herdr panes.
- `herdr config check` validates config.toml; unknown keys are warnings.
- Robin's taste, learned the hard way: no raw fzf aesthetics, no emoji
  soup in UI chrome (small colored dots > 🟠), but the sheep 🐑 is beloved
  branding — a tasteful `🐑` somewhere in the palette footer would land.
