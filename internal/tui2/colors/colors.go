// Package colors provides shared tview color tags and helpers used by all
// TUI2 component packages.
package colors

import "github.com/rivo/tview"

// Color constants (tview tag format — already include brackets).
const (
	Accent = "[#00afff]"    // blue accent
	Dim    = "[#5f5f5f::d]" // dim gray
	Reset  = "[-]"          // close tag
)

// Tag wraps text in a tview foreground-color tag.
func Tag(hex, text string) string { return "[" + hex + "]" + text + "[-]" }

// TagBold wraps text in a tview bold + foreground-color tag.
func TagBold(hex, text string) string { return "[" + hex + "::b]" + text + "[-]" }

// TaggedStringWidth is an alias for tview.TaggedStringWidth, used when
// computing column widths for tag-stripped strings.
var TaggedStringWidth = tview.TaggedStringWidth
