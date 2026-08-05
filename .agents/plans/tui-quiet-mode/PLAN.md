---
name: tui-quiet-mode
description: Make the TUI hide reasoning blocks and tool output by default, with a runtime verbose toggle (Ctrl+G / :verbose) and an optional tui.verbose config default.
status: draft
---

# TUI Quiet Mode — Verbose Toggle for Reasoning & Tool Output

## Goal

Change the default TUI behavior so reasoning text (live thinking +
stored reasoning sections) and tool result blocks are **not rendered at
all** unless verbose mode is on. A keybinding, a colon command, and an
optional config default toggle verbose mode, which restores today's
rendering exactly.

## Current behavior (what changes)

`renderMessages()` (`internal/tui/render.go:335-432`) always renders:

| Block | Location | Today | After |
|---|---|---|---|
| Stored reasoning (`ExpandableSection`) | render.go:353-358 | collapsed "▶ Reasoning…" line | hidden unless verbose |
| Live thinking (`thinkContent`) | render.go:394-415 | spinner/live text | hidden unless verbose |
| Tool result (`ToolMessage`) | render.go:374-384 | header line, box collapsed when done | hidden unless verbose |
| "Thinking…" spinner line | render.go:424-428 | shown | **stays** (activity signal) |
| Sub-agent brackets | render.go:368-372 | one-liner | **stays** (decision below) |
| Streaming assistant text | render.go:417-422 | shown | **stays** |

With tool blocks hidden, the spinner line, the info-bar active prompt, and
sub-agent brackets remain the activity signals — the UI is never silent
about work in progress.

### Scope decisions

- **Fully hidden, not collapsed**: default mode renders nothing for these
  blocks (no "▶" toggle lines). The fully-hidden version is simpler than a
  third collapse tier and matches the request.
- **Sub-agent bracket lines stay visible** — they are compact lifecycle
  markers, not verbose output. Easy to gate later if wanted.
- **Ctrl+T (toggle reasoning expansion) stays independent**: it flips the
  `reasoningExpanded` map as today. When verbose is off there is nothing to
  expand; turning verbose on reveals blocks in their current expansion state.
- **tui2 is out of scope** — this plan covers `internal/tui` (bubbletea) only.

## Design

### 1. Model state and API (`internal/tui/tui.go`)

```go
// Model field (UI misc section, near showBanner)
verbose bool

// Config field
Verbose bool  // initial verbose state (default false = quiet)

// Public method
func (m *Model) ToggleVerbose() {
    m.verbose = !m.verbose
    if m.verbose { m.SetEphemeral("Verbose on.") } else { m.SetEphemeral("Verbose off.") }
    m.refreshViewport()
}
```

`New(cfg)` sets `verbose: cfg.Verbose`.

### 2. Render gating (`internal/tui/render.go`)

Three `if m.verbose` gates in `renderMessages()`:

1. Stored reasoning (wrap lines 354-359) — skipping it also skips the
   `reasoningZones` append, so the mouse handlers (`tui.go:1100-1106`)
   and hover loops (`tui.go:1010-1017`) see no stale zone IDs.
2. Tool messages case (lines 374-384) — same effect for `toolZones`.
3. Live thinking block (lines 394-415) — includes the `reasoning-live`
   zone registration.

The zone slices are reset at the top of each render (`render.go:338-339`),
so toggling off never leaves stale zones behind.

### 3. Toggle inputs

- **Keybinding** — `keymap.go`: new binding `Verbose: ctrl+g`
  (chosen deliberately: `ctrl+v` is terminal paste on Windows, `ctrl+b`
  is the tmux prefix, `ctrl+t` is taken). Register in the help overlay
  "Actions" group (`palette_component.go:245`).
- **Dispatch** — `handleNormalKey` (`tui.go:1230`): `case key.Matches(msg,
  keys.Verbose): m.ToggleVerbose(); return nil`. Place next to the
  existing `keys.Reasoning` case.
- **Command** — `defaultCommands` entry `{Name: ":verbose", Description:
  "Toggle reasoning and tool output"}` and a case in `executeCommand`
  (`tui.go:541`) calling `m.ToggleVerbose()`.

### 4. Config default (optional but included)

`internal/config/load.go` has no TUI section — add one, matching existing
yaml-tag style (cf. `OtelConfig`):

```go
// TUIConfig holds terminal UI preferences.
type TUIConfig struct {
    Verbose bool `yaml:"verbose"` // show reasoning + tool output by default
}

// in Config struct:
TUI TUIConfig `yaml:"tui"`
```

Wire in `cmd/yaah/tui.go:126`: `Verbose: cfg.TUI.Verbose` in the
`tui.Config{...}` literal. Default stays `false` (quiet) — no
`defaultConfig()` change needed. Other commands (serve, web, acp, tui2)
don't construct a bubbletea Model and need no change.

