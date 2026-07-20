---
name: charm-bubbles
description: Charm Bubbles v2 — TUI primitive components for Bubble Tea v2 in the yaah TUI. Load when adding or modifying any bubbles component in yaah's TUI (spinner, text input, textarea, viewport, list, table, progress, paginator, timer, stopwatch, help, key, filepicker, cursor), or when migrating v1 bubbles code to v2. v1 patterns (tea.KeyMsg, exported Width/Height fields, DefaultKeyMap vars, NewModel aliases, AdaptiveColor, github.com/charmbracelet/bubbles imports) are all wrong for yaah — use this skill to get the v2 API right.
---

# Charm Bubbles v2 — TUI Components for yaah

Bubble Tea v2 TUI primitives. yaah's M7 TUI imports this library — use these
patterns, not the v1 patterns you'll find in older blog posts or LLM training
data.

> **Source of truth.** The repo is at
> `C:\Code\Personal\yaah\.scratch\repos\bubbles\` (already cloned) and on
> GitHub at `charmbracelet/bubbles`. When in doubt, read the component's
> source file (e.g. `textinput/textinput.go`) — it's the canonical reference.
> The upgrade guide at `UPGRADE_GUIDE_V2.md` is the **most useful single
> document** for yaah work because yaah's TUI is being built fresh on v2.

## Module and import path (v2)

```go
import (
    "charm.land/bubbles/v2"                        // root (empty package)
    "charm.land/bubbles/v2/cursor"
    "charm.land/bubbles/v2/help"
    "charm.land/bubbles/v2/key"
    "charm.land/bubbles/v2/list"
    "charm.land/bubbles/v2/paginator"
    "charm.land/bubbles/v2/progress"
    "charm.land/bubbles/v2/spinner"
    "charm.land/bubbles/v2/stopwatch"
    "charm.land/bubbles/v2/table"
    "charm.land/bubbles/v2/textarea"
    "charm.land/bubbles/v2/textinput"
    "charm.land/bubbles/v2/timer"
    "charm.land/bubbles/v2/viewport"
    "charm.land/bubbles/v2/filepicker"
)
```

**Required companion modules** (bump together):

```bash
go get charm.land/bubbletea/v2@latest
go get charm.land/bubbles/v2@latest
go get charm.land/lipgloss/v2@latest
```

`runeutil` and `memoization` are now **internal** — not importable.

## Components at a glance

| Component    | Constructor                       | Use it for                                     |
|--------------|-----------------------------------|------------------------------------------------|
| `spinner`    | `spinner.New()`                   | "Working…" indicator. yaah's REPL already has one — drop this in. |
| `textinput`  | `textinput.New()`                 | Single-line prompt. **Use in yaah's REPL/chat input.** |
| `textarea`   | `textarea.New()`                  | Multi-line input. Drop in for `yaah tui`'s chat box. |
| `viewport`   | `viewport.New()` + `SetWidth/Height` | Scrollable region. **Use for streaming agent output.** |
| `list`       | `list.New(items, delegate, width, height)` | Selectable list (slash commands, sessions, skills). |
| `table`      | `table.New()` + `WithRows/WithColumns` | Tabular data. Sessions list, memory entries. |
| `progress`   | `progress.New(opts...)`           | Tool-call progress, long task indicator. |
| `paginator`  | `paginator.New()`                 | Pagination dots / numbers. Pair with viewport. |
| `timer`      | `timer.New(timeout)`              | Countdown. |
| `stopwatch`  | `stopwatch.New()`                 | Elapsed-time counter. |
| `help`       | `help.New()`                      | Auto-generated keybinding help bar. |
| `key`        | (package)                         | `key.Binding`, `key.Matches`, `key.NewBinding`. |
| `filepicker` | `filepicker.New()`                | File picker. |
| `cursor`     | (advanced)                        | Virtual cursor (mostly used by `textinput`/`textarea` internally now). |

## V1 → V2 breaking changes yaah devs hit

These trip up LLM-generated code. Memorize the "After" column.

### Global patterns (apply first)

| v1 (wrong for yaah)             | v2 (correct)                              |
|---------------------------------|-------------------------------------------|
| `case tea.KeyMsg:`              | `case tea.KeyPressMsg:`                   |
| `import "github.com/charmbracelet/bubbles/..."` | `import "charm.land/bubbles/v2/..."` |
| `m.Width = 40; _ = m.Width`     | `m.SetWidth(40); _ = m.Width()`           |
| `m.Height = 20; _ = m.Height`   | `m.SetHeight(20); _ = m.Height()`         |
| `km := textinput.DefaultKeyMap` (var) | `km := textinput.DefaultKeyMap()` (func) |
| `spinner.NewModel()`            | `spinner.New()`                           |
| `progress.FullColor = "#FF0000"` (string) | `progress.FullColor = lipgloss.Color("#FF0000")` (`color.Color`) |
| `vp.HighPerformanceRendering`   | **Removed.** Just don't set it.           |

Affected by "var → func" keymap rename: `paginator`, `textarea`, `textinput`.
Affected by "field → getter/setter": `filepicker`, `help`, `progress`, `table`,
`textinput`, `viewport`.

### Text input (`textinput`) — most-likely-first yaah component

```go
import (
    "charm.land/bubbles/v2/textinput"
    "charm.land/bubbles/v2/key"
    "charm.land/lipgloss/v2"
)

