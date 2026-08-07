# Plan: tui2 hardening + extractable markdown renderer

> Status: DRAFT
> Owner: gbuch
> Depends on: the TUI decision (see `docs/architecture-review.md` §3). The markdown module
> is independently shippable; the tui2 work only matters if tui2 is chosen as the UI.

## Goals

1. Build a **standalone, extractable** terminal-markdown renderer as a separate in-tree
   module, designed so it can be lifted to its own OSS repo with zero import rewrites.
2. Produce a **complete gap inventory** of tui (bubbletea) features that must reach tui2 for
   parity.
3. Establish the **target architecture** for tui2 so it is "very well designed" — not just
   feature-complete — before the porting work begins.

## Non-goals

- Deleting `internal/tui` or dropping glamour from `go.mod` (that happens only after tui2
  reaches parity and is chosen).
- Changing the agent core, event system, or `Session` interface. tui2 consumes those as-is.

---

# Part A — The markdown renderer module (`md/`)

## A.1 Layout & module setup

Separate Go module living inside the repo, resolved locally via a committed `go.work`. The
module path is the **final OSS path from day one** so extraction never rewrites imports.

```
yaah/
├── go.mod          module github.com/buchenberg/yaah
├── go.work         (NEW, committed)
│       go 1.25.8
│       use ( .  ./md )
├── md/                       ◄── NEW module, NOT under internal/
│   ├── go.mod      module github.com/buchenberg/tviewmd   ← final OSS path
│   ├── LICENSE     (MIT OR Apache-2.0, copies yaah's)
│   ├── README.md
│   ├── doc.go      package doc + public API surface
│   ├── ast.go      Block / Segment / Style types
│   ├── parse.go    goldmark AST → []Block
│   ├── parse_test.go
│   ├── render.go   Renderer interface + Options
│   ├── render_tview.go   tview color-tag backend
│   ├── render_tview_test.go
│   ├── chroma.go   code-block syntax highlighting (chroma lexer→terminal256)
│   └── fuzz_test.go
└── internal/tui2/markdown.go   ← shrinks to: return tviewmd.Render(md, opts)
```

**Dependencies of `md/` (the extractability contract — grep-enforce):**
stdlib, `github.com/yuin/goldmark`, `github.com/alecthomas/chroma/v2`,
`github.com/rivo/tview` (backend only). **Zero** `github.com/buchenberg/yaah/...` imports.

> goldmark and chroma are already indirect deps of yaah today (via glamour), so the `md`
> module declaring them direct adds nothing new to the build graph.

## A.2 The `Block` / `Segment` model (the real deliverable)

A curated, renderer-neutral markdown vocabulary — smaller and friendlier than goldmark's
full AST, tuned for terminals.

```go
// Style is renderer-neutral formatting. "" means inherit.
type Style struct {
    FG, BG       string // color name / hex, "" = inherit
    Bold, Italic bool
    Underline, Strikethrough, Dim bool
}

// Segment is one styled inline run.
type Segment struct {
    Text  string
    Style Style
    Link  string // non-empty ⇒ Text is the label, Link is the URL
    Code  bool   // inline-code styling hint
}

// Block is one block-level element. Exactly one field is set.
type Block struct {
    Kind      BlockKind // Heading / Paragraph / List / CodeBlock / Table / BlockQuote / ThematicBreak
    Level     int       // heading level 1-6
    Segments  []Segment // paragraph / heading / quote inline content
    Items     []ListItem
    Code      CodeBlock
    Table     Table
}

type ListItem struct {
    Segments []Segment
    Children []ListItem // nesting
    Ordered  bool
}

type CodeBlock struct{ Lang, Source string }
type Table struct{ Header []Segment; Rows [][]Segment; Align []TextAlign }
```

Public API (stable from day one):

```go
func Parse(md string) ([]Block, error)
func RenderTView(blocks []Block, opts Options) string
func Render(md string, opts Options) string // Parse + RenderTView convenience

type Options struct {
    WordWrap  int    // 0 = let widget wrap (recommended for tview)
    Highlight bool   // chroma code highlighting, default true
    Theme     Theme  // colors per element; default DarkTheme()
    Width     int    // for table column math / hr length; 0 = 80
}
```

## A.3 Parser wiring

`goldmark.New()` with the GFM extensions (table, strikethrough, linkify, task list) via
`goldmark.WithExtensions(...)`. Walk `ast.Document` with `ast.Walk`, mapping each node kind
to a `Block`/`Segment`. Inline formatting (emphasis spans) is flattened into `[]Segment`
runs with `Style` set.

## A.4 Code highlighting

For each `CodeBlock` with a known `Lang`: chroma `lexers.Get(lang)` →
`chroma.Tokenise` → `formatters.Get("terminal256")` → write to a buffer → the resulting ANSI
is converted via `tview.TranslateANSI`. `Options.Highlight=false` emits plain monospace.
Unknown language ⇒ no highlight (never error on a code block).

