---
name: charm-ultraviolet
description: Charm Ultraviolet — cell-based terminal renderer and input layer that powers Bubble Tea v2 and Lip Gloss v2. Load ONLY when writing custom terminal code below bubbletea (raw `Screen`/`Buffer`/`Window` drawing, custom input event loops, custom Kitty keyboard protocol handling, terminal feature detection not exposed by bubbletea, or shelling out to editors with suspend/resume). Do NOT load for ordinary yaah TUI work — that's `charm-bubbletea`, `charm-bubbles`, and `charm-glamour`. Ultraviolet is upstream of bubbletea and explicitly marked "API may change."
---

# Charm Ultraviolet — cell-based terminal primitives (under bubbletea v2)

Low-level cell-based renderer, cross-platform input, and a diffing
algorithm inspired by ncurses. This is the **substrate** that Bubble Tea v2
sits on; you almost never need to import it directly from yaah code.

> **Source of truth.** Repo at
> `C:\Code\Personal\yaah\.scratch\repos\ultraviolet\` (already cloned) and
> on GitHub at `charmbracelet/ultraviolet`.
> The README opens with: **"Ultraviolet is in active development. The API
> may change."** Treat it as internal to the Charm stack unless you have a
> specific reason to import it.

## Relationship to yaah and bubbletea

- **Yaah does NOT import ultraviolet directly.** It's only in `go.mod` /
  `go.sum` as a transitive dep of `charm.land/bubbletea/v2` and
  `charm.land/lipgloss/v2`.
- **`tea.Msg = uv.Event`.** Every bubbletea message (KeyPressMsg, MouseMsg,
  WindowSizeMsg, BackgroundColorMsg, PasteMsg, …) is an ultraviolet event
  under the hood. That's why bubbletea can use `msg.Mod`, `msg.Mouse()`,
  `msg.IsRepeat()` — the methods are on the underlying `uv.Event` types.
- **Bubbletea owns the event loop, screen lifecycle, and resize handling
  in yaah.** Reaching past it to drive ultraviolet directly is an
  architectural change, not a code change.

If you find yourself wanting to import `github.com/charmbracelet/ultraviolet`
in yaah, **stop and ask first**. Almost every use case is already covered
by bubbletea. The exception list is below.

## When to use ultraviolet directly

Only consider importing ultraviolet in yaah when you need to do one of:

1. **Custom drawing outside `tea.View`.** You need pixel-level cell
   control for something bubbletea's `tea.NewView(string)` can't do —
   e.g. custom canvas, custom grapheme-cluster rendering, custom font/
   glyph layout, or non-text content (sparklines, mini bar charts in
   raw cells, custom tables with multi-byte cell merging).
2. **Custom input event loop.** You're building a non-bubbletea TUI tool
   (e.g. a debugging REPL, a custom installer UI) that needs raw event
   control.
3. **Suspend/resume for shelling out** to `$EDITOR`. Ultraviolet's
   `uv.Suspend()` and `t.Start()`/`t.Stop()` cycle is the right primitive
   for "stop owning the TTY while the editor runs." yaah's current
   `p.Run()` model doesn't need this, but if you add a "open in editor"
   command you'd use it.
4. **Custom Kitty keyboard protocol handling** beyond what bubbletea
   exposes (e.g. you need to react to specific
   `DisambiguateEscapeCodes` events that bubbletea abstracts away).
5. **Terminal feature detection** not in bubbletea's options. Bubbletea
   already covers AltScreen, mouse modes, focus, bracketed paste, window
   title, cursor shape/color, progress bar, keyboard enhancements. If you
   need something *else* (e.g. sixel support, OSC 52 clipboard with
   specific flags, kitty's iTerm-style notifications), drop down.

If your use case is none of the above, **use bubbletea.** The
`charm-bubbletea` skill covers it.

## Module and import path

```go
import (
    uv "github.com/charmbracelet/ultraviolet"          // root package
    "github.com/charmbracelet/ultraviolet/screen"      // drawing helpers
    "github.com/charmbracelet/ultraviolet/layout"      // constraint solver
)
```

Note: **no `/v2` suffix.** Ultraviolet uses pre-1.0 versioning
(`v0.0.0-…` pseudo-versions in `go.mod`).

```bash
go get github.com/charmbracelet/ultraviolet@latest
```

## Public surface (what's available if you do import it)

| Type / function            | Purpose                                              |
|----------------------------|------------------------------------------------------|
| `uv.DefaultTerminal()`     | Get a default `Terminal` (raw mode, event loop).     |
| `uv.NewTerminal(console, opts)` | Build a `Terminal` against a custom console.   |
| `uv.Suspend(fn)`           | Suspend the TTY, run `fn`, resume. For shelling out. |
| `uv.Cursor`                | Position, color, shape, blink, hidden.               |
| `uv.NewCursor(x, y)`       | Construct a default-block, blinking cursor.          |
| `uv.ProgressBar`           | `State`, `Value` (0-100) for the OS taskbar.         |
| `uv.ProgressBarNone` / `Default` / `Error` / `Indeterminate` / `Warning` | State constants |
| `uv.NewProgressBar(state, value)` | Construct a progress bar (clamps value 0-100). |
| `uv.KeyboardEnhancements`  | Kitty keyboard flags struct.                         |
| `uv.NewKeyboardEnhancements(flags int)` | Build from Kitty flag bits.              |
| `uv.EncodeBackgroundColor(w, c)` | Emit OSC for background color to `w`.      |
| `uv.EncodeForegroundColor(w, c)` | Emit OSC for foreground color to `w`.      |
| `uv.EncodeCursorColor(w, c)`     | Emit OSC for cursor color to `w`.         |
| `uv.EncodeCursorStyle(w, shape, blink)` | Emit DECSCUSR for cursor shape.       |
| `uv.EncodeBracketedPaste(w, enable)` | Emit paste mode set/reset to `w`.         |
| `uv.EncodeMouseMode(w, mode)`     | Emit mouse tracking set/reset.            |
| `uv.EncodeMouseEncoding(w, enc)`  | SGR vs legacy mouse encoding.             |
| `uv.EncodeProgressBar(w, pb)`     | OSC 9;4 progress to `w`.                  |
| `uv.EncodeKeyboardEnhancements(w, ke)` | Kitty keyboard query.                  |
| `uv.EncodeWindowTitle(w, title)`  | OSC 0/2 window title to `w`.              |

### Interfaces

```go
// uv.Screen — minimal screen interface
type Screen interface {
    Bounds() Rectangle
    CellAt(x, y int) *Cell
    SetCell(x, y int, c *Cell)
    WidthMethod() WidthMethod
}

