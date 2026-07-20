# TUI Component System

How the yaah TUI (`internal/tui/`) renders its visual elements through a
React-like component system: stateless renderers, one per UI element, with
styling centralized in `theme.go`.

## Overview

```
tui.go (Model, View, Update, HandleAgentMsg)
   │
   ├── View() ─────────► Header, StatusBar, palettes, viewport, input, footer
   │
   ├── renderMessages() ► per-message components (user, assistant, tool, …)
   │        (render.go)
   │
   └── theme.go ────────► package-level lipgloss styles (single source
                          of truth for colors)
```

The `Model` owns all state (messages, streaming buffers, expansion maps,
mode flags). Components own **no** state — each is constructed from model
state at render time and discarded. This keeps the bubbletea Elm loop intact
while making every visual element independently testable.

## The Component interface

```go
type Component interface {
    Render() string
}
```

Deliberately minimal. Components are mutable structs built per render pass;
there is no builder chain and no children/composition tree. `BaseComponent`
in `component.go` provides the shared "styled, width-constrained content"
case for trivial uses.

## Component catalog

| Component | File | Renders | Constructed in |
|---|---|---|---|
| `UserMessage` | `message_component.go` | Bold user text, wrapped, user background | `renderMessages()` |
| `AssistantMessage` | `message_component.go` | Assistant-colored content | `renderMessages()` |
| `SubAgentBracket` | `message_component.go` | `╭─` / `╰─` sub-agent container corners | `renderMessages()` |
| `SystemMessage` | `message_component.go` | System text on system background | `renderMessages()` |
| `ExpandableSection` | `expandable_component.go` | Zone-marked ▶/▼ toggle + content | `renderMessages()` |
| `ToolMessage` | `tool_component.go` | Tool header, bordered output box, truncation | `renderMessages()` |
| `Header` | `header_component.go` | ASCII banner + provider/model title | `View()` |
| `StatusBar` | `status_component.go` | cwd, message count, context bar | `View()` |
| `CommandPalette` | `palette_component.go` | Colon-command suggestions | `View()` |
| `ModelPalette` | `palette_component.go` | Provider-grouped model picker | `View()` |
| `QuestionPalette` | `palette_component.go` | Interactive question modal | `View()` |
| `HelpOverlay` | `palette_component.go` | Full keybinding help | `View()` |
| `TodoTable` | `todo_component.go` | Todo list as a borderless table | `renderToolResult()` |

## Rendering flow

### Messages (`renderMessages()` in render.go)

The message loop keeps the two pieces of cross-render state components
cannot own: the zone-ID registries (`m.reasoningZones`, `m.toolZones`) and
the expansion maps (`m.reasoningExpanded`, `m.toolExpanded`). Each iteration
generates the zone ID, appends it to the registry, resolves the expansion
state, then delegates rendering:

```go
case "tool":
    zoneID := fmt.Sprintf("tool-%d", msgIdx)
    m.toolZones = append(m.toolZones, zoneID)

    expanded, has := m.toolExpanded[zoneID]
    if !has {
        expanded = m.toolCall == msg.ToolName // running tools start expanded
    }

    b.WriteString(NewToolMessage(
        zoneID, msg.ToolName, msg.ToolArgs, msg.Content,
        m.width, m.viewport.Height(), expanded,
        m.toolCall == msg.ToolName,
    ).Render())
```

The live thinking/streaming sections at the bottom of `renderMessages()`
are intentionally **not** components — they are bound to spinner state and
debounced refresh flags in `Model`, and forcing them into the component
model would add indirection without test benefit.

### Chrome (`View()` in tui.go)

`View()` composes header, viewport, status bar, optional ephemeral line,
optional palette, search line, input, and footer with
`lipgloss.JoinVertical`. The header, status bar, and palettes are
components; the viewport, text input, spinner, and help footer remain
bubbletea widgets (out of scope for the component system).

## Styling

Components never define colors. They consume the package-level
`lipgloss.Style` variables declared in `tui.go` and initialized by
`ApplyTheme()` in `theme.go`. Layout properties (width, truncation budgets,
bordered-box geometry) live inside the components themselves.

This split is deliberate:

- **Colors/themes** — `theme.go`, one place, theme-switchable at runtime.
- **Layout/geometry** — the component file, next to the rendering logic
  that depends on it.

## ToolMessage specifics

`ToolMessage` is the most involved component. It encapsulates three
previously scattered behaviors:

1. **Header construction** (`toolHeader`) — translates `(toolName, toolArgs)`
   into a display label: `task` calls become `sub-agent: <role> — <desc>`,
   `webfetch` becomes `web_fetch → <url>`, `bash` becomes `bash — <args>`.
2. **Running/completed icon** — `⏳` while executing, `✓` when done.
3. **Truncation budget** — expanded output is wrapped to the inner box
   width and capped at `viewport.Height()/3` (clamped 4–24 lines), with a
   `··· N more lines above ···` notice when truncated.

