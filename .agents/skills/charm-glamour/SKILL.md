---
name: charm-glamour
description: Charm Glamour v2 — markdown rendering for ANSI terminals, used by yaah's TUI to render assistant messages. Load when working on `internal/tui/tui.go`'s `mdRenderer`/`glamourRender`/`renderMarkdown` path, adding markdown rendering to other yaah output (REPL tool results, slash command output, skill list display), changing the rendering style, fixing link/code-block/table rendering bugs, or migrating v1 glamour code to v2. v1 patterns (github.com/charmbracelet/glamour, WithAutoStyle, WithColorProfile(termenv.TrueColor), Overlined field, fmt.Print of rendered output for downsampling) are all wrong for yaah — use this skill to get the v2 API right.
---

# Charm Glamour v2 — Markdown rendering for yaah

Stylesheet-based markdown → ANSI converter. yaah's TUI uses it to render
assistant messages, tool output, and slash-command content into the
scrollback viewport.

> **Source of truth.** Repo at
> `C:\Code\Personal\yaah\.scratch\repos\glamour\` (already cloned) and on
> GitHub at `charmbracelet/glamour`. `UPGRADE_GUIDE_V2.md` in the repo is
> the single best doc — it covers every v1→v2 break in 280 lines.

## Current yaah usage (read this first)

`internal/tui/tui.go` already has a working v2 glamour integration. Pattern
is the canonical one — copy and extend it, don't reinvent:

```go
// internal/tui/tui.go:160-175
func (m *Model) createRenderer() {
    width := m.width - 2
    if width < 20 {
        width = 20
    }
    r, err := glamour.NewTermRenderer(
        glamour.WithStandardStyle("dark"),
        glamour.WithWordWrap(width),
        glamour.WithEmoji(),
        glamour.WithChromaFormatter("terminal256"),
        glamour.WithPreservedNewLines(),
    )
    if err == nil {
        m.mdRenderer = r
    }
}
```

Calling site is one helper, `m.glamourRender(content)`, that pre-injects
OSC 8 hyperlinks before rendering. Renderer is **recreated on window
resize** because `WithWordWrap` is set at construction time. **Don't
build a new renderer per call** — that's the whole point of keeping
`m.mdRenderer` as a field.

## Module and import path (v2)

```go
import (
    "charm.land/glamour/v2"               // main package
    "charm.land/glamour/v2/ansi"          // StyleConfig, StyleBlock, etc.
    "charm.land/glamour/v2/styles"        // built-in style configs
)
```

```bash
go get charm.land/glamour/v2@latest
go get charm.land/lipgloss/v2@latest    # for color downsampling
```

## Public API (v2)

```go
// Quick one-shot — no state:
out, err := glamour.Render(in, "dark")           // style name or path
out, err := glamour.RenderWithEnvironmentConfig(in)  // honors $GLAMOUR_STYLE

// Reusable renderer (what yaah uses):
r, err := glamour.NewTermRenderer(opts...)
out, err := r.Render(in)
out, err := r.RenderBytes([]byte(in))

