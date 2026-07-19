# TUI Implementation Review & Improvement Plan

> Produced by applying the `tui-design` skill against `internal/tui/tui.go` (1,758 LOC) + `cmd/yaah/tui.go` (444 LOC).

**Date:** 2025-07-18
**Scope:** The full terminal UI layer — Bubble Tea v2 model, rendering pipeline, interactivity, keybindings, theming, responsiveness, testing.

---

## 1. Executive Summary

The yaah TUI is a solid, working AI-chat interface built on the **correct ecosystem (Charm stack v2)** with good fundamentals: alt screen, proper Bubble Tea MVU architecture, markdown rendering via Glamour, clickable OSC 8 hyperlinks, streaming responses, reasoning support, and a well-tested text-input/command subsystem. The test file (1,414 LOC) is thorough for the utility layer.

**The main gap is polish**: hardcoded colors (no theming, no `NO_COLOR`), missing panic/suspend hygiene, non-standard keybindings (Esc-as-quit), absence of discoverability layers (no footer hints, no `?` help, no `hjkl`), and no responsive fallback message for tiny terminals. These are exactly the kind of issues that distinguish a "working" TUI from a "professional" one.

**Overall assessment:** 6/10 — functional but unfinished. The path to 9/10 is ~2–3 days of work, mostly additive, with zero architectural risk.

---

## 2. Strengths (What's Done Well)

1. **Correct framework choice.** Bubble Tea v2 + Lipgloss v2 + Bubbles v2 + Glamour v2 — the Charm stack is the right Go TUI ecosystem for a chat application. Import paths are all on `charm.land/*/v2`.

2. **Clean MVU architecture.** The `Model` struct, `Init()`, `Update(msg)`, and `View()` are idiomatic Bubble Tea. Messages from the agent flow via a `chan AgentMsg` and are handled in `Update()` — exactly right.

3. **Alt screen + mouse mode.** Set correctly on the view: `v.AltScreen = true`, `v.MouseMode = tea.MouseModeCellMotion`. No scrollback pollution.

4. **Glamour + table pipeline.** Markdown rendering with Glamour (dark theme, 256-color chroma), plus a custom markdown-table → Lipgloss table converter that handles wrapping, inline markdown styling, borders, and CJK/emoji. This is genuinely good work.

5. **OSC 8 hyperlinks.** `injectHyperlinks()` converts markdown `[text](url)` and `<url>` autolinks into clickable OSC 8 sequences before Glamour processes them. Survives SSH and tmux (with `allow-passthrough`). Rare and well-executed.

6. **Reasoning/thinking support.** Expandable `▶ Reasoning...` / `▼ Reasoning...` sections via `bubblezone` click targets and `ctrl+r` toggle. Active reasoning shown inline with spinner during streaming.

7. **Interactive question modal.** Full modal dialog with single/multi-select, keyboard navigation (`↑↓ Enter Space Esc`), mouse-click on options via zones, windowed overflow. The right way to handle `ask_user` questions.

8. **Model selection palette.** `/model` enters a filterable, scrollable model list with provider headings, current-model indicator, and `↑↓` navigation. Good progressive disclosure.

9. **Custom list/tree rendering.** Detects bullet lists (`* - +`) and tree drawings (`├── └──`) in tool output and renders them through Lipgloss `list` and `tree` packages. Smart detection.

10. **Context window bar.** The status bar shows a segmented fill bar (`[████░░░░ 40%]`) with color-coded thresholds. Good at-a-glance signal.

11. **Ctrl+Y clipboard copy.** Copies the last assistant message to the system clipboard via `tea.SetClipboard`. Surfaces the raw markdown (not the ANSI-rendered version). Good.

12. **Thorough tests.** 1,414 lines of test code covering: `splitRow`, `renderCompactTable`, `parseAndRenderTables`, `renderMarkdown`, `isListContent`, `isTreeContent`, `renderList`, `renderTree`, `renderToolResult`, `splitTreePrefix`, `treeDepth`, `defaultCommands`, `executeCommand`, command suggestions, model filtering/selection, reasoning collapse/expand/transfer, question modal lifecycle, and zone tracking.

---

## 3. Issues by Severity

### 🔴 Critical (Terminal Hygiene & Safety)