// uv.Drawable — anything that can draw onto a Screen
type Drawable interface {
    Draw(scr Screen, area Rectangle)
}

type DrawableFunc func(scr Screen, rect Rectangle)
func (f DrawableFunc) Draw(scr Screen, rect Rectangle)  // makes func a Drawable
```

`TerminalScreen`, `Buffer`, `Window`, `ScreenBuffer` all implement
`Screen`. Write code against the `Screen` interface to stay
implementation-agnostic (e.g. tests can use `Buffer`; production uses
`TerminalScreen`).

### `screen` package — drawing helpers

```go
import "github.com/charmbracelet/ultraviolet/screen"

ctx := screen.NewContext(scr)
ctx.DrawString("Hello, World!", x, y)   // styled text rendering
screen.Clear(scr)                       // fill with blanks
screen.Fill(scr, cell, rect)            // fill a region
screen.Clone(scr)                       // deep-copy a screen
```

`Context` is the high-level drawing API (think `lipgloss.NewRenderer` but
at the cell level). It handles grapheme clusters, ANSI styling, and
width-method lookup.

### `layout` package — Cassowary constraint solver

```go
import "github.com/charmbracelet/ultraviolet/layout"

constraints := []layout.Constraint{
    layout.Len(20),            // exact 20 cells
    layout.Min(10),            // at least 10
    layout.Max(30),            // at most 30
    layout.Percent(0.5),       // 50% of available
    layout.Ratio(1, 2),        // 1/2 of remaining
    layout.Fill(),             // take all remaining
}
sizes := layout.Solve(constraints, totalAvailable)  // []int
```

Use this when you need constraint-based flex layout (think CSS Flexbox
for terminal cells). For most TUI work, you don't need it — just
hand-compute widths.

## Quick start (standalone)

The README's example. **Don't paste this into yaah**; if you do need
ultraviolet, factor it into a separate package that takes a `Screen`:

```go
package main

import (
    "log"

    uv "github.com/charmbracelet/ultraviolet"
    "github.com/charmbracelet/ultraviolet/screen"
)