// Streaming io.Writer (advanced; not needed in yaah today):
r.Write([]byte(chunk))
r.Close()
out, _ := io.ReadAll(r)
```

### TermRenderer options

| Option                          | Purpose                                              |
|---------------------------------|------------------------------------------------------|
| `WithStandardStyle(name)`       | Use a built-in style. See table below.               |
| `WithStylePath(path)`           | Path to a JSON style file, or a built-in name.       |
| `WithStyles(ansi.StyleConfig)`  | Programmatic style.                                  |
| `WithStylesFromJSONBytes([]byte)` | Parse a JSON style from a byte slice.              |
| `WithStylesFromJSONFile(name)`  | Load a JSON style from a file.                       |
| `WithEnvironmentConfig()`       | Read style from `GLAMOUR_STYLE` env var.             |
| `WithWordWrap(n)`               | Wrap at column N. **Set on construction; recreate on resize.** |
| `WithTableWrap(true)`           | Wrap table cells (default `true`). Set `false` to truncate with `…`. |
| `WithInlineTableLinks(true)`    | Render table links inline (default: list at bottom). |
| `WithPreservedNewLines()`       | Don't collapse `\n` runs.                            |
| `WithEmoji()`                   | Enable `:emoji:` shortcodes.                         |
| `WithChromaFormatter("terminal256")` | Syntax highlighting for code blocks. Try `"terminal16"`, `"terminal8"`, `"terminal256"`, `"terminal16m"` (truecolor), `"noop"`. |
| `WithBaseURL(url)`              | Base for relative links.                             |
| `WithOptions(opts...)`          | Group options under one Option.                      |

### Built-in styles (valid `WithStandardStyle` names)

From `styles/styles.go`:

| Name             | Use when                                              |
|------------------|-------------------------------------------------------|
| `"dark"`         | **Default.** Dark terminal background. yaah uses this. |
| `"light"`        | Light terminal background.                            |
| `"pink"`         | Playful, pink-heavy.                                  |
| `"dracula"`      | Dracula theme.                                        |
| `"tokyo-night"`  | Tokyo Night theme.                                    |
| `"ascii"`        | No color, no unicode. For plain terminals/pipes.      |
| `"notty"`        | Pipe-safe, minimal styling. Use for non-TTY output.   |

`glamour.RenderWithEnvironmentConfig` reads `GLAMOUR_STYLE` env var (defaults
to `"dark"`). yaah's `notty` choice for non-TTY output is `"notty"` —
already in the catalog.

## V1 → V2 breaking changes (what LLMs get wrong)

| v1 (wrong)                                         | v2 (correct)                              |
|----------------------------------------------------|-------------------------------------------|
| `import "github.com/charmbracelet/glamour"`        | `import "charm.land/glamour/v2"`          |
| `import "github.com/charmbracelet/glamour/ansi"`   | `import "charm.land/glamour/v2/ansi"`     |
| `import "github.com/charmbracelet/glamour/styles"` | `import "charm.land/glamour/v2/styles"`   |
| `glamour.WithAutoStyle()`                          | **Removed.** Default is `"dark"`.         |
| `glamour.WithColorProfile(termenv.TrueColor)`      | **Removed.** Use `lipgloss.Print(out)` for downsampling, or output TrueColor directly. |
| `import "github.com/muesli/termenv"`               | Drop it (no longer needed).               |
| `StylePrimitive{Overlined: &true}`                 | **Removed.** Use Bold/Underline/Inverse/BackgroundColor. |
| Custom `ansi.MarginWriter` without `Close()`       | **Must call `Close()`.** Leak otherwise.  |

### Color downsampling — the biggest gotcha

Glamour v2 is **pure**: it always emits TrueColor escape codes. If you write
the output via `fmt.Print`, the terminal sees colors it may not support
(truecolor escapes are usually a no-op on Windows cmd.exe and basic
terminals). Two correct patterns:

**Pattern A: Let lipgloss adapt** (recommended for yaah's TUI):

```go
out, _ := r.Render(md)
lipgloss.Print(out)   // downsamples to terminal capabilities
```

**Pattern B: Force always-TrueColor** (works on Windows Terminal, modern
Terminal.app, most Linux terms). yaah's TUI does this implicitly because
it writes through Bubble Tea, which handles color profiles — so today
yaah is pattern B. If you ever pipe glamour output outside Bubble Tea,
switch to pattern A.

**Anti-pattern (do not do this):**

```go
out, _ := r.Render(md)
fmt.Print(out)        // raw TrueColor on a non-truecolor terminal = wrong colors
```

## Light/dark styles — current yaah gap

`internal/tui/tui.go:166` hardcodes `"dark"`. If the user runs yaah TUI in
a light terminal, headings will be unreadable (dark-on-dark). Bubble Tea v2
ships the right primitive (`tea.RequestBackgroundColor` returning a
`tea.BackgroundColorMsg` with `.IsDark()`), but it's not wired to the
glamour renderer today.

**Fix when needed** (don't apply unless asked — touches the TUI bootstrap):

```go
// In Init():
return tea.Batch(m.spinner.Tick(), tea.RequestBackgroundColor, ...)

