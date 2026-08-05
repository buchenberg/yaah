---
name: break-up-tui-god-file
description: Break the 1891-line internal/tui/tui.go god file into focused per-concern files (model, events, input, modes, view, utils), consolidate styles into theme.go, move component helpers next to their sole consumers, and optionally extract interactive modes into stateful sub-models — mirroring the tui2 god-file breakup (PR #143).
status: draft
---

# Break Up tui.go God File

## Context

`internal/tui/tui.go` is the last god file in yaah: **1891 lines** containing
the Model struct, all bubbletea lifecycle methods, agent-event dispatch,
control-plane dispatch, keyboard/mouse handling, four interactive overlay
modes (search, question, model picker, command palette), top-level view
layout, palette height math, and ~15 text-processing utilities.

```
File                          Lines  Status
────────────────────────────  ─────  ──────────────────────────
internal/tui/tui.go           1891   🔴 must split (>800 rule)
internal/tui/render.go         463   ✅ well-factored
internal/tui/theme.go          323   ✅ gains style declarations
internal/tui/palette_comp.     264   ✅ component
internal/tui/tui_test.go      1400+  (tests may exceed file limit, exempt)
```

The guideline (`docs/code-organization.md`): files > 800 lines **must** be
split; target < 500 lines per concern. Section 1 of that document already
proposes this split; this plan is the detailed, line-verified version.

### Precedent: the tui2 breakup

PR #143 (`496d422`) broke up the tui2 god file the same way — a **file-level
split into focused concerns inside the same package**, keeping `tui2.go` as a
slim coordinator:

```
internal/tui2/
├── tui2.go            492   coordinator: struct, layout, wiring
├── control.go          65   control-plane (types.CtrlMsg) dispatch
├── events.go          100   agent event (agent.Event) dispatch
├── keymap.go           94   key bindings
├── markdown.go         44   markdown helpers
└── helpers_*.go      ~70    message/subagent/tool formatting
```

This plan applies the same shape to `internal/tui`.

### What is already componentized

The **rendering** layer is already broken into components and needs no work:

| Concern | File |
|---|---|
| Per-message renderers (user, assistant, system, sub-agent brackets) | `message_component.go` |
| Tool result box | `tool_component.go` |
| Error box | `error_component.go` |
| Expandable reasoning sections | `expandable_component.go` |
| Header (banner + title + MCP status) | `header_component.go` |
| Info bar | `info_bar_component.go` |
| Status bar | `status_component.go` |
| Todo table | `todo_component.go` |
| Command/Model/Question palettes + Help overlay | `palette_component.go` |
| Markdown/table/list/tree rendering pipeline | `render.go` |
| Themes and styles | `theme.go` |
| Key bindings | `keymap.go` |
| Shared helpers (`chatBubble`, `scrollWindow`) | `component.go` |

What remains in `tui.go` is **behavior, not rendering** — which is why this
plan is a file split plus mode-submodel extraction rather than new render
components (see `docs/tui-components.md`: "The Model owns all state.
Components own no state.").

## Constraints

1. **Public API is frozen.** Exactly one external consumer: `cmd/yaah/tui.go`.
   It uses only: `tui.New`, `tui.Config`, `*Model` as `tea.Model`,
   `SetMCPInfos`, `RegisterCommand`, `ApplyTheme`, `DetectTheme`. Everything
   else in the package is unexported or internal-only exported types
   (`Message`, `Command`, deprecated aliases `ServerInfo`, `QuestionModal`,
   `QuestionOption`).
2. **Behavior-neutral.** No rendering or interaction changes. The 126
   existing test results (`go test ./internal/tui/...`, including subtests)
   must pass unchanged through Phase 1 with **zero test edits**.
3. **Same-package move.** All new files stay in `package tui`, so symbol
   moves are invisible to tests and the compiler catches mistakes at each
   step.

### Baseline (recorded 2026-08-05, main @ `496d422`)

```
go build ./...               → ok
go test ./internal/tui/...   → ok (0.265s), 126 PASS/FAIL result lines
go vet ./internal/tui/...    → clean
```

> Note: the working tree on main currently has unrelated uncommitted changes
> (`cmd/yaah/agent_frame.go`, `internal/agent/*`). Branch for this work must
> be cut from a clean base or stash those first.

---

## Symbol inventory (verified against tui.go line numbers)

Every symbol in `tui.go` and its destination:

### → theme.go (style declarations)

Lines 33–65: the 31 style `var` declarations (`titleStyle` …
`errorBoxStyle`). Declared in `tui.go`, initialized by `ApplyTheme()` in
`theme.go` — move the declarations so both live together.

### → model.go (coordinator)

| Symbol | Lines | Notes |
|---|---|---|
| Package doc comment | 1 | Keep canonical doc on model.go |
| `Message` struct | 68–77 | |
| `ServerInfo` alias | 80–81 | Deprecated alias, keep |
| `cursorHoverMsg` | 84–86 | |
| `QuestionModal` / `QuestionOption` aliases | 89–94 | Deprecated aliases, keep |
| `Command` struct | 97–100 | |
| `defaultCommands` | 103–115 | |
| `Model` struct | 119–209 | Drop duplicated doc comment (lines 117–118) |
| `Config` struct | 213–238 | |
| `New()` | 241–287 | |
| `Init()` | 963–965 | |
| `Update()` | 968–1058 | Main dispatcher |
| `handleSpinnerTick()` | 1062–1080 | Update plumbing |
| `headerHeight()` | 469–471 | Layout accessor |
| `inputAreaHeight()` | 475–477 | Layout accessor |
| `refreshViewport()` | 480–482 | |
| `scrollToBottom()` | 485–487 | |

### → events.go (agent events + control plane + message state)

| Symbol | Lines | Notes |
|---|---|---|
| `AddAssistantMessage()` | 442–450 | |
| `AddAssistantMessageWithReasoning()` | 453–462 | |
| `AddMessage()` | 490–494 | |
| `AddToolResult()` | 499–519 | |
| `SetEphemeral()` | 523–526 | |
| `SetMCPInfos()` | 535–537 | |
| `SetThinking()` / `SetCompacting()` | 679–689 | |
| `SetToolCall()` / `ClearToolCall()` | 692–703 | |
| `AppendToken()` | 710–725 | Streaming debounce |
| `HandleEvent()` | 729–890 | 12-case agent event switch |
| `handleControlMsg()` | 893–960 | 8-case CtrlMsg switch |
| `HandleContextInfo()` | 1837–1846 | |
| `formatDuration()` | 1849–1854 | Only callers are HandleEvent cases |
| `parseTodosFromArgs()` | 1860–1885 | Only caller is AddToolResult |

### → input.go (normal-mode input routing + commands)

| Symbol | Lines | Notes |
|---|---|---|
| `RegisterCommand()` | 530–532 | Public API |
| `executeCommand()` | 541–632 | Colon-command dispatch |
| `handleMouseClick()` | 1084–1116 | Zone clicks |
| `viewportUpdate()` | 1118–1122 | |
| `handleKeyPress()` | 1127–1144 | Top-level mode router |
| `handleNormalKey()` | 1230–1349 | Largest handler |
| `detectCommandMode()` | 1352–1366 | |
| `clearCommandMode()` | 1368–1371 | |
| `hasReasoning()` | 1373–1383 | Only caller is handleNormalKey |

### → modes.go (overlay-mode logic: search, question, model picker)

| Symbol | Lines | Notes |
|---|---|---|
| `selectModel()` | 635–652 | Model picker |
| `filteredModels()` | 655–667 | |
| `exitModelMode()` | 670–676 | |
| `handleSearchKey()` | 1146–1175 | |
| `handleQuestionKey()` | 1177–1205 | |
| `handleModelKey()` | 1207–1228 | |
| `answerQuestion()` | 1385–1392 | |
| `commitQuestionAnswer()` | 1394–1406 | |
| `buildSearchMatches()` | 1508–1526 | |
| `searchNextMatch()` | 1529–1538 | |
| `searchPrevMatch()` | 1541–1550 | |
| `scrollToMatch()` | 1553–1558 | |

### → view.go (top-level layout)

| Symbol | Lines | Notes |
|---|---|---|
| `maxModelLines()` | 1412–1423 | Fix orphaned doc comment at 1408–1410 |
| `maxQuestionLines()` | 1425–1431 | |
| `paletteLines()` | 1433–1501 | Overlay height math |
| `adjustViewport()` | 1564–1598 | |
| `View()` | 1667–1779 | Top-level composition |
| Delete orphan doc: reRenderMessages | 464–466 | Function lives in render.go |
| Delete orphan doc: renderMessages | 1661–1664 | Function lives in render.go |

### → utils.go (text processing, shared by render.go + components)

| Symbol | Lines | Consumers |
|---|---|---|
| `mdLinkRe`, `autoLinkRe` | 290–292 | injectHyperlinks |
| `osc8Link()` | 295–297 | injectHyperlinks |
| `injectHyperlinks()` | 301–314 | render.go |
| `textSegment` | 316–319 | render.go parseAndRenderTables |
| `splitRow()` | 322–329 | render.go renderCompactTable |
| `replacePattern()` | 331–347 | render.go renderInlineMarkdown |
| `isWideRune()` | 349–360 | displayWidth |
| `bulletPattern`, `isListContent()` | 365–370 | render.go renderList |
| `treeLineRe`, `isTreeContent()` | 372–377 | render.go renderToolResult |
| `splitTreePrefix()`, `treeDepth()` | 380–407 | render.go renderTree |
| `displayWidth()` | 411–438 | wrapParagraph |
| `chatWrap()`, `wrapText()`, `wrapParagraph()` | 1603–1659 | component.go, palette_component.go, tool_component.go |
| `lolcatRender()` | 1856–1858 | render.go, info_bar_component.go |
| `ansiRe`, `stripANSI()` | 1887–1891 | render.go, View(), executeCommand, component_test.go |

### → next to their sole consumer (component files)

| Symbol | Lines | Move to | Why |
|---|---|---|---|
| `shortenCWD()` | 1783–1793 | `status_component.go` | Sole consumer |
| `contextBar()` | 1796–1819 | `status_component.go` | Sole consumer |
| `toolIndent()` | 1822–1834 | `tool_component.go` | Sole consumer |

### Resulting sizes (estimates)

```
internal/tui/
├── model.go            ~400   coordinator: types, Model, New, Init, Update
├── events.go           ~420   agent/control events + message state
├── input.go            ~330   key/mouse routing + command execution
├── modes.go            ~230   search/question/model-picker logic
├── view.go             ~280   View(), overlay height, adjustViewport
├── utils.go            ~250   text/markdown/width helpers
├── render.go            463   unchanged
├── theme.go            ~360   + style declarations
├── keymap.go             91   unchanged
├── component.go          29   unchanged
├── status_component.go  +47   + shortenCWD, contextBar
├── tool_component.go    +13   + toolIndent
└── tui.go              DELETED
```

Every file < 500 lines. ✅

---

## Phase 1 — Mechanical split (6 commits, zero behavior change)

Work on branch `break-up-tui-god-file`. **Build after every step**; each new
file declares its own imports (same package, so no cross-file changes).
Commit per step so any step can be reverted in isolation.

### Step 0: Branch + baseline

```powershell
git switch -c break-up-tui-god-file     # from clean main
go build ./... ; go test ./internal/tui/... -count=1   # confirm baseline
```

### Step 1: Move style declarations into theme.go

- Cut lines 32–65 (the 31 `var` declarations) from `tui.go`, paste above
  `ApplyTheme` in `theme.go`.
- `go build ./internal/tui/` → commit `refactor(tui): move style declarations to theme.go`.

Risk: none. Same package; gofmt only.

### Step 2: Extract utils.go

Move (per inventory): hyperlinks (`mdLinkRe`, `autoLinkRe`, `osc8Link`,
`injectHyperlinks`), table helpers (`textSegment`, `splitRow`,
`replacePattern`), width helpers (`isWideRune`, `displayWidth`), list/tree
detection (`bulletPattern`, `isListContent`, `treeLineRe`, `isTreeContent`,
`splitTreePrefix`, `treeDepth`), wrapping (`chatWrap`, `wrapText`,
`wrapParagraph`), and ANSI/lolcat (`lolcatRender`, `ansiRe`, `stripANSI`).

Required imports: `regexp`, `strings`, `"github.com/buchenberg/yaah/internal/banner"`.

- `go build ./internal/tui/` → commit `refactor(tui): extract text helpers to utils.go`.

Why before the method moves: utils are stateless leaf functions — the
compiler verifies the move completely, and later steps can't strand a
helper.

### Step 3: Move component helpers to their consumers

- `shortenCWD`, `contextBar` → `status_component.go`
- `toolIndent` → `tool_component.go`

- `go build ./internal/tui/` → commit `refactor(tui): co-locate component helpers`.

### Step 4: Extract model.go

Move: package doc, types (`Message`, `ServerInfo`, `cursorHoverMsg`,
`QuestionModal`, `QuestionOption`, `Command`), `defaultCommands`, `Model`
(delete the duplicated doc comment at lines 117–118), `Config`, `New()`,
`Init()`, `Update()`, `handleSpinnerTick()`, and the four layout accessors
(`headerHeight`, `inputAreaHeight`, `refreshViewport`, `scrollToBottom`).

Required imports: bubbles (`help`, `key`, `spinner`, `textarea`,
`viewport`), `tea`, `glamour`, `banner`, `todo` (for `todos []todo.Item`
field type), `types`, `agent` (Update matches `agent.Event`), `mcp` (alias).

- `go build ./internal/tui/` → commit `refactor(tui): extract model.go coordinator`.

### Step 5: Extract events.go

Move: all message-append methods, streaming setters, `HandleEvent()`,
`handleControlMsg()`, `HandleContextInfo()`, `formatDuration()`,
`parseTodosFromArgs()`, `SetMCPInfos()`. Also delete the orphaned
`reRenderMessages` doc comment (lines 464–466).

Required imports: `encoding/json`, `fmt`, `time`, `agent`, `subagent`,
`todo`, `types`.

- `go build ./internal/tui/` → commit `refactor(tui): extract event and control dispatch`.

### Step 6: Extract input.go and modes.go

- **input.go**: `RegisterCommand`, `executeCommand`, `handleMouseClick`,
  `viewportUpdate`, `handleKeyPress`, `handleNormalKey`,
  `detectCommandMode`, `clearCommandMode`, `hasReasoning`.
  Imports: `fmt`, `strings`, `tea`, `key`, `zone`, `clipboard`.
- **modes.go**: `selectModel`, `filteredModels`, `exitModelMode`,
  `handleSearchKey`, `handleQuestionKey`, `handleModelKey`,
  `answerQuestion`, `commitQuestionAnswer`, and the four search methods.
  Imports: `fmt`, `strings`, `tea`, `key`, `zone`.

- `go build ./internal/tui/` → commit `refactor(tui): extract input routing and overlay modes`.

### Step 7: Extract view.go, delete tui.go

Move: `maxModelLines`, `maxQuestionLines` (fix the orphaned doc comment at
1408–1410 into proper per-function docs), `paletteLines`, `adjustViewport`,
`View()`. Delete the orphaned `renderMessages` doc comment (1661–1664).
`tui.go` must now be empty of symbols — **delete the file**.

Imports: `fmt`, `lipgloss`, `zone`, `observability`.

```powershell
Remove-Item internal/tui/tui.go
go build ./...
```

- Commit `refactor(tui): extract view layout, delete tui.go`.

### Step 8: Quality gates

```powershell
gofmt -l internal/tui/                  # must be empty
go vet ./internal/tui/...               # clean
go run honnef.co/go/tools/cmd/staticcheck@latest ./internal/tui/...   # clean
go test ./internal/tui/... -count=1     # 126 results, zero test edits
go test ./... -count=1                  # full suite
git diff --stat main -- internal/tui/   # sanity: only moved code
```

Optional whitespace-only verification: `git diff main -w -- internal/tui`
should show only the deliberate deletions (duplicate doc comments, orphaned
comments), confirming no logic changed.

---

## Phase 2 — Mode sub-models (optional, separate PR)

The file split solves the god-file problem; Phase 2 goes further toward the
tui2 component ideal by giving each interactive overlay its own **stateful
sub-model**, shrinking `Model` from ~60 fields to ~35 and making each mode
independently testable.

> Decision gate: Phase 2 churns `tui_test.go` (57 references to
> `m.questionMode`, `m.modelSelected`, `m.searchQuery`, etc.). It is
> recommended but separable; land Phase 1 first and decide per review
> feedback.

### Design

Four sub-model structs, one file each, owned as fields on `Model`. Each
owns its mode flag, per-mode state, key handler, height math, and the
mouse/hover contract. Rendering still delegates to the existing stateless
palette components (`docs/tui-components.md` contract preserved).

| Sub-model | New file | Owns (moved from Model) | Key methods |
|---|---|---|---|
| `searchModel` | `search_model.go` | `searchMode`, `searchQuery`, `searchMatches`, `searchIdx` | `handleKey()`, `buildMatches(view)`, `next()`, `prev()`, `renderLine()`, `height()` |
| `questionModel` | `question_model.go` | `questionMode`, `questionModal`, `questionIdx`, `questionMulti` | `openQuestion(*types.CtrlQuestion)`, `openApproval(*types.CtrlApproval)`, `handleKey()`, `handleClick(zone) bool`, `answer()`, `commit()`, `height(maxLines)` |
| `pickerModel` | `picker_model.go` | `modelMode`, `modelItems`, `modelSelected`, `providerNames` | `open()`, `handleKey()`, `filtered(filter)`, `select() (provider, model, ok)`, `exit()`, `height()` |
| `commandState` | `command_state.go` | `commandMode`, `commands` | `register()`, `detect(inputValue)`, `clear()`, `matchCount(filter)`, `height()` |

- `handleKeyPress()` in `input.go` becomes a thin router over the
  sub-models; `handleControlMsg()` delegates `CtrlQuestion`/`CtrlApproval`
  to `questionModel.open*`.
- `executeCommand()` **stays** a `Model` method in input.go: it touches too
  many host concerns (banner toggle, compaction callback, clipboard, agent
  abort) to decouple cleanly. `commandState` only owns palette state.
- Side effects the sub-models need from the host (`refreshViewport`,
  `adjustViewport`, `setEphemeral`, input placeholder reset) are passed as
  callback fields set in `New()`, matching the `Config.On*` callback
  convention already used for agent callbacks.
- Zone IDs (`question-opt-%d`) stay stable so mouse handling and
  `palette_component.go` render code are untouched.

### Test migration (Phase 2 only)

- 57 field references in `tui_test.go` become sub-model references
  (`m.questionIdx` → `m.question.idx`, `m.modelMode` → `m.picker.active`).
  Mechanical; keep assertions identical.
- Add `modes_test.go` unit tests per sub-model (open → key sequence →
  committed answer) that previously needed a full `Model`.

### Phase 2 gates

Same as Step 8, plus `go test ./internal/tui/... -count=1 -race` (question
modal uses an answer channel with a goroutine).

---

## Documentation updates (with Phase 1)

1. `docs/tui-components.md` — update references that name `tui.go`
   ("paletteLines() in tui.go", styles "declared in tui.go") to the new
   file names.
2. `docs/code-organization.md` — mark the `internal/tui/tui.go` split
   (section 1 + Future-Work item 2) as done; update the file-table row.
3. `AGENTS.md` — no change needed (it lists the directory, not files).

## What does NOT change

- Public API: `New`, `Config`, `Model`, `SetMCPInfos`, `RegisterCommand`,
  `ApplyTheme`, `DetectTheme`, exported types and deprecated aliases.
- All `*_component.go` renderers and their tests (`component_test.go`).
- `render.go`, `keymap.go`, `component.go` logic.
- Zone-ID scheme, key bindings, palette layouts, streaming debounce.
- `cmd/yaah/tui.go`.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Missing import in a new file breaks build | Medium | Build after every step; errors name the exact file/symbol |
| Logic accidentally altered during move | Low | Moves are cut/paste only; `git diff main -w` must show only intentional deletions; 126 tests unchanged |
| Orphaned/duplicated doc comments cause golint noise | Low | Inventory lists each one (lines 117–118, 464–466, 1408–1410, 1661–1664) |
| Phase 2 test churn hides a behavior regression | Medium | Gate Phase 2 behind Phase 1 merge; compare test assertions line-by-line, add `-race` |
| Dirty working tree on main conflicts with branch | Low | Step 0 requires a clean base (current uncommitted agent-context work must land or be stashed first) |

## Rollback

Each Phase-1 step is an independent commit; `git revert <sha>` in reverse
order restores the monolith with no data loss. Phase 2 is reverted as a
single PR.
