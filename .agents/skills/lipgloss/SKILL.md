---
name: lipgloss
description: Terminal styling for Go using charm.land/lipgloss/v2. Use when building TUIs, styling CLI output, or any time you need colored/bordered/aligned terminal text in Go. Covers: styles, colors (ANSI/256/TrueColor), formatting, padding, margins, borders, alignment, layout (Join, Place), tables, lists, trees, color utilities, inheritance, and adaptive colors.
---

# Lip Gloss v2 — Terminal Styling for Go

Import: `"charm.land/lipgloss/v2"`

## Basic Pattern

```go
var style = lipgloss.NewStyle().
    Bold(true).
    Foreground(lipgloss.Color("#FAFAFA")).
    Background(lipgloss.Color("#7D56F4")).
    PaddingTop(2).PaddingLeft(4).
    Width(22)

lipgloss.Println(style.Render("Hello, kitty"))
```

Always use `lipgloss.Println` / `lipgloss.Sprint` (not `fmt.Println`) so colors are automatically downsampled to the terminal's capability.

## Colors

**ANSI 16 (4-bit):** `lipgloss.Color("5")` (magenta), `"9"` (red), `"12"` (light blue). Named constants: `lipgloss.Black`, `lipgloss.Red`, `lipgloss.Green`, `lipgloss.Yellow`, `lipgloss.Blue`, `lipgloss.Magenta`, `lipgloss.Cyan`, `lipgloss.White`, `lipgloss.BrightBlack`, `lipgloss.BrightRed`, ...

**ANSI 256 (8-bit):** `lipgloss.Color("86")` (aqua), `"201"` (hot pink).

**TrueColor (24-bit):** `lipgloss.Color("#0000FF")`, `"#04B575"`, `"#3C3C3C"`.

**Color utilities:**
```go
c := lipgloss.Color("#EB4268")
dark := lipgloss.Darken(c, 0.5)
light := lipgloss.Lighten(c, 0.35)
green := lipgloss.Complementary(c)
withAlpha := lipgloss.Alpha(c, 0.2)
```

**1D/2D color blends:**
```go
colors := lipgloss.Blend1D(10, lipgloss.Color("#FF0000"), lipgloss.Color("#0000FF"))
colors := lipgloss.Blend2D(80, 24, 45.0, color1, color2, color3)
```

## Inline Formatting

```go
style := lipgloss.NewStyle().
    Bold(true).
    Italic(true).
    Faint(true).         // dim
    Blink(true).
    Strikethrough(true).
    Underline(true).
    Reverse(true).       // swap fg/bg
    UnderlineStyle(lipgloss.UnderlineCurly).  // UnderlineSingle, UnderlineDouble, UnderlineCurly, UnderlineDotted, UnderlineDashed
    UnderlineColor(lipgloss.Color("#FF0000"))
```

**Hyperlinks** (degrade gracefully on unsupported terminals):
```go
s := lipgloss.NewStyle().
    Foreground(lipgloss.Color("#7B2FBE")).
    Hyperlink("https://charm.land")
lipgloss.Println(s.Render("Visit Charm"))
```

## Block-Level Formatting

**Padding (inside border):**
```go
style.Padding(2)                  // all sides
style.Padding(2, 4)               // top/bottom, left/right
style.Padding(1, 4, 2)            // top, sides, bottom
style.Padding(2, 4, 3, 1)         // top, right, bottom, left (clockwise)
```

**Margins (outside border):** Same shorthand syntax as padding.

**Custom fill characters:**
```go
s := lipgloss.NewStyle().
    Padding(1, 2).PaddingChar('·').
    Margin(1, 2).MarginChar('░')
```

**Width & Height:**
```go
style := lipgloss.NewStyle().Width(24).Height(32)
```

**MaxWidth / MaxHeight** (enforce hard limits):
```go
style.MaxWidth(40).MaxHeight(10).Render(text)
```

**Inline mode** (force single line, strip margins/padding/borders):
```go
style.Inline(true).MaxWidth(5).Render("yadda yadda")
```

**Tabs** — converted to 4 spaces by default:
```go
style.TabWidth(2)                     // 2 spaces
style.TabWidth(0)                     // remove tabs
style.TabWidth(lipgloss.NoTabConversion) // leave tabs raw
```

## Alignment

```go
style.Align(lipgloss.Left)    // default
style.Align(lipgloss.Center)
style.Align(lipgloss.Right)
```

## Borders

**Built-in border styles:**
```go
style.BorderStyle(lipgloss.NormalBorder())
style.BorderStyle(lipgloss.RoundedBorder())
style.BorderStyle(lipgloss.ThickBorder())
style.BorderStyle(lipgloss.DoubleBorder())
style.BorderStyle(lipgloss.HiddenBorder())        // no border, preserves space
```

**Border colors:**
```go
style.
    BorderForeground(lipgloss.Color("63")).
    BorderBackground(lipgloss.Color("228")).
    BorderTop(true).BorderLeft(true)
```

**Border shorthand** (clockwise from top):
```go
style.Border(lipgloss.ThickBorder(), true, false)                    // top+bottom
style.Border(lipgloss.DoubleBorder(), true, false, false, true)     // top+left
```

**Gradient borders:**
```go
style.Border(lipgloss.RoundedBorder()).
    BorderForegroundBlend(lipgloss.Color("#FF0000"), lipgloss.Color("#0000FF"))
```

**Custom border:**
```go
myBorder := lipgloss.Border{
    Top: "._.:*:", Bottom: "._.:*:",
    Left: "|*", Right: "|*",
    TopLeft: "*", TopRight: "*",
    BottomLeft: "*", BottomRight: "*",
}
```

## Layout Helpers