ti := textinput.New()
ti.Placeholder = "Type a message…"
ti.Prompt = "> "
ti.SetWidth(60)                    // not ti.Width = 60
ti.Focus()

// Styles (v2) live in a Styles struct:
s := textinput.DefaultStyles(isDark)   // isDark is the bool from the
                                      // BackgroundColorMsg pattern below
s.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
ti.SetStyles(s)

// In Update:
case tea.KeyPressMsg:                // NOT tea.KeyMsg
    switch {
    case key.Matches(msg, ti.KeyMap.Submit, key.NewBinding(key.WithKeys("enter"))):
        // user pressed Enter
    }
```

V1-era fields that **don't exist** in v2: `ti.PromptStyle`, `ti.TextStyle`,
`ti.PlaceholderStyle`, `ti.CompletionStyle`, `ti.CursorStyle`, `ti.Cursor` (as
a `cursor.Model`). They're all under `Styles.Focused.*` / `Styles.Blurred.*`
or accessed via `Styles()` / `SetStyles()`.

### Spinner

```go
import "charm.land/bubbles/v2/spinner"

s := spinner.New()                  // not spinner.NewModel()
// Optionally: s.Spinner = spinner.Dot  (or Line, MiniDot, Jump, Pulse, Points, Globe, Moon, Monkey, Meter, Hamburger, Ellipsis)

case tea.KeyPressMsg: ...            // when key turns spinner off
case spinner.TickMsg:
    var cmd tea.Cmd
    s, cmd = s.Update(msg)
    return s, cmd
```

Start it from `Init()`:

```go
func (m model) Init() tea.Cmd {
    return m.spinner.Tick()          // v2: method, not package func
}
```

### Viewport (streaming agent output)

```go
import "charm.land/bubbles/v2/viewport"

vp := viewport.New()
vp.SetWidth(80)
vp.SetHeight(24)

// On stream chunk:
vp.SetContentLines(append(vp.GetContentLines(), line))
// or:
vp.SetContent(vp.GetContent() + "\n" + chunk)
```

**Removed:** `vp.HighPerformanceRendering`. Just delete it.

**New and useful** for yaah:

- `vp.SoftWrap = true` — wraps long agent output without horizontal scroll.
- `vp.SetHighlights([][]int{...}); vp.HighlightNext(); vp.HighlightPrevious()` — for search-in-output.
- `vp.LeftGutterFunc = func(viewport.GutterContext) string { ... }` — line numbers.
- `vp.SetContentLines([]string)` — set lines directly (works with soft-wrap).

### Key bindings (`key` package)

```go
import "charm.land/bubbles/v2/key"

type KeyMap struct {
    Submit key.Binding
    Cancel key.Binding
}

var DefaultKeyMap = KeyMap{
    Submit: key.NewBinding(
        key.WithKeys("enter"),
        key.WithHelp("enter", "submit"),
    ),
    Cancel: key.NewBinding(
        key.WithKeys("esc", "ctrl+c"),
        key.WithHelp("esc", "cancel"),
    ),
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyPressMsg:                       // NOT tea.KeyMsg
        switch {
        case key.Matches(msg, DefaultKeyMap.Submit):
            // …
        case key.Matches(msg, DefaultKeyMap.Cancel):
            return m, tea.Quit
        }
    }
    return m, nil
}
```

`Matches` is generic over any `fmt.Stringer` (works with `tea.KeyPressMsg`
because it implements `String() string`).

## Light/dark styles (REQUIRED in v2)

Lip Gloss v2 removed `AdaptiveColor`. Bubbles no longer auto-detect background.
You **must** pass an explicit `isDark bool` to the `DefaultStyles(isDark)`
functions on `help`, `list`, `textarea`, `textinput`, etc.

**Canonical pattern** (recommended — works over SSH/Wish):

```go
func (m model) Init() tea.Cmd {
    return tea.RequestBackgroundColor
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.BackgroundColorMsg:
        isDark := msg.IsDark()
        m.input.SetStyles(textinput.DefaultStyles(isDark))
        m.help.Styles = help.DefaultStyles(isDark)
        m.list.Styles = list.DefaultStyles(isDark)
        // …apply to every styled component
        return m, nil
    }
    return m, nil
}
```

**Quick pattern** (one-shot, not SSH-safe — fine for local TUI):

```go
import "charm.land/lipgloss/v2/compat"