func main() {
    t := uv.DefaultTerminal()
    scr := t.Screen()

    scr.EnterAltScreen()
    if err := t.Start(); err != nil {
        log.Fatalf("failed to start terminal: %v", err)
    }
    defer t.Stop()

    ctx := screen.NewContext(scr)
    text := "Hello, World!"
    textWidth := scr.StringWidth(text)

    display := func() {
        screen.Clear(scr)
        bounds := scr.Bounds()
        x := (bounds.Dx() - textWidth) / 2
        y := bounds.Dy() / 2
        ctx.DrawString(text, x, y)
        scr.Render()
        scr.Flush()
    }

    for ev := range t.Events() {
        switch ev := ev.(type) {
        case uv.WindowSizeEvent:
            scr.Resize(ev.Width, ev.Height)
            display()
        case uv.KeyPressEvent:
            if ev.MatchString("q", "ctrl+c") {
                return
            }
        }
    }
}
```

## Event types (subset)

| Event type             | Notes                                              |
|------------------------|----------------------------------------------------|
| `uv.WindowSizeEvent`   | Resize. `Width`, `Height`.                         |
| `uv.KeyPressEvent`     | Key press. `MatchString(...)`, `Code`, `Text`, `Mod`. |
| `uv.KeyReleaseEvent`   | Key release.                                       |
| `uv.MouseClickEvent` / `MouseReleaseEvent` / `MouseWheelEvent` / `MouseMotionEvent` | Mouse events. |
| `uv.PasteEvent`        | Pasted text.                                      |
| `uv.FocusEvent` / `uv.BlurEvent` | Focus changes (when ReportFocus is on). |
| `uv.SuspendEvent`      | Sent when the terminal suspends the process.       |

Note the **v1-era event names use "Event" suffix** (e.g. `uv.KeyPressEvent`)
while bubbletea v2's equivalent uses "Msg" suffix (e.g. `tea.KeyPressMsg`).
The actual types are the same — bubbletea just re-exports them under the
"Msg" convention.

## yaah-specific guidance

**Don't add ultraviolet to yaah's import graph.** Three reasons:

1. **Bubbletea already owns the terminal.** Mixing bubbletea's event loop
   with a parallel ultraviolet event loop will cause double-reads of stdin
   and competing state.
2. **API is unstable.** The README warns the API may change. Importing
   ultraviolet ties yaah to an unstable surface that bubbletea absorbs
   changes from.
3. **Yaah's needs are bubbletea-shaped.** TUI, keybindings, viewport
   scrolling, glamour markdown rendering — all solved by bubbletea +
   bubbles + glamour.

If you genuinely need a feature ultraviolet provides and bubbletea
doesn't, the right path is:

1. File a yaah issue documenting the gap.
2. Check if bubbletea has a recent release or open PR that exposes it.
3. As a last resort, fork or vendor ultraviolet behind a yaah-internal
   interface so the dependency surface is bounded.

## Verification checklist (if you do import it)

- [ ] You have a written reason in the commit message / PR description
      for why bubbletea is insufficient.
- [ ] You have a way to test the custom code path in isolation
      (`uv.NewTerminal(uv.NewConsole(...))` with a fake reader/writer).
- [ ] The ultraviolet version is pinned exactly in `go.mod`
      (`github.com/charmbracelet/ultraviolet v0.0.0-2026…` is OK; `@latest`
      is **not** — API can shift).
- [ ] `gofmt -l .` empty, `go vet ./...` clean, `go build .` succeeds.
- [ ] Manual: `yaah tui` still works (regression check — bubbletea +
      ultraviolet are layered; don't break the bubbletea path).

## When this skill should fire

- "yaah needs sixel graphics" (not currently in bubbletea)
- "yaah needs a custom terminal taskbar progress that bubbletea's
  `View.ProgressBar` doesn't cover"
- "yaah needs to shell out to $EDITOR without losing TTY state"
- "yaah needs a constraint-based layout solver"
- "We're porting yaah off bubbletea" (very unlikely — would be its own
  milestone)
- "Custom Kitty protocol flags we need aren't in bubbletea"
- Custom canvas / sparkline / mini-chart rendering that doesn't fit
  through glamour's markdown model

## When NOT to load this

- Any ordinary yaah TUI work — that's `charm-bubbletea` / `charm-bubbles` /
  `charm-glamour`.
- Markdown rendering — `charm-glamour`.
- TUI primitives (textinput, viewport, spinner, etc.) — `charm-bubbles`.
- Bubble Tea v2 patterns in general — `charm-bubbletea`.

**Rule of thumb:** if you're modifying `internal/tui/`, do not load this
skill. If you find yourself wanting to, that's a signal to step back and
ask whether the change belongs in bubbletea, bubbles, or glamour instead.
