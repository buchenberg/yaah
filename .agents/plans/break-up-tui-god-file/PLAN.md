---
name: break-up-tui-god-file
description: Split the 1729-line monolithic tui.go into 5 focused files: model, view, input, events, utils + consolidate style vars into theme.go
status: draft
---

# Break Up tui.go God File

## Context

`internal/tui/tui.go` is a 1729-line monolith containing the Model struct,
the bubbletea View/Update/Init methods, all keyboard handling, agent-event
dispatching, message management, view helpers, and text-processing utilities.
The rest of the package is already well-factored into per-concern files
(render.go, theme.go, component files), but tui.go remains the last god file.

This plan follows the blueprint in `docs/code-organization.md` (monolithic
god-file candidates section), adjusted for the actual current state of
the code and the existing `render.go`.

## Target file layout

```
internal/tui/
├── model.go         ~350 lines   Model, Config, types, structs, New(), Init(), Update()
├── view.go          ~320 lines   View(), layout helpers, palette rendering, context bar
├── render.go         ~409 lines  [EXISTING] Markdown/tool-content rendering pipeline
├── input.go         ~350 lines   Keyboard/mouse handlers, command dispatch, model selector
├── events.go        ~350 lines   HandleEvent(), handleControlMsg(), message-append methods (merged from the thin messages.go)
├── utils.go         ~200 lines   Text processing, tree/list detection, search helpers
└── theme.go          ~310 lines  [EXISTING] + migrate the 31 style var declarations from tui.go
```

## Step-by-step

### Step 1: Move style var declarations into theme.go

**What:** The 31 package-level `var` declarations at the top of `tui.go`
(normalStyle, thinkingStyle, toolStyle, etc.) are *declared* in `tui.go`
but *initialized* by `ApplyTheme()` in `theme.go`. Move the declarations
into `theme.go` so declaration and initialization live in the same file.

**Files touched:**
- `internal/tui/theme.go` — add var block (31 declarations)
- `internal/tui/tui.go` — remove var block

**Risk:** None — same package, no import changes. `gofmt` afterward.

### Step 2: Extract model.go

**Create:** `internal/tui/model.go`

**Contents:**
- Package declaration + imports
- Types: `Message`, `ServerInfo`, `cursorHoverMsg`, `QuestionModal`, `QuestionOption`, `Command`
- `defaultCommands` slice
- `Model` struct with all fields
- `Config` struct
- `New()` constructor
- `Init()` bubbletea method
- `Update()` bubbletea method (~90 lines, the main orchestrator)
- `handleSpinnerTick()` + spinner tick handler
- Small accessors: `headerHeight()`, `inputAreaHeight()`, `refreshViewport()`, `scrollToBottom()`

**Order of removal from tui.go:** Types first, then constructor, then
Init/Update, then tick handlers, then accessors. Build after each move
to catch any missing symbols.

### Step 3: Extract view.go

**Create:** `internal/tui/view.go`

**Contents:**
- `View()` — the top-level layout method (~112 lines)
- `shortenCWD()`, `contextBar()`, `toolIndent()` — header/context helpers
- `chatWrap()`, `wrapText()`, `wrapParagraph()` — line-wrapping
- `paletteLines()` — model selector list rendering
- `maxModelLines()`, `maxQuestionLines()` — height calculations
- `lolcatRender()`, `formatDuration()`, `stripANSI()` — tiny rendering utils
- `adjustViewport()` — viewport scrolling after content change

**Note:** `render.go` already handles markdown/tool/message rendering.
`view.go` is strictly the top-level layout container — it calls into
`render.go` (e.g., `renderMessages()`).

### Step 4: Extract input.go

**Create:** `internal/tui/input.go`

**Contents:**
- `handleKeyPress()` — top-level key dispatcher
- `handleSearchKey()`, `handleQuestionKey()`, `handleModelKey()`, `handleNormalKey()` — per-mode handlers
- `handleMouseClick()`
- `detectCommandMode()`, `clearCommandMode()`
- `executeCommand()` — slash-command dispatch (~91 lines, the biggest)
- `selectModel()`, `filteredModels()`, `exitModelMode()`
- `RegisterCommand()`
- `answerQuestion()`, `commitQuestionAnswer()`

### Step 5: Extract events.go (includes messages.go content)

**Create:** `internal/tui/events.go`

**Contents:**
- `HandleEvent()` — type-switch over all agent event types (~161 lines)
- `handleControlMsg()` — todo/question/model/approval dispatch (~67 lines)
- `HandleContextInfo()`
- Message-append methods (merged from the originally-planned messages.go):
  - `AddMessage()`, `AddToolResult()`
  - `AddAssistantMessage()`, `AddAssistantMessageWithReasoning()`
  - `AppendToken()`, `SetEphemeral()`
- State setters: `SetThinking()`, `SetCompacting()`, `SetToolCall()`, `ClearToolCall()`, `SetMCPInfos()`

**Why merge messages.go:** The ~105 line messages.go would have been
the thinnest file in the package. HandleEvent already dispatches to all
these methods — keeping them together is cohesive and produces a
healthier ~350-line file instead of two anemic ones.

### Step 6: Extract utils.go

**Create:** `internal/tui/utils.go`

**Contents:**
- `textSegment` type + `splitRow()`, `replacePattern()`
- `isWideRune()`, `displayWidth()`
- `bulletPattern`, `isListContent()`, `treeLineRe`, `isTreeContent()`, `splitTreePrefix()`, `treeDepth()`
- `buildSearchMatches()`, `searchNextMatch()`, `searchPrevMatch()`, `scrollToMatch()`
- `parseTodosFromArgs()`, `injectHyperlinks()`
- `osc8Link()`
- `hasReasoning()`

### Step 7: Delete tui.go + verify

Once all symbols have been moved and the file is empty:
```
rm internal/tui/tui.go
```

### Step 8: Quality gates

```bash
go build ./...                    # must compile
go test ./internal/tui/...        # must pass (109 tests currently)
go vet ./internal/tui/...         # must be clean
staticcheck ./internal/tui/...    # must be clean
gofmt -w internal/tui/            # normalize formatting
go test ./...                     # full suite (1061 tests)
```

## What does NOT change

- `render.go` — stays as-is. It's already well-organized.
- All component files — untouched.
- `theme.go` — only gains the var block, no logic changes.
- `keymap.go` — untouched.
- `tui_test.go` — should need zero changes (symbols stay in same package).
- Public API surface — unchanged.

## Risks

| Risk | Likelihood | Mitigation |
|------|:---:|-----------|
| Build break from missing import in new file | Medium | Build after each step; each new file must declare its own imports |
| Accidental symbol duplication | Low | Go compiler catches redeclarations at build time |
| Order-dependent test compilation | Low | Same package — test symbols remain visible |
| Missed function during extraction | Low | Compiler catches missing methods on `*Model`
