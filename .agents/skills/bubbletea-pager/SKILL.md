---
name: bubbletea-pager
description: Bubble Tea v2 full-screen pager/reader pattern — viewport with line-number gutter, regex search/highlight, header/footer chrome, mouse wheel support, and window resize handling. Use when building a terminal file viewer, log viewer, help browser, markdown reader, scrolling output display, or any full-screen TUI that displays scrollable content with line numbers and navigation. This is the canonical Bubble Tea v2 pager example from charmbracelet/bubbletea refactored as a reusable pattern. WHEN: pager, file viewer, log viewer, help browser, man page, reader, scroll viewport, line numbers gutter, viewport search highlight, full-screen text display, less-like pager, terminal file browser.
---

# Bubble Tea v2 Pager Pattern

A complete full-screen terminal pager built with Bubble Tea v2 and the Bubbles
viewport component. This skill captures the canonical Charmbracelet pager example
as a reusable pattern — not just component docs (see `charm-bubbles` for that).

> **Source.** Adapted from `charmbracelet/bubbletea/examples/pager/main.go`.
> Requires `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, and `charm.land/lipgloss/v2`.

## When to use this pattern

- Building a `less`-like file pager or help browser
- Scrolling log output, man pages, or markdown documents
- Any full-screen TUI with a scrollable content area, line numbers, and chrome
- Adding search/highlight to a viewport

For yaah specifically: this pattern complements the TUI's chat viewport. Use this
when building standalone reader/pager tools or adding line-number + search
capabilities to yaah's output views.

## Architecture

```
┌─────────────────────────────────────┐
│ headerView()                        │  ← title bar (lipgloss)
├─────────────────────────────────────┤
│    1 │ Content line one             │  ← left gutter (line numbers)
│    2 │ Content line two             │  ← viewport content
│    3 │ Content line three           │
│   ~ │                               │  ← empty lines show "~"
├─────────────────────────────────────┤
│ ──────────────────── 42%:0%        │  ← footer bar (scroll %)
└─────────────────────────────────────┘
```

## Imports

```go
import (
    "fmt"
    "regexp"
    "strings"

    "charm.land/bubbles/v2/viewport"
    tea "charm.land/bubbletea/v2"
    "charm.land/lipgloss/v2"
)
```

## Model

```go
type model struct {
    content  string
    ready    bool          // false until first WindowSizeMsg arrives
    viewport viewport.Model
}

func (m model) Init() tea.Cmd {
    return nil  // viewport initialized on first WindowSizeMsg
}
```

### Why lazy init on WindowSizeMsg?

The viewport needs actual terminal dimensions to size itself. The first
`tea.WindowSizeMsg` arrives async but fast. Set `m.ready = true` after
initializing the viewport so the `View()` method knows not to show a loading
spinner.

## View (three-zone layout)

```go
func (m model) View() tea.View {
    var v tea.View
    v.AltScreen = true                     // full alternate screen buffer
    v.MouseMode = tea.MouseModeCellMotion  // enables mouse wheel scrolling

    if !m.ready {
        v.SetContent("\n  Initializing...")
    } else {
        v.SetContent(fmt.Sprintf("%s\n%s\n%s",
            m.headerView(),
            m.viewport.View(),
            m.footerView(),
        ))
    }
    return v
}
```

### Header

```go
var titleStyle = func() lipgloss.Style {
    b := lipgloss.RoundedBorder()
    b.Right = "├"
    return lipgloss.NewStyle().BorderStyle(b).Padding(0, 1)
}()

func (m model) headerView() string {
    title := titleStyle.Render("Mr. Pager")
    line := strings.Repeat("─", max(0, m.viewport.Width()-lipgloss.Width(title)))
    return lipgloss.JoinHorizontal(lipgloss.Center, title, line)
}
```

The border trick: set `b.Right = "├"` so the title box joins seamlessly with
the horizontal rule. This creates a clean "title ├─────" look.

### Footer

```go
var infoStyle = func() lipgloss.Style {
    b := lipgloss.RoundedBorder()
    b.Left = "┤"
    return titleStyle.BorderStyle(b)
}()