var isDark = compat.HasDarkBackground()
```

**Manual** (force a theme regardless of terminal):

```go
h.Styles = help.DefaultDarkStyles()   // or DefaultLightStyles()
```

For yaah specifically: since yaah TUI runs locally on a terminal, the
`compat.HasDarkBackground()` shortcut is acceptable as a default. The
`tea.RequestBackgroundColor` pattern is the right long-term answer.

## Embedding a component in yaah's TUI

yaah's TUI structure is `internal/tui/`. Each component instance lives as a
field on a top-level model. Pattern:

```go
type model struct {
    spinner spinner.Model
    input   textinput.Model
    output  viewport.Model
    help    help.Model
    keys    KeyMap
    isDark  bool
}

func newModel() model {
    ti := textinput.New()
    ti.Placeholder = "Ask yaah…"
    ti.Prompt = "> "
    ti.SetWidth(80)

    return model{
        spinner: spinner.New(),
        input:   ti,
        output:  viewport.New(),
        keys:    DefaultKeyMap,
    }
}

func (m model) Init() tea.Cmd {
    return tea.Batch(m.spinner.Tick(), m.input.Focus(), tea.RequestBackgroundColor)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    var cmds []tea.Cmd
    switch msg := msg.(type) {
    case tea.BackgroundColorMsg:
        m.isDark = msg.IsDark()
        m.input.SetStyles(textinput.DefaultStyles(m.isDark))
    case tea.KeyPressMsg:                     // NOT tea.KeyMsg
        switch {
        case key.Matches(msg, m.keys.Quit):
            return m, tea.Quit
        case key.Matches(msg, m.keys.Submit):
            v := m.input.Value()
            m.input.Reset()
            return m, m.send(v)               // yaah-specific
        }
    case spinner.TickMsg:
        var cmd tea.Cmd
        m.spinner, cmd = m.spinner.Update(msg)
        cmds = append(cmds, cmd)
    }
    // Forward to child components:
    var cmd tea.Cmd
    m.input, cmd = m.input.Update(msg)
    cmds = append(cmds, cmd)
    m.output, cmd = m.output.Update(msg)
    cmds = append(cmds, cmd)
    return m, tea.Batch(cmds...)
}
```

## Common pitfalls

1. **`tea.KeyMsg` is wrong** — Bubble Tea v2 renamed it. Compiler will error
   on the type switch, so easy to catch, but LLMs love to write it.
2. **`github.com/charmbracelet/bubbles` import path is wrong** — must be
   `charm.land/bubbles/v2`. Search-and-replace:
   `github.com/charmbracelet/bubbles/  →  charm.land/bubbles/v2/`
3. **`NewModel()` aliases are gone** — use `New()`.
4. **`vp.HighPerformanceRendering` is removed** — just delete the line.
5. **`progress.FullColor = "#FF0000"` won't compile** — wrap in
   `lipgloss.Color(...)` because the field is now `color.Color`.
6. **Forgetting `isDark`** — components will render with default styles
   (usually dark) regardless of terminal background. Always wire up
   `tea.RequestBackgroundColor` in `Init()`.
7. **Don't paste Bubble Tea v1 examples** — even the official `bubbletea`
   README has v1 patterns in older commits. Trust the v2 repo
   (`UPGRADE_GUIDE_V2.md` + component source files) over anything you remember
   from a tutorial.
8. **Embedded v1 patterns in `examples/`** — Bubbles v1 example code in
   blog posts uses `lipgloss.AdaptiveColor` and exported Width/Height fields.
   Convert with the table above.

## Verification checklist (before shipping a TUI change)

- [ ] `gofmt -l .` is empty
- [ ] `go vet ./...` is clean
- [ ] `go build .` succeeds
- [ ] No `github.com/charmbracelet/bubbles` imports remain (only `charm.land/bubbles/v2`)
- [ ] No `tea.KeyMsg` remains (only `tea.KeyPressMsg`)
- [ ] No `NewModel(` calls remain (only `New(`)
- [ ] No `m.Width =` or `m.Height =` assignments on bubbles components
- [ ] `tea.RequestBackgroundColor` is wired up in `Init()` (or
      `compat.HasDarkBackground()` is used as a documented shortcut)
- [ ] Run `go test ./internal/tui/...` if a test exists
- [ ] Manually: `yaah tui` — confirm spinner animates, input focuses,
      viewport scrolls, Ctrl+C quits, styles look right in both light and
      dark terminals (`yaah tui` in `cmd.exe` and Windows Terminal)

## When this skill should fire

- "Add a spinner to yaah's REPL"
- "Make yaah's input field support multiline"
- "Show the agent's streaming output in a scrollable region"
- "Build a slash-command picker for yaah"
- "Migrate this bubbles code from v1 to v2"
- "yaah tui table component"
- Any task touching `internal/tui/`

## When NOT to load this

- Working on the REPL (non-TUI) — uses raw readline-style input
- Pure provider/tool/agent work — bubbles is TUI-only
- Touching `internal/tui/`'s parent (`cmd/yaah/`) — just for the entry point
