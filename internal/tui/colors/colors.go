// Package colors provides shared tview color tokens and helpers used by all
// TUI2 component packages.
package colors

import "github.com/rivo/tview"

// TaggedStringWidth is an alias for tview.TaggedStringWidth, used when
// computing column widths for tag-stripped strings.
var TaggedStringWidth = tview.TaggedStringWidth

// RenderCtx carries the rendering environment to content components.
type RenderCtx struct {
	Width    int
	Theme    *Theme
	Expanded bool
}