## A.5 Test bar (the gate for extraction)

- `parse_test.go`: table-driven `Parse(md) → []Block` for every node type (6 heading
  levels, bold/italic/strike combos, inline code, nested lists, ordered/unordered, task
  lists, fenced code w/ and w/o language, blockquote, thematic break, link, autolink,
  GFM table, hard/soft line breaks).
- `render_tview_test.go`: assert representative tag output per block type.
- `fuzz_test.go`: `FuzzParse` — goldmark is robust but the walker can panic on
  pathological trees; fuzz until clean.
- `go vet` + `staticcheck` clean for the module on its own.

## A.6 Extraction (when the test bar is green)

```bash
git mv md ../tviewmd          # or git filter-repo to keep history in a new repo
# drop ./md from go.work (or delete go.work if it was the only extra module)
# publish github.com/buchenberg/tviewmd v0.1.0
# in yaah: go get github.com/buchenberg/tviewmd@v0.1.0
```

`internal/tui2` needs **zero import edits** — same path, now resolved from the published
module.

---

# Part B — tui1 → tui2 feature inventory (gap analysis)

Source of truth for tui1: `internal/tui/keymap.go`, `input.go`, `modes.go`, `model.go`,
`events.go`, `render.go`, `view.go`. Status column reflects current tui2 code.

## B.1 Keybindings

| Binding | tui1 key | tui2 status | Notes |
|---|---|---|---|
| Quit | Ctrl+C | ✅ | |
| Cancel / back | Esc | ✅ | |
| Scroll up/down | ↑/↓, j/k | ✅ | tui2 already has vim j/k |
| Page up/down | PgUp/PgDn | ✅ | |
| Top / bottom | Home/End, g/G | ✅ | tui2 has g/G |
| Help | `?` | ✅ | |
| Command palette | `:` (auto) | ⚠️ partial | tui2 uses Ctrl+P; tui1 auto-detects `:` prefix. Pick one model. |
| **Search** | `/` n N | ❌ | `ActionSearch` declared, never handled. Build search mode. |
| **Copy last response** | Ctrl+Y | ❌ | uses `tea.SetClipboard`; tui2 needs `atotto/clipboard`. |
| Toggle reasoning | Ctrl+T | ⚠️ | tui2 binds Ctrl+T to *tools*; tui1 binds Ctrl+T to reasoning. **Conflict to resolve.** |
| Toggle verbose | Ctrl+G | ❌ | no verbose concept yet. |
| Submit | Enter | ✅ | |
| Panel focus | (n/a) | ✅ (new) | Tab/Shift+Tab — tui2-only nicety, keep. |

## B.2 Commands (`:` palette)

| Command | tui1 | tui2 status | Notes |
|---|---|---|---|
| `:help` | ✅ | ✅ | |
| `:clear` | ✅ | ✅ | |
| `:compact` | onCompact (real compaction) | ❌ **bug** | tui2 maps `CmdCompact → CollapseAll` (collapses blocks, not context). Wire to `OnCompact`→`sess.Compact()`. |
| `:banner` | toggle showBanner | ❌ | |
| `:model` | model picker | ⚠️ | `HandleCommand` opens picker but data wiring (CtrlModelList) is flaky; verify list populates. |
| `:mcp` | renderMCPStatus | ❌ | tui2 InfoPane MCP section is a placeholder. |
| `:login` / `:logout` | onLogin/onLogout | ❌ | declared in command enum, not dispatched. |
| `:stop` | onAbort | ❌ | Esc covers abort; add explicit `:stop`. |
| `:copyview` | strip ANSI → clipboard | ❌ | needs `md/` plain backend + clipboard. |
| `:verbose` | ToggleVerbose | ❌ | |
| `:steer <text>` | onSteer (mid-turn inject) | ❌ | `OnSteer` not wired in `tui2.go`. |
| `:quit` | tea.Quit | ✅ | |

## B.3 Modes

| Mode | tui1 | tui2 status |
|---|---|---|
| Normal | ✅ | ✅ |
| Command (auto on `:`) | ✅ | ⚠️ Ctrl+P modal only |
| Search | ✅ (build matches, n/N, scroll-to-match) | ❌ |
| Question modal | ✅ | ✅ |
| Model picker | ✅ (filter + scroll window) | ⚠️ basic |
| Help overlay | ✅ | ✅ |

## B.4 Rendering & content

