---
name: charm-bubbletea
description: Charm Bubble Tea v2 — the Elm-style TUI framework that powers yaah's `yaah tui` command. Load when working on `internal/tui/tui.go` (the `Model` with `Init/Update/View` methods), adding a new message type, wiring up a new keybinding, debugging input/render/resize behavior, or migrating v1 bubbletea code to v2. v1 patterns (View() string, tea.KeyMsg struct, tea.WithAltScreen option, case " " for space, tea.EnterAltScreen command, p.Start() method, github.com/charmbracelet/bubbletea import) are all wrong for yaah — use this skill to get the v2 API right.
---

# Charm Bubble Tea v2 — TUI framework for yaah

The Elm-architecture TUI framework. yaah's TUI (`internal/tui/`) is built
on this; bubbletea is also the parent of the `charm-bubbles` and
`charm-glamour` skills.

> **Source of truth.** Repo at
> `C:\Code\Personal\yaah\.scratch\repos\bubbletea\` (already cloned) and on
> GitHub at `charmbracelet/bubbletea`. `UPGRADE_GUIDE_V2.md` in the repo is
> the single best doc — it covers every v1→v2 break in 573 lines.
>
> **Layered with ultraviolet.** Bubble Tea v2 sits on top of
> `github.com/charmbracelet/ultraviolet` (cell-based renderer, input
> handling). `tea.Msg` is literally `uv.Event`. You almost never need to
> import ultraviolet directly — load the `charm-ultraviolet` skill only
> if you're writing custom terminal code below bubbletea.

## Current yaah usage (read this first)

`internal/tui/tui.go` is the canonical v2 program in this repo. Pattern is
correct end-to-end — copy and extend it, don't reinvent:

```go
// cmd/yaah/tui.go:137 — Program construction
m := tui.New(providerName, modelName, …)
p := tea.NewProgram(m)
go func() { for msg := range agentCh { p.Send(tui.AgentMsg{…}) } }()
if _, err := p.Run(); err != nil { return fmt.Errorf("TUI error: %w", err) }
```

```go
// internal/tui/tui.go:783-786 — Init
func (m *Model) Init() tea.Cmd {
    return tea.Batch(textinput.Blink, m.spinner.Tick)
}
```

```go
// internal/tui/tui.go:788-845 — Update (excerpt; full is ~225 lines)
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    var cmd tea.Cmd
    switch msg := msg.(type) {
    case AgentMsg:
        m.HandleAgentMsg(msg)
        return m, nil
    case spinner.TickMsg:
        var spinCmd tea.Cmd
        m.spinner, spinCmd = m.spinner.Update(msg)
        if m.thinking { m.refreshViewport() }
        return m, spinCmd
    case tea.WindowSizeMsg:
        m.width = msg.Width; m.height = msg.Height
        m.input.SetWidth(msg.Width - 4)
        m.createRenderer()             // re-creates glamour with new wrap width
        m.reRenderMessages()
        // …resize viewport, refresh
        return m, nil
    case tea.MouseMsg:
        var vpCmd tea.Cmd
        m.viewport, vpCmd = m.viewport.Update(msg)
        return m, vpCmd
    case tea.KeyPressMsg:
        switch msg.String() {
        case "ctrl+c", "esc":
            if m.onQuit != nil { m.onQuit() }
            return m, tea.Quit
        case "ctrl+y":
            // copy markdown to clipboard
            return m, tea.Batch(
                tea.SetClipboard(m.messages[i].Raw),
                tea.Tick(2*time.Second, func(time.Time) tea.Msg {
                    return clearCopyFlashMsg{}
                }),
            )
        }
    }
    // Forward unhandled messages to child components:
    m.input, cmd = m.input.Update(msg)
    m.viewport, cmd = m.viewport.Update(msg)
    return m, cmd
}
```

```go
// internal/tui/tui.go:1014-1061 — View
func (m *Model) View() tea.View {
    if m.width == 0 { return tea.NewView("Initializing...") }
    // …build header, status, viewport, input
    v := tea.NewView(body)
    v.AltScreen = true
    if !m.input.VirtualCursor() {
        if c := m.input.Cursor(); c != nil {
            c.Y = m.height - 1
            v.Cursor = c
        }
    }
    return v
}
```

Note what's correct here: `View() tea.View` (not string), `v.AltScreen`
declared in `View()` (not `tea.WithAltScreen()`), `case tea.KeyPressMsg`
(not `tea.KeyMsg`), `tea.MouseMsg` (interface), `tea.SetClipboard`,
`tea.Tick`, `p.Run()`. All v2.

## Module and import path (v2)

```go
import tea "charm.land/bubbletea/v2"
```

```bash
go get charm.land/bubbletea/v2@latest
go get charm.land/lipgloss/v2@latest
```

## The big idea: declarative View (v2's headline change)

In v1, terminal features lived as **program options** (`tea.WithAltScreen()`)
or **imperative commands** (`tea.EnterAltScreen`). In v2, they live as
**fields on the `tea.View` struct** returned by `View()`. Just declare what
you want; bubbletea handles the rest.

```go
func (m model) View() tea.View {
    v := tea.NewView("Hello!")
    v.AltScreen = true
    v.MouseMode = tea.MouseModeCellMotion
    v.ReportFocus = true
    v.WindowTitle = "yaah"
    v.Cursor = tea.NewCursor(10, 5)        // or nil to hide
    v.ForegroundColor = lipgloss.Color("#FFFFFF")
    v.BackgroundColor = lipgloss.Color("#000000")
    v.ProgressBar = uv.NewProgressBar(uv.ProgressBarDefault, 50)
    v.KeyboardEnhancements = &tea.KeyboardEnhancements{
        ReportEventTypes: true,            // key release events
    }
    return v
}
```

| View field                         | What it does                                |
|------------------------------------|---------------------------------------------|
| `Content` (via `NewView`/`SetContent`) | The rendered string                     |
| `AltScreen`                        | Enter/exit alternate screen buffer           |
| `MouseMode`                        | `MouseModeNone` / `MouseModeCellMotion` / `MouseModeAllMotion` |
| `ReportFocus`                      | Enable focus/blur event reporting            |
| `DisableBracketedPasteMode`        | Disable bracketed paste                      |
| `WindowTitle`                      | Set terminal window title                   |
| `Cursor`                           | `*tea.Cursor` (position, shape, color, blink) |
| `ForegroundColor` / `BackgroundColor` | Terminal-wide colors                      |
| `ProgressBar`                      | `*uv.ProgressBar` (OS taskbar / tab bar)    |
| `KeyboardEnhancements`             | Kitty keyboard protocol flags               |
| `OnMouse`                          | Mouse intercept based on view content       |

## Messages and `tea.Msg`

`tea.Msg` is `uv.Event` (an interface). You type-switch on concrete
message types. Common types in yaah's TUI:

| Message type                | When it fires                                |
|-----------------------------|----------------------------------------------|
| `tea.KeyPressMsg`           | Any key press. `msg.String()`, `msg.Code`, `msg.Text`, `msg.Mod`, `msg.IsRepeat()`. |
| `tea.KeyReleaseMsg`         | Key release (only if `KeyboardEnhancements.ReportEventTypes`). |
| `tea.MouseMsg`              | Mouse event interface. Type-switch: `MouseClickMsg`, `MouseReleaseMsg`, `MouseWheelMsg`, `MouseMotionMsg`. Call `msg.Mouse()` for `X, Y, Button, Mod`. |
| `tea.WindowSizeMsg`         | Terminal resize. `msg.Width`, `msg.Height`.  |
| `tea.BackgroundColorMsg`    | Background query result. `msg.IsDark()`.     |
| `tea.PasteMsg`              | Pasted text. `msg.Content`.                 |
| `tea.PasteStartMsg` / `tea.PasteEndMsg` | Paste boundary events              |
| `tea.FocusMsg` / `tea.BlurMsg` | Focus change (only if `ReportFocus`).    |
| `tea.ClipboardMsg`          | Result of `tea.RequestClipboard`.            |
| `spinner.TickMsg`           | Bubbles spinner tick (yaah's TUI uses this). |
| `AgentMsg` (yaah-defined)   | Internal: messages from the agent goroutine. |

### Key events in v2

`tea.KeyMsg` is an **interface** in v2 (was a struct). For most code use
`tea.KeyPressMsg`. To handle both presses and releases:

```go
case tea.KeyMsg:
    switch key := msg.(type) {
    case tea.KeyPressMsg:   // …
    case tea.KeyReleaseMsg: // …
    }