## ExpandableSection and zones

Expandable content (reasoning blocks) uses `ExpandableSection`, which wraps
its toggle header in `zone.Mark(zoneID, …)`. The zone ID is **generated by
the caller** (from the message index) and registered in `m.reasoningZones` /
`m.toolZones` — the component only marks; the model keeps the registry used
by the mouse-click and hover handlers in `Update()`.

## Testing

`internal/tui/component_test.go` tests each component in isolation with
plain constructor arguments — no `Model`, no viewport, no agent goroutine.
This is the property the whole refactor exists for: e.g.
`TestToolMessage_Header` table-tests 7 header variants that were previously
untestable inline code.

Model-level behavior (message handling, mode transitions, key dispatch)
remains covered by `tui_test.go`.

## Contracts & conventions

These conventions keep the component set visually consistent. Follow them
when adding components.

### Block vs fragment

- **Block components** self-terminate: `Render()` output ends with exactly
  one `\n`. All message components except `AssistantMessage` are blocks.
- **Fragment components** return content with no trailing newline;
  the caller owns line breaks. `AssistantMessage` is a fragment because it
  composes with reasoning sections and streaming output.

### Shared shapes

- **Chat bubble** — user messages, system messages, and expandable-section
  content all render through `chatBubble(content, width, fg, bg)`:
  word-wrapped foreground on a width-constrained background. Never hand-roll
  `bg.Width(w).Render(fg.Render(chatWrap(...)))`.
- **Pre-wrapped content** — glamour-rendered markdown (assistant responses,
  reasoning text) must NOT go through `chatBubble`: `chatWrap` collapses
  code-block indentation and splits ANSI escape runs. Render it directly
  (`fg.Render(content)` on a bg style) or, for `ExpandableSection`, chain
  `.AsPreWrapped()`.
- **Scroll window** — scrollable palettes compute their visible range with
  `scrollWindow(selected, maxVisible, total)`, centered on the selection
  and clamped to both ends.

### Palette chrome

Every palette renders inside `commandPaletteStyle.Width(w)` — a rounded
border constrained to the terminal width. Inside the box:

- **Titles** use `paletteTitleStyle` (bold, `PaletteTitle` theme token):
  palette headers, provider headings, help-overlay group titles.
- **Selection** uses ` ▶ ` (with matching 3-space pad on unselected rows);
  multi-select rows use ` ☑ ` / ` ☐ ` in the same slot.
- **Overflow** shows `commandDescStyle.Render("  (N-M of K)")` when the
  list is scrolled; `paletteLines()` in `tui.go` must account for the
  extra line when computing palette height.
- **Empty state** follows the mode's intent: transient typing aids
  (`CommandPalette`) return `""` to hide entirely; explicit modals
  (`ModelPalette`) show a boxed "No matching …" notice.

### Theme tokens

Components never hardcode colors or build styles at render time. Colors
come from `Theme` fields in `theme.go`, applied by `ApplyTheme()`:

| Style var | Theme token | Used for |
|---|---|---|
| `paletteTitleStyle` | `PaletteTitle` | Palette headers, provider/group headings |
| `noticeStyle` | `Notice` | Ephemeral status line (auto-clearing notices) |

Geometry clamps use the Go builtin `max()` (e.g. `max(width-4, 20)`).

### Embedded content

`TodoTable` is rendered **inside** a `ToolMessage`'s bordered box, not as
standalone chrome: the `todowrite` tool result renders the todo list as a
borderless table at the call site, expanding while the tool runs and
collapsing on completion like any other tool output. Two rules follow:

- **Borderless** — the `ToolMessage` box provides the border; embedded
  tables must use `Border(lipgloss.Border{})` so no nested box appears.
- **Fit the inner width** — the component receives `m.width - 8` (the
  box's inner content width) and truncates cell content so the box's
  line wrapping never mangles table rows.

The todo snapshot flows from `TodoWriteTool.OnWrite` → `AgentMsg.Todos` →
`m.todos` ahead of the tool result message (channel-ordered, same
goroutine), so the table always reflects the list as written by that call.

## Extending

- **New message role**: create a component struct in `message_component.go`
  (or its own file), add one `case` in `renderMessages()`.
- **New palette**: create a component in `palette_component.go`, add one
  branch in `View()` and, if it consumes vertical space, a sizing case in
  `paletteLines()`.
- **New theme**: add a `Theme` value in `theme.go` and register it in
  `namedThemes`. No component changes — colors flow through the shared
  style variables.

## Non-goals

- Replacing bubbletea widgets (viewport, textinput, spinner, help) with
  components. They are stateful widgets, not renderers.
- A component tree or layout engine. `View()` composes a fixed vertical
  stack; there is no generic composition API because the TUI has exactly
  one layout.