| Feature | tui1 | tui2 status |
|---|---|---|
| Markdown (glamour) | ✅ rich (tables/trees/lists) | ⚠️ glamour+TranslateANSI → **replace with `md/`** |
| Expandable reasoning blocks | ✅ bubblezone, clickable | ⚠️ toggle-all only, not per-block click |
| Expandable tool blocks | ✅ clickable | ⚠️ toggle-all only |
| Expandable sub-agent blocks | ✅ clickable | ⚠️ toggle-all only |
| Todo table (inline + pane) | ✅ inline render | ⚠️ pane only |
| Message role styling | ✅ | ✅ (via colors pkg) |
| MCP status rendering | ✅ | ❌ placeholder |
| Context % bar | ✅ | ✅ statusbar |
| Lolcat reasoning header | ✅ | ⚠️ has lolcat/ pkg, partial use |

## B.5 Streaming & chrome

| Feature | tui1 | tui2 status |
|---|---|---|
| Token append w/ debounce | ✅ | ✅ (QueueUpdateDraw) |
| Thinking spinner | ✅ | ✅ |
| Active-prompt display | ✅ | ❌ |
| Follow-up while running | ✅ (Enter → onFollowUp) | ❌ Enter-while-running no-ops |
| Steer mid-turn | ✅ | ❌ |
| Ephemeral status messages | ✅ (SetEphemeral) | ❌ |
| Min terminal size guard | ✅ (60×20) | ❌ |
| Dynamic input/header height | ✅ | ⚠️ header yes; content widths hardcoded |

## B.6 Mouse & clipboard

| Feature | tui1 | tui2 status |
|---|---|---|
| Click to expand blocks | ✅ bubblezone | ❌ (mouse enabled, no regions) |
| Question-option click | ✅ | ❌ |
| Hover → pointer cursor (OSC 22) | ✅ | ❌ |
| Wheel scroll | ✅ | ✅ (tview built-in) |
| Copy to clipboard | ✅ | ❌ (dep present, unused) |

---

# Part C — tui2 target architecture

The goal is not just parity but a cleaner, more maintainable structure than tui1. These
principles must be in place *before* the feature port so features land on a sound base.

## C.1 Layered structure: chrome vs content vs viewmodel

Separate three concerns that tui2 currently muddles in `tui2.go`:

```
agent events ──► ViewModel (single source of truth) ──► Render pass ──► widgets
                        ▲
control msgs ───────────┘
```

- **Chrome** = layout primitives that are real `tview.Primitive`s: header, infobar,
  messages TextView, infopane, todopane, statusbar, input. Owned by the `App` shell. These
  already exist and are mostly fine.
- **Content** = the message-stream blocks (reasoning/tool/subagent/text) rendered *into* the
  messages TextView as tagged strings. These are the `components/*` packages.
- **ViewModel** = a `Conversation` struct that is the **single source of truth** for what
  the messages pane shows. `HandleEvent`/`handleControlMsg` mutate the ViewModel only; a
  `render()` pass reads the ViewModel and rebuilds the TextView string. This replaces the
  current `plainMessages` + `conversationLog` double-bookkeeping (a real drift bug today).

## C.2 Theme as first-class