```

Key fields and methods:

| Field / method             | Notes                                                |
|----------------------------|------------------------------------------------------|
| `msg.Code`                 | `rune` — `tea.KeyEnter`, `'a'`, etc.                 |
| `msg.Text`                 | `string` (was `[]rune` in v1)                        |
| `msg.Mod`                  | Modifier set: `msg.Mod.Contains(tea.ModAlt)`, `tea.ModCtrl`, `tea.ModShift`, `tea.ModMeta` |
| `msg.String()`             | `"a"`, `"ctrl+c"`, `"up"`, `"space"`, etc.           |
| `msg.IsRepeat()`           | Auto-repeating key                                   |
| `msg.ShiftedCode`          | The shifted key code (`'B'` for shift+b)             |
| `msg.BaseCode`             | US PC-101 layout code (for international keyboards) |

**Space is now `"space"`** in `String()`, not `" "`:

```go
case "space":  // not " "
```

### Ctrl+key matching

v1's `tea.KeyCtrlC` etc. are gone. Use either:

```go
// String matching:
switch msg.String() {
case "ctrl+c": // …
}

// Or field matching:
if msg.Code == 'c' && msg.Mod == tea.ModCtrl { // … }
```

## Commands

`tea.Cmd` is `func() tea.Msg`. yaah uses these directly:

| Command                      | Purpose                                          |
|------------------------------|--------------------------------------------------|
| `tea.Quit`                   | End the program.                                 |
| `tea.Batch(cmds...)`         | Run commands concurrently; returns one combined. |
| `tea.Sequence(cmds...)`      | Run commands sequentially. (v2 rename; was `tea.Sequentially`.) |
| `tea.Tick(d, fn)`            | Send `fn(time.Time)` after duration `d`.         |
| `tea.SetClipboard(s)`        | Set clipboard to string `s`. Returns success/fail `tea.Msg`. |
| `tea.RequestClipboard`       | Ask terminal for current clipboard.              |
| `tea.RequestBackgroundColor` | Ask terminal for background. Returns `tea.BackgroundColorMsg`. |
| `tea.RequestWindowSize`      | Ask terminal for current size. (v2: returns `tea.Msg` directly, not a `Cmd`.) |
| `textinput.Blink`            | Bubbles: tick the textinput cursor blink.        |
| `spinner.Model.Tick()`       | Bubbles: tick the spinner (v2: method, not package func). |
| `m.spinner.Update(msg)`      | Bubbles: return a command that advances the spinner. |

Sending messages into a running program from a goroutine: `p.Send(msg)`.
Used in `cmd/yaah/tui.go` to feed `tui.AgentMsg` from the agent loop.

## Program options (v2)

```go
p := tea.NewProgram(model{},
    tea.WithAltScreen(),                    // REMOVED in v2 — use View().AltScreen
    tea.WithMouseCellMotion(),              // REMOVED in v2 — use View().MouseMode
    // v2-only options:
    tea.WithColorProfile(p),                // force a color profile (testing)
    tea.WithWindowSize(w, h),               // force initial window size (testing)
    tea.WithFPS(60),                        // cap redraw rate
    tea.WithOutput(io.Writer),              // override output
    tea.WithInput(io.Reader),               // override input
    tea.WithoutSignals(),                   // don't install signal handlers
    tea.WithFilter(func(tea.Model, tea.Msg) tea.Msg { … }),  // intercept msgs
)
```

`p.Run()` (was `p.Start()` in v1) blocks until the program quits.

## V1 → V2 breaking changes (the table LLMs get wrong)

### Imports
| v1                                            | v2                                |
|-----------------------------------------------|-----------------------------------|
| `import tea "github.com/charmbracelet/bubbletea"` | `import tea "charm.land/bubbletea/v2"` |
| `import "github.com/charmbracelet/lipgloss"`   | `import "charm.land/lipgloss/v2"` |

### Model interface
| v1                | v2                  |
|-------------------|---------------------|
| `View() string`   | `View() tea.View`   |

### Key events
| v1                       | v2                                            |
|--------------------------|-----------------------------------------------|
| `case tea.KeyMsg:` (struct) | `case tea.KeyPressMsg:` (most code)        |
| `msg.Type`               | `msg.Code` (rune)                             |
| `msg.Runes`              | `msg.Text` (string)                           |
| `msg.Alt`                | `msg.Mod.Contains(tea.ModAlt)`                |
| `case " ":` (space)      | `case "space":`                               |
| `tea.KeyCtrlC` etc.      | `msg.String() == "ctrl+c"` or `msg.Code == 'c' && msg.Mod == tea.ModCtrl` |

### Mouse events
| v1                                | v2                                                  |
|-----------------------------------|-----------------------------------------------------|
| `case tea.MouseMsg:` (struct)     | `case tea.MouseMsg:` (interface)                    |
| `msg.X, msg.Y` (direct)           | `msg.Mouse().X, msg.Mouse().Y`                      |
| `msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft` | `case tea.MouseClickMsg: if msg.Button == tea.MouseLeft` |
| `tea.MouseButtonLeft`             | `tea.MouseLeft`                                     |
| `tea.MouseEvent` struct           | `tea.Mouse` struct                                  |

### Paste events
| v1                                     | v2                              |
|----------------------------------------|---------------------------------|
| `case tea.KeyMsg: if msg.Paste { … }`  | `case tea.PasteMsg: m.text += msg.Content` |
|                                        | `case tea.PasteStartMsg: {…}`   |
|                                        | `case tea.PasteEndMsg: {…}`     |

### Program options → View fields
| v1 option                      | v2 View field                              |
|--------------------------------|--------------------------------------------|
| `tea.WithAltScreen()`          | `view.AltScreen = true`                    |
| `tea.WithMouseCellMotion()`    | `view.MouseMode = tea.MouseModeCellMotion` |
| `tea.WithMouseAllMotion()`     | `view.MouseMode = tea.MouseModeAllMotion`  |
| `tea.WithReportFocus()`        | `view.ReportFocus = true`                  |
| `tea.WithoutBracketedPaste()`  | `view.DisableBracketedPasteMode = true`    |
| `tea.WithInputTTY()`           | Just remove — v2 always opens TTY for input |
| `tea.WithANSICompressor()`     | Just remove — new renderer auto-optimizes |

### Program commands → View fields
| v1 command                       | v2 View field                          |
|----------------------------------|----------------------------------------|
| `tea.EnterAltScreen` / `tea.ExitAltScreen` | `view.AltScreen = true/false` |
| `tea.EnableMouseCellMotion`      | `view.MouseMode = tea.MouseModeCellMotion` |
| `tea.EnableMouseAllMotion`       | `view.MouseMode = tea.MouseModeAllMotion`  |
| `tea.DisableMouse`               | `view.MouseMode = tea.MouseModeNone`       |
| `tea.HideCursor` / `tea.ShowCursor`     | `view.Cursor = nil` / `view.Cursor = &tea.Cursor{…}` |
| `tea.EnableBracketedPaste`       | `view.DisableBracketedPasteMode = false` |
| `tea.DisableBracketedPaste`      | `view.DisableBracketedPasteMode = true`  |
| `tea.EnableReportFocus` / `tea.DisableReportFocus` | `view.ReportFocus = true/false` |
| `tea.SetWindowTitle("…")`        | `view.WindowTitle = "…"`                |

### Program methods
| v1 method                        | v2                                       |
|----------------------------------|------------------------------------------|
| `p.Start()`                      | `p.Run()`                                |
| `p.StartReturningModel()`        | `p.Run()`                                |
| `p.EnterAltScreen()`             | `view.AltScreen = true` in `View()`      |
| `p.ExitAltScreen()`              | `view.AltScreen = false` in `View()`     |
| `p.EnableMouseCellMotion()` etc. | `view.MouseMode` in `View()`             |
| `p.SetWindowTitle(…)`            | `view.WindowTitle` in `View()`           |

### Other renames
| v1                      | v2                                       |
|-------------------------|------------------------------------------|
| `tea.Sequentially(…)`   | `tea.Sequence(…)`                        |
| `tea.WindowSize()`      | `tea.RequestWindowSize` (returns `Msg`, not `Cmd`) |

## yaah-specific patterns

### Sending messages from a goroutine (the agent loop)

In `cmd/yaah/tui.go:139-143`, the agent goroutine sends `tui.AgentMsg`
values into the program via `p.Send(msg)`. Inside `Update`, you type-switch
on `AgentMsg` like any other message. This is the **only** safe way to
communicate from outside the bubbletea event loop — never mutate `m`
directly from another goroutine.

```go
go func() {
    for msg := range agentCh {
        p.Send(tui.AgentMsg{Token: chunk, …})
    }
}()

// In Update:
case tui.AgentMsg:
    m.HandleAgentMsg(msg)
    return m, nil
```

### Resize handling

`tea.WindowSizeMsg` fires on terminal resize and on program start. Always
recompute layout-derived sizes (viewport width/height, input width, glamour
renderer wrap width) in this handler. yaah's TUI does this correctly at
`internal/tui/tui.go:809-822`.

### Cursor positioning

`textinput.Cursor()` returns a `*tea.Cursor` (Y is **relative to the
widget**, often 0). For multi-line layouts, set `c.Y` to the absolute row
in the view. yaah does this at `internal/tui/tui.go:1055-1060` (puts the
cursor on the input line, which is the last line of the view).

### Streaming output

Don't `tea.Quit` when the user submits; instead return a command that
starts the agent goroutine and let the goroutine stream `AgentMsg`s back
via `p.Send`. yaah's pattern: `onSubmit` callback kicks off the work, the
TUI model just displays the result.

## Common pitfalls

1. **`View() string` won't compile** — must be `View() tea.View`. Return
   `tea.NewView(content)` for the simple case.
2. **`tea.WithAltScreen()` won't compile** — set `view.AltScreen = true`
   in `View()`. Same for mouse, focus, paste, title.
3. **`tea.EnterAltScreen` etc. won't compile** — all moved to View fields.
4. **`case " ":` for space** — must be `case "space":`.
5. **`msg.Type` / `msg.Runes` / `msg.Alt`** — renamed to `msg.Code`,
   `msg.Text`, `msg.Mod`. Field/method form.
6. **`msg.X, msg.Y` on `tea.MouseMsg`** — `MouseMsg` is now an interface;
   call `msg.Mouse().X, msg.Mouse().Y`.
7. **`p.Start()`** — renamed to `p.Run()`.
8. **Trusting older tutorials** — almost all online bubbletea tutorials
   show v1 (string View, `KeyMsg` struct, `WithAltScreen`). Convert with
   the tables above. The official
   [`tutorials/`](https://github.com/charmbracelet/bubbletea/tree/main/tutorials)
   directory has been rewritten for v2.
9. **Mutating model from another goroutine** — use `p.Send(msg)`. Direct
   mutation is a data race.
10. **Setting `WithWordWrap` on glamour at construction** — when the
    terminal resizes, the renderer must be recreated. yaah does this in
    `createRenderer()`. See `charm-glamour` skill for details.
11. **Hardcoding "dark" glamour style** — yaah's `createRenderer` does
    this today (gap). For light-terminal support, wire
    `tea.RequestBackgroundColor` → `tea.BackgroundColorMsg` → recreate
    the renderer with the right style. See `charm-glamour` skill.

## Verification checklist

- [ ] `gofmt -l .` empty
- [ ] `go vet ./...` clean
- [ ] `go build .` succeeds
- [ ] `go test ./...` passes
- [ ] No `github.com/charmbracelet/bubbletea` imports (only
      `charm.land/bubbletea/v2`)
- [ ] No `View() string` (only `View() tea.View`)
- [ ] No `tea.WithAltScreen(` / `WithMouseCellMotion(` / `WithReportFocus(`
      / `WithoutBracketedPaste(` calls
- [ ] No `tea.EnterAltScreen` / `ExitAltScreen` / `EnableMouse…` /
      `DisableMouse` / `EnableBracketedPaste` / `DisableBracketedPaste` /
      `EnableReportFocus` / `DisableReportFocus` / `SetWindowTitle`
      commands
- [ ] No `p.Start()` (only `p.Run()`)
- [ ] No `case tea.KeyMsg:` (only `case tea.KeyPressMsg:` unless you
      also handle `KeyReleaseMsg`)
- [ ] No `msg.Type` / `msg.Runes` / `msg.Alt` field access
- [ ] No `case " ":` for space (use `case "space":`)
- [ ] `tea.WindowSizeMsg` is handled (re-layout, recreate glamour
      renderer, resize viewport)
- [ ] Goroutines communicate with the model via `p.Send(msg)` — no
      direct field mutation
- [ ] Manual: `yaah tui` — start, submit a message, scroll the
      viewport, resize the terminal, copy markdown with Ctrl+Y, quit
      with Ctrl+C / Esc

## When this skill should fire

- "Add a new keybinding to yaah TUI"
- "Handle a new event in yaah TUI"
- "Make yaah TUI work in alt screen / inline / with mouse"
- "Show a progress bar in the terminal taskbar"
- "Migrate this v1 bubbletea code"
- "yaah tui looks broken / doesn't compile"
- Any work on `internal/tui/tui.go` or `cmd/yaah/tui.go`

## When NOT to load this

- Pure provider/tool/agent work — bubbletea is rendering only
- REPL (`internal/repl/`) — uses ANSI color helpers, not bubbletea
- File-system / config / memory code — no UI
- Markdown rendering details — that's `charm-glamour`
- TUI primitive components (textinput, viewport, etc.) — that's `charm-bubbles`
- Low-level cell rendering or custom terminal I/O — that's `charm-ultraviolet`