// In Update:
case tea.BackgroundColorMsg:
    m.isDark = msg.IsDark()
    m.createRenderer()  // recreates with the right style
```

```go
// In createRenderer():
style := "dark"
if !isDark {
    style = "light"
}
r, _ := glamour.NewTermRenderer(
    glamour.WithStandardStyle(style),
    // …rest of existing options
)
```

The bubbles skill documents the full `RequestBackgroundColor` pattern; copy
it from there.

## Custom styles

Two ways to override: programmatic (`ansi.StyleConfig`) or JSON file. yaah
does **not** currently override the glamour style programmatically — the
default `"dark"` style is used as-is, and table layouts are handled by
extracting tables out of the markdown and rendering them as plain text
via `renderCompactTable` in `internal/tui/tui.go` (see "Extracting
tables before rendering" below). If you add a programmatic style
override, follow the pattern below.

```go
import "charm.land/glamour/v2/ansi"

var myStyle = &ansi.StyleConfig{
    Document: ansi.StyleBlock{
        StylePrimitive: ansi.StylePrimitive{
            Color:       stringPtr("#E6DB74"),
            BackgroundColor: stringPtr("#1E1E1E"),
        },
        Margin: uintPtr(2),
    },
    Heading: ansi.StyleBlock{
        StylePrimitive: ansi.StylePrimitive{
            Bold:  boolPtr(true),
            Color: stringPtr("39"),
        },
    },
    // H1..H6, Paragraph, List, Item, Code, CodeBlock, Link, LinkText, Image,
    // BlockQuote, Emph, Strong, Strikethrough, HorizontalRule, Task, Table,
    // DefinitionList, DefinitionDescription — all overridable.
    CodeBlock: ansi.StyleCodeBlock{
        StyleBlock: ansi.StyleBlock{
            StylePrimitive: ansi.StylePrimitive{Color: stringPtr("244")},
            Margin:        uintPtr(2),
        },
        Chroma: &ansi.Chroma{ /* per-token colors */ },
    },
}

r, _ := glamour.NewTermRenderer(glamour.WithStyles(*myStyle))
```

JSON alternative (user-loadable themes — `GLAMOUR_STYLE=/path/to/style.json`):

```go
r, _ := glamour.NewTermRenderer(glamour.WithStylesFromJSONFile("theme.json"))
```

## yaah-specific patterns

### Recreating the renderer on resize

`WithWordWrap` is set at construction time. yaah's TUI already handles
this in `createRenderer()` (line 160) — call it again from the
`tea.WindowSizeMsg` handler:

```go
case tea.WindowSizeMsg:
    m.width, m.height = msg.Width, msg.Height
    m.createRenderer()  // re-apply word wrap at new width
    // forward to viewport