func (m model) footerView() string {
    info := infoStyle.Render(fmt.Sprintf("%3.f%%:%3.f%%",
        m.viewport.ScrollPercent()*100,
        m.viewport.HorizontalScrollPercent()*100,
    ))
    line := strings.Repeat("─", max(0, m.viewport.Width()-lipgloss.Width(info)))
    return lipgloss.JoinHorizontal(lipgloss.Center, line, info)
}
```

Same border trick in reverse: `b.Left = "┤"` gives "─────┤ 42%".

## Update — window sizing

```go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    var cmd tea.Cmd
    var cmds []tea.Cmd

    switch msg := msg.(type) {
    case tea.KeyPressMsg:
        if k := msg.String(); k == "ctrl+c" || k == "q" || k == "esc" {
            return m, tea.Quit
        }

    case tea.WindowSizeMsg:
        headerHeight := lipgloss.Height(m.headerView())
        footerHeight := lipgloss.Height(m.footerView())
        verticalMarginHeight := headerHeight + footerHeight

        if !m.ready {
            m.viewport = viewport.New(
                viewport.WithWidth(msg.Width),
                viewport.WithHeight(msg.Height-verticalMarginHeight),
            )
            m.viewport.YPosition = headerHeight
            // ... gutter + highlights setup
            m.viewport.SetContent(m.content)
            m.ready = true
        } else {
            m.viewport.SetWidth(msg.Width)
            m.viewport.SetHeight(msg.Height - verticalMarginHeight)
        }
    }

    m.viewport, cmd = m.viewport.Update(msg)
    cmds = append(cmds, cmd)
    return m, tea.Batch(cmds...)
}
```

**Key points:**
- Always forward `msg` to `m.viewport.Update(msg)` — the viewport handles its
  own keybindings (up/down, page up/down, home/end, mouse wheel).
- Subtract header + footer height from the viewport height.
- Use `YPosition` to offset the viewport below the header.

## Left gutter — line numbers

```go
m.viewport.LeftGutterFunc = func(info viewport.GutterContext) string {
    if info.Soft {
        return "     │ "      // soft-wrapped continuation line
    }
    if info.Index >= info.TotalLines {
        return "   ~ │ "      // beyond end of content (vim-style)
    }
    return fmt.Sprintf("%4d │ ", info.Index+1)
}
```

The gutter function receives a `GutterContext`:
- `info.Index` — line index (0-based)
- `info.TotalLines` — total content lines
- `info.Soft` — true if this is a soft-wrapped continuation

## Search and highlight

```go
// Style for highlight matches
m.viewport.HighlightStyle = lipgloss.NewStyle().
    Foreground(lipgloss.Color("238")).
    Background(lipgloss.Color("34"))   // green bg

// Style for the *currently selected* match
m.viewport.SelectedHighlightStyle = lipgloss.NewStyle().
    Foreground(lipgloss.Color("238")).
    Background(lipgloss.Color("47"))   // brighter green bg

// Find all matches and highlight them
m.viewport.SetHighlights(
    regexp.MustCompile("artichoke").FindAllStringIndex(m.content, -1),
)
m.viewport.HighlightNext()  // jump to first match
```

Navigation between highlights uses:
- `m.viewport.HighlightNext()`
- `m.viewport.HighlightPrevious()`

## Main — wiring it up

```go
func main() {
    content, err := os.ReadFile("artichoke.md")
    if err != nil {
        fmt.Println("could not load file:", err)
        os.Exit(1)
    }

    p := tea.NewProgram(model{content: string(content)})
    if _, err := p.Run(); err != nil {
        fmt.Println("could not run program:", err)
        os.Exit(1)
    }
}
```

## Common customizations

### Soft wrap

```go
m.viewport.SoftWrap = true
```

### Custom keybindings beyond the defaults

Add to the `tea.KeyPressMsg` switch:

```go
case tea.KeyPressMsg:
    switch msg.String() {
    case "ctrl+c", "q", "esc":
        return m, tea.Quit
    case "g":
        m.viewport.GotoTop()
        return m, nil
    case "G":
        m.viewport.GotoBottom()
        return m, nil
    case "n":
        m.viewport.HighlightNext()
        return m, nil
    case "N":
        m.viewport.HighlightPrevious()
        return m, nil
    }
```

### Dynamic content updates

```go
m.viewport.SetContent(newContent)
// Preserve highlight positions if needed:
re := regexp.MustCompile("pattern")
m.viewport.SetHighlights(re.FindAllStringIndex(newContent, -1))
```

### Setting content line-by-line (streaming)

```go
// Append a line:
m.viewport.SetContentLines(append(m.viewport.GetContentLines(), newLine))

// Or build and set:
m.viewport.SetContent(m.viewport.GetContent() + "\n" + chunk)
```

## V1 → V2 gotchas

| v1 (wrong) | v2 (correct) |
|---|---|
| `case tea.KeyMsg:` | `case tea.KeyPressMsg:` |
| `tea.EnterAltScreen` (cmd) | `v.AltScreen = true` (field) |
| `tea.WithAltScreen()` (option) | `v.AltScreen = true` (field) |
| `tea.WithMouseCellMotion()` | `v.MouseMode = tea.MouseModeCellMotion` |
| `p.Start()` | `p.Run()` |
| `vp.HighPerformanceRendering` | Removed — just delete it |
| `vp.Width`, `vp.Height` (fields) | `vp.SetWidth()`, `vp.SetHeight()` |
| `viewport.Model.Width` (exported) | `vp.Width()` (getter) |

## Integration with yaah

yaah's TUI already uses a viewport for chat messages in `internal/tui/tui.go`.
The pager pattern adds:

1. **Line-number gutter** — useful for log output, code display, or debug views
2. **Search/highlight** — could power `Ctrl+F` search within yaah's message history
3. **Header/footer chrome** — reusable pattern for any full-screen tool view
4. **AltScreen usage** — when yaah wants a dedicated full-screen mode for a
   specific output (e.g., `yaah skill show <name>` as a pager)

When adding pager features to yaah, prefer importing this skill *alongside*
`charm-bubbles` (for component API details) and `charm-bubbletea` (for the
framework lifecycle).