Replace scattered inline tags (`[red]`, `[#888888]` in `events.go`; `colors.Tag(...)` calls)
with one `Theme` struct (mirroring tui1's `theme.go` / lipgloss styles):

```go
type Theme struct {
    Accent, Dim, User, Assistant, System, Error string
    Tool map[string]string                       // tool name → hex
    ReasoningBg, CodeBg string
    // ...
}
var DarkTheme, LightTheme Theme
func DetectTheme() Theme // respects NO_COLOR, $YAHH_THEME, terminal bg
```

All components take a `*Theme`. Enables `:theme` command and light mode for free.

## C.3 A `Component` contract

Today the `components/*` packages are ad-hoc (some return strings, some are builders). Give
them a uniform shape so they are composable and testable:

```go
// Renderable is anything that can produce tagged content for the message stream.
type Renderable interface {
    Render(ctx RenderCtx) string
}

type RenderCtx struct {
    Width, Height int
    Theme         *Theme
    Expanded      bool
}
```

Content components (`reasoning`, `toolblock`, `subagent`, `messages`) implement `Renderable`.
Chrome components keep their current `Build()` → `tview.Primitive` shape.

## C.4 Width propagation (fix hardcoded layout)

`toolblock.go:154` hardcodes `width := 58`. Thread `RenderCtx.Width` (from the TextView's
inner width, via `t.Messages.GetInnerRect().Dx()`) through every content render. Capture the
width once per `refreshMessages()` and pass it down.

## C.5 Interactive blocks via tview regions

tview `TextView.SetRegions(true)` + `["id"]...[""]` tags give clickable spans *without*
bubblezone. Use region tags for each expandable block so click-to-expand works natively,
matching tui1's per-block interactivity (currently tui2 only has toggle-all).

## C.6 Focused input/action dispatcher

Move the `globalInputCapture` switch out of `tui2.go` into a `dispatch.go` that owns a small
focus-state machine (which panel is focused, which modal is open) and routes `Action`s. Keep
the declarative `keymap.go` (it's already good). Resolves the Ctrl+T reasoning-vs-tools
conflict explicitly in the binding table.

## C.7 Cleanups (mechanical, do during the port)

- Delete `plainMessages []string`; `Conversation` is the sole store.
- Delete the local `max()` in `toolblock.go:209` (Go 1.21+ builtin).
- Centralize tool icon + tool color (currently split between `toolblock.go` `Icon()` and
  `colors/rolecolors.go`).
- `OnAbort` should always hide the thinking indicator, not only when `cancelAgent != nil`.
- Finish `UpdateInfopane(tab,…)` (currently `_ = tab // TODO`).

---

# Part D — Phased execution

Each phase is independently mergeable and leaves the build green.

## Phase 0 — Prerequisites & decisions (no code)
- Confirm the module/OSS path (`github.com/buchenberg/tviewmd` or other).
- Confirm Ctrl+T semantics (reasoning, per tui1 — recommended) and command-palette trigger
  (`:` auto-detect, per tui1 — recommended for muscle memory).
- **Exit gate:** paths and keybinding model decided.

## Phase 1 — Scaffold the `md/` module
- Create `md/go.mod`, repo-root `go.work`, `LICENSE`, `ast.go`, `parse.go`, `render.go`,
  `render_tview.go`.
- Implement node subset: headings, paragraphs, bold/italic/strike, inline code, lists
  (1-2 levels), blockquote, thematic break, links (static). No tables, no chroma yet.
- Wire chroma code highlighting.
- Table tests + fuzz test (A.5). `go test ./md/...` green.
- **Exit gate:** `md/` passes its test bar; CI (workspace mode) builds both modules.

## Phase 2 — tui2 architecture base (no new user features)
- Introduce `Theme` + `DetectTheme()`; route all components through it.
- Introduce `RenderCtx` + `Renderable`; thread width from `GetInnerRect()`.
- Introduce `Conversation` viewmodel; delete `plainMessages`; make `refreshMessages()` the
  single render pass over `Conversation`.
- Move input dispatch to `dispatch.go`; resolve Ctrl+T / `:` decisions.
- Mechanical cleanups (C.7).
- **Exit gate:** tui2 still builds and runs with existing features; no behavior regressions;
  `md/` replaces glamour in `internal/tui2/markdown.go`.

## Phase 3 — Feature parity port (the B-inventory)
Port in priority order, each as its own small PR:
1. **Steer + FollowUp** (wire `OnSteer`/`OnFollowUp` in `tui2.go`; Enter-while-running →
   follow-up). Highest user-visible value.
2. **Fix `:compact`** to call `sess.Compact()`.
3. **Search mode** (`/`, n/N, scroll-to-match) over the messages TextView.
4. **Verbose toggle** + per-block verbose collapse semantics (match tui1).
5. **Click-to-expand** via tview regions.
6. **Clipboard**: copy-last-response (Ctrl+Y) + `:copyview`.
7. **`:login`/`:logout`/`:stop`/`:banner`/`:mcp`** command wiring.
8. **Model picker** data + filter correctness.
9. **Ephemeral messages** + **active-prompt display**.
10. **Min terminal size guard**.
- **Exit gate:** every row in Part B reads ✅; tui2 feature-parity with tui1 confirmed by a
  manual + scripted checklist.

## Phase 4 — Decision & extraction
- Run tui2 as the default `yaah tui` behind a flag (or swap) for a soak period.
- Decide tui vs tui2 (review §3). If tui2 wins:
  - Extract `md/` to its OSS repo (A.6).
  - Delete `internal/tui`, drop glamour + bubbletea/bubbles/lipgloss/bubblezone from
    `go.mod`, retire `tui2.go`-vs-`tui.go` duplication.
- **Exit gate:** single TUI; one terminal-UI dependency cluster; `md/` published.

---

# Risks & open questions

- **Ctrl+T conflict**: tui1 = reasoning, tui2 = tools. Recommend adopting tui1's
  reasoning binding and rebinding tools to something else (e.g. Ctrl+Shift+T / `:tools`).
- **`:compact` semantic bug** (CollapseAll vs real compaction) should be fixed in Phase 3
  item 2 regardless of the larger decision — it's a correctness issue today.
- **Chroma weight**: chroma adds lexer binaries; if binary size matters, gate highlighting
  behind a build tag or lazy-init only the lexers seen. Measure before Phase 4.
- **tview mouse on Windows**: verify region-click works under the Windows tcell backend
  (yaah's primary platform) during Phase 3 item 5.
- **Extraction timing**: `md/` is publishable after Phase 1 regardless of the TUI decision,
  but importing it into yaah only pays off if tui2 is chosen.