## Steps (one commit each)

### Step 1: Model + render gating

`verbose` field, `Config.Verbose`, `New()` wiring, `ToggleVerbose()`, and
the three `renderMessages()` gates. Build + `go test ./internal/tui/...`
(failures expected — fixed in Step 4).

Commit: `feat(tui): add quiet mode, hide reasoning and tool output by default`

### Step 2: Inputs

`keys.Verbose` binding, `handleNormalKey` case, `:verbose` command,
help-overlay entry.

Commit: `feat(tui): wire verbose toggle to ctrl+g and :verbose`

### Step 3: Config

`TUIConfig` in `internal/config/load.go`, `Config.TUI` field,
`cmd/yaah/tui.go` wiring.

Commit: `feat(config): add tui.verbose setting`

### Step 4: Test migration + new tests

Existing tests asserting reasoning/tool output through `renderMessages()`
must set `verbose: true` on their `Model` literals. Known sites in
`tui_test.go`:

- `TestCTRLRTogglesReasoning` (1033)
- `TestRenderReasoningCollapsed_MessageLevel` (1121)
- `TestRenderReasoningExpanded_MessageLevel` (1144)
- `TestRenderReasoningPlainTextNotMarkdown` (1167)
- `TestRenderReasoningMarkdownPreservedOnRaw` (1184)
- `TestRenderReasoningActiveThinking` (1197)
- `TestRenderReasoningActiveThinkingNoContent` (1217)
- `TestReasoningZonesPopulated_MessageLevel` (1236)
- `TestReasoningZonesPopulated_ModelLevel` (1252)
- `TestReasoningZonesMultipleMessages` (1266)
- `TestReasoningZonesClearedEachRender` (1300)
- any model-level tool-rendering tests encountered during the run
  (`component_test.go` tests components directly and needs no changes)

New tests (in `tui_test.go`):

1. `TestQuietModeHidesReasoningAndTools` — `Model{verbose: false}` with an
   assistant+reasoning message and a tool message: `renderMessages()`
   output contains no `Reasoning` toggle and no tool header, and both
   zone slices are empty; assistant content still present.
2. `TestVerboseShowsBlocks` — same setup, `verbose: true`: blocks render
   (mirrors today's assertions).
3. `TestToggleVerbose` — `ToggleVerbose()` flips the flag, refreshes, and
   sets the ephemeral message.
4. `TestQuietModeHidesLiveThinking` — `thinkContent` set, `streaming:
   true`: hidden when quiet, shown when verbose.

Commit: `test(tui): migrate render tests to verbose mode, cover quiet mode`

### Step 5: Quality gates + docs

```powershell
gofmt -l internal/tui internal/config cmd/yaah   # empty
go vet ./internal/tui/... ./internal/config/... ./cmd/yaah/...
staticcheck ./internal/tui/... ./internal/config/... ./cmd/yaah/...
go test ./... -count=1
```

Manual smoke: run `yaah tui`, submit a prompt that triggers tools and
reasoning — chat stays clean with only spinner/brackets; `Ctrl+G` reveals
all blocks mid-run and hides them again; `:verbose` works; setting
`tui: {verbose: true}` in `~/.yaah/config.yaml` starts in verbose mode.

Docs: add `tui.verbose` to `docs/configuration.md`; note quiet mode in
the TUI section of `docs/features.md`; add the `Ctrl+G` row wherever
`docs/tui-components.md` or help text enumerates bindings.

## Coordination with other plans

`break-up-tui-god-file` moves `tui.go` into model/input/view files. This
plan is independent of that split and safe in either order:

- **This plan first**: the split plan's inventory table gains one field
  (`verbose` → model.go), one method (`ToggleVerbose` → model.go), the
  `:verbose` case (→ input.go), and the keybinding case (→ input.go).
- **Split first**: identical edits, applied to `model.go`/`input.go`
  instead of `tui.go`.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Missed test literal needs `verbose: true` | Certain (known) | Step 4 enumerates sites; compiler + test run catch the rest |
| Users lose discoverability of tool progress | Medium | Spinner + info bar + sub-agent brackets remain; `:help` and help overlay document Ctrl+G; ephemeral feedback on toggle |
| Ctrl+G collides with a terminal binding | Low | BEL key is unused by Windows Terminal/iTerm/most emulators in raw mode; escape hatch is `:verbose` |
| Config section name conflicts with future UI work | Low | `tui:` mirrors the command name; easy to extend (`theme:` already flows through env vars) |

## Out of scope

- Gating sub-agent brackets, compaction, or escalation messages.
- Per-block visibility granularity (e.g. hide tools but show reasoning).
- tui2 (tview prototype) equivalent.
