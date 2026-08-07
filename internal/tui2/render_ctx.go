package tui2

// RenderCtx carries the layout and styling context for a single render pass.
// Content components use it to size blocks (width/height) and pick colors
// (Theme) without hardcoding values or importing the colors package directly.
type RenderCtx struct {
	Width  int
	Height int
	Theme  *Theme
}

// newRenderCtx builds a RenderCtx from the widget's current dimensions and
// the active theme. width <= 0 is clamped to 80.
func newRenderCtx(width, height int, t *Theme) RenderCtx {
	if width <= 0 {
		width = 80
	}
	return RenderCtx{Width: width, Height: height, Theme: t}
}
