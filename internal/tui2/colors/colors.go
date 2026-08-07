// Package colors provides shared tview color tokens and helpers used by all
// TUI2 component packages.
package colors

import "github.com/rivo/tview"

// Color tokens are RAW tview values (no brackets). Tag/TagBold and the
// `[%s]` format strings used by the block components wrap them in
// brackets. Keeping the tokens bracket-free avoids double-bracket tags
// like `[[#00afff]]` that tview renders as literal text.
const (
	Accent = "#00afff"    // blue accent
	Dim    = "#5f5f5f::d" // dim gray with faint style
	Reset  = "[-]"        // complete close tag (the only bracketed token)
)

// Tag wraps text in a tview foreground-color tag.
func Tag(color, text string) string { return "[" + color + "]" + text + Reset }

// TagBold wraps text in a tview bold + foreground-color tag.
func TagBold(color, text string) string { return "[" + color + "::b]" + text + Reset }

// TaggedStringWidth is an alias for tview.TaggedStringWidth, used when
// computing column widths for tag-stripped strings.
var TaggedStringWidth = tview.TaggedStringWidth

// RenderCtx carries the rendering environment to content components.
type RenderCtx struct {
	Width    int
	Theme    *Theme
	Expanded bool
}

// Renderable is a content component that produces tagged text for the
// message stream.
type Renderable interface {
	Render(ctx RenderCtx) string
}