**Horizontal join** (align shorter blocks relative to tallest):
```go
lipgloss.JoinHorizontal(lipgloss.Top, blockA, blockB, blockC)
lipgloss.JoinHorizontal(lipgloss.Center, blockA, blockB)
lipgloss.JoinHorizontal(lipgloss.Bottom, blockA, blockB)
lipgloss.JoinHorizontal(0.2, blockA, blockB)  // 20% from top
```

**Vertical join:**
```go
lipgloss.JoinVertical(lipgloss.Left, blockA, blockB)
```

**Place** (position text in whitespace):
```go
lipgloss.PlaceHorizontal(80, lipgloss.Center, paragraph)
lipgloss.PlaceVertical(30, lipgloss.Bottom, paragraph)
lipgloss.Place(30, 80, lipgloss.Right, lipgloss.Bottom, paragraph)
```

**Measure:**
```go
width := lipgloss.Width(block)
height := lipgloss.Height(block)
w, h := lipgloss.Size(block)
```

**Wrap** (preserves ANSI styles across line breaks):
```go
wrapped := lipgloss.Wrap(styledText, 40, " ")
```

## Copying & Inheritance

Styles are pure value types — assignment creates a true copy:
```go
style := lipgloss.NewStyle().Foreground(lipgloss.Color("219"))
copied := style                     // independent copy
wild := style.Blink(true)           // also a copy, with blink added
```

**Inheritance** (only unset rules are inherited):
```go
var styleA = lipgloss.NewStyle().
    Foreground(lipgloss.Color("229")).
    Background(lipgloss.Color("63"))

var styleB = lipgloss.NewStyle().
    Foreground(lipgloss.Color("201")).  // this wins
    Inherit(styleA)                     // only background inherits
```

**Unsetting:**
```go
style := lipgloss.NewStyle().
    Bold(true).UnsetBold().
    Background(lipgloss.Color("227")).UnsetBackground()
```

## Rendering Output

Always use lipgloss writer functions (they downsample colors for non-TTY output):
```go
lipgloss.Println(s)       // print to stdout
lipgloss.Fprint(os.Stderr, s)
downsampled := lipgloss.Sprint(s)
```

Full set: `Print`, `Println`, `Printf`, `Fprint`, `Fprintln`, `Fprintf`, `Sprint`, `Sprintln`, `Sprintf`.

## Tables

Import: `"charm.land/lipgloss/v2/table"`

```go
t := table.New().
    Border(lipgloss.NormalBorder()).
    BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("99"))).
    StyleFunc(func(row, col int) lipgloss.Style {
        switch {
        case row == table.HeaderRow:
            return headerStyle
        case row%2 == 0:
            return evenRowStyle
        default:
            return oddRowStyle
        }
    }).
    Headers("COL1", "COL2", "COL3").
    Rows(
        []string{"a", "b", "c"},
        []string{"d", "e", "f"},
    )

// Add rows incrementally:
t.Row("English", "Hello", "Hi")

lipgloss.Println(t)
```

**Table border presets:** `lipgloss.MarkdownBorder()`, `lipgloss.ASCIIBorder()`, `lipgloss.NormalBorder()`.

## Lists

Import: `"charm.land/lipgloss/v2/list"`

```go
l := list.New("Item A", "Item B", "Item C")

// Nesting:
l := list.New(
    "A", list.New("A1", "A2"),
    "B", list.New("B1", "B2"),
)

// Enumerator styles: list.Arabic, list.Alphabet, list.Roman, list.Bullet, list.Tree

// Styled:
l := list.New("Glossier", "Nyx", "Mac").
    Enumerator(list.Roman).
    EnumeratorStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("99")).MarginRight(1)).
    ItemStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("212")))

// Incremental:
l := list.New()
l.Item("Lip Gloss")
```

## Trees

Import: `"charm.land/lipgloss/v2/tree"`

```go
t := tree.Root(".").
    Child("A", "B", "C")

// Nesting:
t := tree.Root(".").
    Child("macOS").
    Child(
        tree.New().Root("Linux").Child("NixOS").Child("Arch Linux (btw)"),
    )

// Enumerators: tree.DefaultEnumerator, tree.RoundedEnumerator

// Incremental:
t := tree.New()
t.Child("Node")
```

## Adaptive Colors (Light/Dark)

Detect terminal background at runtime:

```go
hasDarkBG := lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
lightDark := lipgloss.LightDark(hasDarkBG)

myColor := lightDark(
    lipgloss.Color("#D7FFAE"),  // light bg
    lipgloss.Color("#D75FEE"),  // dark bg
)
```

**With Bubble Tea:**
```go
func (m model) Init() tea.Cmd {
    return tea.RequestBackgroundColor
}
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.BackgroundColorMsg:
        m.styles = newStyles(msg.IsDark())
        return m, nil
    }
}
```

## Complete Colors (Per-Profile)

Specify exact values for each color profile:
```go
import "github.com/charmbracelet/colorprofile"

profile := colorprofile.Detect(os.Stdout, os.Environ())
completeColor := lipgloss.Complete(profile)

myColor := completeColor(ansi16Color, ansi256Color, trueColor)
```

## Key Patterns for yaah / Bubble Tea TUIs

- Declare style vars as package-level `var` blocks with `lipgloss.NewStyle()` (not `init()`)
- Use `lipgloss.JoinVertical(lipgloss.Left, ...)` to stack TUI sections
- Use `lipgloss.Color("NNN")` strings from the 256-color palette for quick styling
- Always render through `lipgloss.Println` / `lipgloss.Sprint` for automatic color downsampling
- For Bubble Tea views, build the styled string in `View()` and return it — lipgloss styles are pure values, safe to create per-frame