```

If you forget this, the renderer stays at the old wrap width and either
underflows (line too narrow, weird padding) or overflows (line wraps past
viewport edge).

### Pre-injecting OSC 8 hyperlinks

Glamour doesn't emit clickable hyperlinks by default. yaah's
`injectHyperlinks` (line 190) walks the markdown before rendering and
rewrites `[text](url)`, `<url>`, and bare URLs into OSC 8 escape sequences
that modern terminals render as clickable. **Don't re-implement this** —
it's the `m.glamourRender` wrapper's job.

### Extracting tables before rendering

yaah's `renderMarkdown` (line 271) extracts tables out of the markdown and
renders them as plain text via `renderCompactTable` before passing the
rest to glamour. This is a workaround for glamour's table rendering being
too wide for compact TUI layouts. If you see "table looks bad in TUI"
issues, the answer is more table extraction, not different glamour options.

### Streaming agent output

Glamour is **synchronous** — you give it a complete string, get back a
rendered string. yaah's approach: accumulate streamed tokens into
`Message.Content` (raw markdown), then call `AddAssistantMessage` which
runs `glamourRender` once on the final string. Don't try to glamour-render
each token — it re-parses the whole document and the output flickers.

## Common pitfalls

1. **Forgetting color downsampling** — if you ever call `fmt.Print(out)`
   instead of `lipgloss.Print(out)`, the terminal sees raw TrueColor
   escapes. yaah's TUI is safe because Bubble Tea handles profiles; any
   non-Bubble Tea glamour output needs `lipgloss.Print`.
2. **Setting `WithAutoStyle()`** — **gone in v2.** Will not compile. The
   default is `"dark"`.
3. **`WithColorProfile(termenv.TrueColor)`** — **gone in v2.** Will not
   compile.
4. **Building a new renderer per call** — expensive (re-parses the JSON
   style, sets up the goldmark pipeline). Keep one on the model and reuse
   via `r.Render(in)`.
5. **Renderer not recreated on resize** — wrap width stuck at original.
   yaah handles this; don't break it.
6. **Calling `Render` with an empty string** — returns empty string, no
   error. yaah's `glamourRender` short-circuits on `m.mdRenderer == nil`
   but doesn't check for empty input — safe enough since empty markdown
   produces empty output.
7. **Glamour-rendering streaming chunks** — see "Streaming agent output"
   above. Accumulate, then render once.
8. **Glamour doesn't pass through raw HTML** — it's a markdown renderer,
   not a HTML renderer. Inline HTML is sanitized by bluemonday (in
   `go.mod`). Don't expect `<details>`, `<kbd>`, etc.
9. **Emoji and CJK** — v2 fixed prior wrapping bugs. If you see wrapping
   issues with emoji or CJK, it's a glamour bug, not yaah. File upstream.
10. **Trusting older blog posts** — almost all online glamour tutorials
    show v1 (`github.com/charmbracelet/glamour`, `WithAutoStyle`,
    `termenv.TrueColor`). Convert with the table above.

## Verification checklist

- [ ] `gofmt -l .` empty
- [ ] `go vet ./...` clean
- [ ] `go build .` succeeds
- [ ] No `github.com/charmbracelet/glamour` imports remain (only
      `charm.land/glamour/v2`)
- [ ] No `glamour.WithAutoStyle(` calls
- [ ] No `glamour.WithColorProfile(` calls
- [ ] No `termenv` import (glamour doesn't need it anymore)
- [ ] No `Overlined:` field in any custom `ansi.StyleConfig`
- [ ] Custom `MarginWriter` / `IndentWriter` / `PaddingWriter` usages
      have a matching `defer ...Close()`
- [ ] `go test ./...` passes (glamour has a goldmark-based test suite;
      yaah's own tests shouldn't break)
- [ ] Manual: `yaah tui`, send a message with markdown — headings,
      lists, code blocks, links render correctly
- [ ] Manual: `yaah tui` then resize the terminal — no broken wrap
- [ ] Manual: `yaah tui` in a light-themed terminal (e.g.
      Windows Terminal with a light scheme) — headings readable
      (this will fail today; expected until light/dark is wired up)

## When this skill should fire

- "Yaah TUI markdown looks broken" / "headings unreadable" / "code block
  has no colors"
- "Add a glamour renderer for slash-command output"
- "Render skill descriptions in the TUI"
- "Support user-chosen markdown themes"
- "Make yaah TUI work in light terminals"
- "migrate this v1 glamour code"
- Any work touching `internal/tui/tui.go` lines ~100–290 (glamour surface)

## When NOT to load this

- Working on the REPL (non-TUI) — uses ansi/color helpers, not glamour
- Pure provider/tool/agent work — glamour is rendering-only
- Touching `internal/repl/` — separate ANSI color helpers
- File-system / config / memory code — no markdown rendering