| # | Issue | Location | Skill Ref |
|---|-------|----------|-----------|
| **C1** | **No terminal restore on panic.** The `defer` in `cmd/yaah/tui.go:178` only calls `db.EndSession`/`db.Close`. If the TUI panics, raw mode + alt screen survive, corrupting the terminal. | `cmd/yaah/tui.go:178` | SKILL.md → "Always restore terminal state on exit — even on panic" |
| **C2** | **No SIGTSTP/SIGCONT handling.** `Ctrl+Z` is not handled. The terminal stays in raw mode when the process is suspended, and it doesn't re-enter alt screen / force redraw on resume. | Missing entirely | SKILL.md → "Handle suspend (Ctrl+Z / SIGTSTP)" |
| **C3** | **Esc unconditionally quits.** Outside of sub-modes (question/mode), `Esc` maps to `tea.Quit`. Convention is `Esc = cancel/back`. A user hitting `Esc` to dismiss a thought loses their entire session. | `internal/tui/tui.go:1058-1066` | SKILL.md → cross-app conventions table |

### 🟠 High (Color, Theming, Discoverability)

| # | Issue | Location | Skill Ref |
|---|-------|----------|-----------|
| **H1** | **All 17 colors are hardcoded.** `lipgloss.Color("39")`, `"14"`, `"252"`, etc. No semantic tokens. Users with light terminal themes get invisible text; custom themes are impossible. | Styles block (lines 22–90) | SKILL.md → "Color as a semantic system"; ecosystem-go.md → "LightDark" |
| **H2** | **No `NO_COLOR` support.** The app ignores the `NO_COLOR` environment variable. | Missing entirely | SKILL.md → "Always respect `NO_COLOR`" |
| **H3** | **No footer hint bar.** Users have no way to discover keybindings except reading source. No `?` help screen, no always-visible shortcut row. | Missing entirely | SKILL.md → "Status bars, headers, footers"; "Discoverability is layered" |
| **H4** | **No `key.Binding` declarations.** Raw string comparisons in the `Update` switch (`case "ctrl+c":`, `case "esc":`, etc.). No reusable keymap, no `help.Model` integration. | `Update()` switch statement | ecosystem-go.md → "Declarative keys with key.Binding" |
| **H5** | **Color-only signals.** Thinking spinner, context bar segments, and status elements encode state via color alone. Not CVD-safe. | `renderMessages()`, `contextBar()` | SKILL.md → "Never use color alone" |

### 🟡 Medium (Interaction & Responsiveness)

| # | Issue | Location | Skill Ref |
|---|-------|----------|-----------|
| **M1** | **No `hjkl` / `gg`/`G` / `/` search.** Viewport only scrolls via mouse wheel, arrow keys, PgUp/PgDn. No vim-style navigation, no search within chat. | `Update()` — viewport forwarding | SKILL.md → cross-app conventions table |
| **M2** | **No minimum size or "too small" message.** At tiny terminals, the viewport silently collapses to 5 lines. The skill calls out 80×24 + 60-col tmux split as required floor tests. | `adjustViewport()`: `if chatHeight < 5 { chatHeight = 5 }` | SKILL.md → "Pressure-test the floor" |
| **M3** | **Banner wastes vertical space.** The Figlet + Lolcat ASCII art banner takes 6–10 lines. In a 24-row terminal, that's 25–40% of the screen on permanent chrome. No way to dismiss it. | `headerHeight()`, `View()` header | visual-patterns.md → "The clutter audit" — "ratio of cells spent on chrome vs data" |
| **M4** | **No re-render throttling.** Streaming tokens trigger `refreshViewport()` + `scrollToBottom()` on every token. For fast streaming models (GPT-4o at ~80 tok/s), this is 80 full viewport rebuilds per second. | `AppendToken()` | ecosystem-go.md → "Don't redraw on a fixed timer" (implied: don't redraw at the token rate either) |
| **M5** | **Glamour re-render on every resize.** `reRenderMessages()` re-renders *every* assistant message through Glamour when the window resizes. For long conversations, this can block the UI thread for noticeable periods. | `reRenderMessages()` | ecosystem-go.md → "work smuggled into View() that should be cached" |
| **M6** | **`Ctrl+R` is a surprising toggle key.** Convention is `Ctrl+R` = reverse search (bash, fzf, atuin). Toggling reasoning expansion is fine but should be on a less surprising keybinding, or at least documented. | `Update()`: `case "ctrl+r":` | interaction-patterns.md → keybinding conventions |

### 🟢 Low (Polish & Nice-to-Haves)

| # | Issue | Location | Skill Ref |
|---|-------|----------|-----------|
| **L1** | **No async model list fetch retry.** If the initial model fetch fails, the model palette shows nothing permanently. | `HandleAgentMsg` model list case | — |
| **L2** | **Status bar duplicates header info.** Both the header line and the status bar show `provider/model`. Redundant. | `View(): header`, `View(): statusText` | visual-patterns.md → "chrome-vs-data ratio" |
| **L3** | **Commands are not extensible.** `defaultCommands` is a hardcoded slice. User-provided commands or MCP tools cannot register themselves. | `defaultCommands` var | — |
| **L4** | **No shell completions.** Cobra supports them but the TUI subcommand doesn't wire them. | `cmd/yaah/tui.go` | ecosystem-go.md: "Cobra ships shell completion generation — wire it up" |
| **L5** | **No auto-fade for status messages.** "Compacted." and similar feedback sit in messages permanently. A transient auto-fading status line would be cleaner. | `executeCommand` compact output | visual-patterns.md → "Status / mode line — ephemeral feedback with auto-fade" |
| **L6** | **No session/chat persistence indicator in UI.** Messages are saved to SQLite but there's no visual indicator of save state. | Missing | — |

---

## 4. Clutter Audit

Per the tui-design skill method (name the cuts, don't say "simplify"):

| Offender | Count | What it encodes | Cut? |
|----------|-------|-----------------|------|
| **Banner** (figlet ASCII art) | 6–10 rows | Branding / aesthetic | **Cut or collapsible.** Wasteful in small terminals. Consider a 1-line compact header or a `--no-banner` flag. |
| **Status bar duplicates header** | 2 signals | `provider/model` shown in both header and status bar | **Remove from status bar.** Keep messages + context bar in status; the header already shows provider/model. |
| **Thinking spinner** | 2 signals | Spinner icon + "Thinking..." text | **Keep both.** The text is needed for screen readers; the spinner is standard UX. This is the right kind of duplication. |
| **Full datetime on every log line** | N/A | Not present — yaah doesn't timestamp messages in the viewport. | Could be added optionally, but right now this is a non-issue. |

**Border nesting depth:** 0 (no nested borders). The viewport has no border, the command palette has one level. Good.

---

## 5. Floor Pressure Test

| Terminal Size | What Happens | Verdict |
|---------------|-------------|---------|
| **200×60** (large monitor) | Full banner, comfortable padding. Viewport tall. | ✅ Fine |
| **120×40** (standard) | All features work. Banner takes ~6 lines, viewport ~32 lines. | ✅ Fine |
| **80×24** (minimum floor) | Banner (~6) + header (1) + status (1) + input (1) = 9 lines chrome. Viewport gets 15 lines. Workable but tight. No "terminal too small" message. | ⚠️ Tight, no warning |
| **80×24 with 60-col tmux split** | Width=60. Tables wrap hard. Long code blocks overflow. Glamour word-wrap handles prose. | ⚠️ Functional but cramped |
| **60×20** (SSH from phone) | `chatHeight` clamped to 5. Banner shown. Input functional. Barely usable but doesn't crash. | ❌ Should show "terminal too small" |
| **40×15** (embedded) | Mostly broken. No user feedback. | ❌ Same |

**Recommendation:** Add a minimum size check: if `height < 20` or `width < 60`, render a centered "yaah needs at least 60×20" message instead of the full UI.

---

## 6. Improvement Plan (Phased)

### Phase 1 — Terminal Hygiene & Safety (~2 hours)

These are the true non-negotiables from the skill.

- [ ] **P1.1 — Panic recovery.** Wrap `p.Run()` with terminal restore:
  ```go
  defer func() {
      if r := recover(); r != nil {
          // Bubble Tea already restores, but belt-and-suspenders.
          fmt.Printf("yaah panic: %v\n", r)
          os.Exit(1)
      }
  }()
  ```
  Bubble Tea v2 restores terminal state on `Run()` return even on panic, but the skill explicitly calls this out and it costs nothing to be explicit.

- [ ] **P1.2 — Suspend/resume.** Install a SIGTSTP handler:
  ```go
  sigs := make(chan os.Signal, 1)
  signal.Notify(sigs, syscall.SIGTSTP, syscall.SIGCONT)
  go func() {
      for sig := range sigs {
          switch sig {
          case syscall.SIGTSTP:
              p.Send(tea.SuspendMsg{})
          case syscall.SIGCONT:
              p.Send(tea.ResumeMsg{})
          }
      }
  }()
  ```
  Bubble Tea v2 handles the terminal state transitions internally on these messages.

- [ ] **P1.3 — Fix Esc behavior.** Make `Esc` back out of command mode (clear input, exit command mode) and only quit when there's nothing to back out of. Add a confirmation for Esc-quit or require `Ctrl+C` for quit.

### Phase 2 — Color System & Theming (~4 hours)

- [ ] **P2.1 — Semantic color tokens.** Replace all 17 hardcoded color literals with a named map:
  ```go
  type Theme struct {
      Title, User, Assistant, Tool, Status, Spinner,
      Code, Bold, Italic, Thinking, Toggle, ListBullet,
      ListItem, Tree, TreeItem, CommandBorder, CommandName,
      CommandDesc lipgloss.Color
  }
  ```
  Define a `DarkTheme` (current values) and a `LightTheme`.

- [ ] **P2.2 — Auto-detect background.** Use `lipgloss.HasDarkBackground(os.Stdin, os.Stdout)` at startup and select Dark/Light variant. Use `lipgloss.LightDark(hasDark)` for individual style picking.

- [ ] **P2.3 — Respect `NO_COLOR`.** Check `os.Getenv("NO_COLOR")` at startup. When set, render all styles without color (monochrome: bold/dim/italic only, no foreground/background colors). This makes the context bar and status bar work as weight-only indicators.

- [ ] **P2.4 — Theme config file.** Load `~/.yaah/theme.yaml` with an optional named theme field. Support Catppuccin Mocha, Dracula, Nord, Gruvbox as built-in presets.

- [ ] **P2.5 — Add letter/symbol pairs to color signals.** The context bar already shows `%`; add letter markers. The thinking indicator should show `[THINKING]` not just a colored spinner.

### Phase 3 — Discoverability & Keybindings (~3 hours)

- [ ] **P3.1 — Declarative keymap.** Define a `keyMap` struct with `key.Binding` entries for all actions. This makes the keybindings self-documenting and enables the `help` Bubble.

- [ ] **P3.2 — Footer hint bar.** Add a 1-line footer at the bottom with 3–5 most-used shortcuts:
  ```
  /help   ↑↓ scroll   ctrl+y copy   ctrl+c quit   / commands
  ```
  Use `bubbles/help` to auto-render from the keymap.

- [ ] **P3.3 — Full help screen (`?`).** Show a modal or overlay with every keybinding, grouped by category. Borrow the layout from lazygit or glow.

- [ ] **P3.4 — Add `hjkl`, `gg`, `G`, `/` search.** Forward `j`/`k` to viewport scroll, `gg`/`G` to top/bottom, `/` to an in-viewport search mode (Bubbles viewport doesn't have built-in search, so add a search overlay or use `strings.Contains` to highlight matches).

- [ ] **P3.5 — Re-bind `Ctrl+R`.** Move reasoning toggle to `Ctrl+T` (or another unused key). `Ctrl+R` is too strongly associated with reverse search.

### Phase 4 — Responsive Design & Performance (~3 hours)

- [ ] **P4.1 — Minimum size check.** If `height < 20 || width < 60`, render a centered "Terminal too small — yaah needs at least 60×20" message instead of the normal UI.

- [ ] **P4.2 — Token-rate throttling.** Batch streaming token updates to the viewport. Use a debounce — update viewport at most every 50ms (20 FPS). Accumulate tokens in `streamContent` and update viewport on a `tea.Tick` or on the debounce timer.

- [ ] **P4.3 — Cache Glamour renders.** Store the rendered output alongside the raw markdown in `Message`. `reRenderMessages()` still needs to re-render everything, but regular `renderMessages()` should use pre-rendered content. This is already partially done (Message.Content stores rendered output) but verify it's used correctly in `renderMessages()`.

- [ ] **P4.4 — Banner toggle.** Add a `/banner` command or a `--no-banner` flag. When hidden, reclaim the vertical space. Consider a 1-line compact header variant: `yaah · openai/gpt-4o`.

### Phase 5 — Polish (~2 hours)

- [ ] **P5.1 — De-duplicate provider/model display.** Remove provider/model from the status bar. Keep it in the header only. Status bar becomes: `messages: N │ ctx: [████░░░░ 40%]`.

- [ ] **P5.2 — Auto-fade status messages.** Add an ephemeral status line (1 line above input) that shows "Compacted." / "Model switched to X" / "Copied!" and auto-clears after 3 seconds. Use a `tea.Tick` timer.

- [ ] **P5.3 — Extensible commands.** Allow MCP tools to register slash commands (e.g., `/memory search ...`, `/skill list`). Add a `RegisterCommand(name, desc string)` method to the Model.

- [ ] **P5.4 — Shell completions.** Wire Cobra completions for the `yaah tui` subcommand and flag values.

- [ ] **P5.5 — Golden-file snapshot tests.** Add 3–4 visual regression tests using `teatest/v2` with ASCII color profile and pinned 80×24 size. Cover: empty state, a few messages, command palette open, question modal.

---

## 7. Implementation Notes

### Keybinding Reference (Target State)

| Key | Action |
|-----|--------|
| `Ctrl+C` | Quit |
| `Esc` | Back (dismiss command mode / model mode / question; only quit if nothing to dismiss) |
| `?` | Help screen |
| `/` | Search in chat |
| `n` / `N` | Next / prev search match |
| `Enter` | Submit message / confirm selection |
| `PgUp` / `PgDn` | Scroll viewport (existing) |
| `↑` / `↓` / `j` / `k` | Scroll viewport line by line |
| `gg` / `G` | Jump to top / bottom of chat |
| `Ctrl+Y` | Copy last assistant response |
| `Ctrl+T` | Toggle reasoning expand/collapse |
| `r` | Refresh (re-fetch models) |
| Tab / Shift+Tab | Cycle focus (future: input ↔ viewport) |

### File Changes Required

| File | Changes |
|------|---------|
| `internal/tui/tui.go` | Color tokens, theme struct, keymap, footer, help screen, resize floor, Esc behavior, debounce throttle, cache Glamour, command extensibility |
| `internal/tui/theme.go` (new) | Theme definition, presets, NO_COLOR handling, YAML loading |
| `internal/tui/keymap.go` (new) | `key.Binding` declarations, `keyMap` struct |
| `internal/tui/tui_test.go` | Add Update() pure-function tests, golden-file snapshots |
| `cmd/yaah/tui.go` | Panic recovery, SIGTSTP/SIGCONT, theme loading, `--no-banner` flag |

### Risk Assessment

- **Zero risk:** P1 (terminal hygiene), P2.1–P2.3 (semantic color tokens), P3.1–P3.3 (key declarations + footer + help), P4.1 (minimum size), P5.1–P5.2 (de-dupe status, auto-fade).
- **Low risk:** P2.4–P2.5 (theme config), P3.4 (new keybindings), P4.4 (banner toggle), P5.3 (extensible commands).
- **Moderate risk:** P4.2 (token rate throttling — needs careful testing with real streaming), P4.3 (Glamour render caching — verify re-render correctness), P3.5 (Ctrl+R re-bind — backward compat consideration).

---

## 8. References Consulted

- `tui-design/SKILL.md` — Full review checklist and universal principles
- `tui-design/references/ecosystem-go.md` — Bubble Tea v2 idioms, key.Binding, teatest, debugging
- `tui-design/references/visual-patterns.md` — Clutter audit method, responsive floor, borders, density
- `tui-design/references/interaction-patterns.md` — Keybinding conventions, discoverability layers, Esc semantics

*Generated by applying the tui-design skill to the yaah codebase.*
