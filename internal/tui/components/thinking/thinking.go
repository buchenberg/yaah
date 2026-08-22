// Package thinking renders the animated "Reasoning..." / "Thinking..."
// indicator with lolcat rainbow coloring and a braille spinner.
package thinking

import (
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/tui/lolcat"
)

// Indicator renders a lolcat-colored spinner with a label.
type Indicator struct {
	seed    float64
	frame   int
	frames  []string
	label   string
	visible atomic.Bool
}

// New creates a thinking indicator. Use "Reasoning..." for reasoning
// blocks and "Thinking..." for pre-response thinking.
func New(label string) *Indicator {
	return &Indicator{
		frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		label:  label,
	}
}

// Advance moves to the next spinner frame and increments the lolcat seed
// for the flowing rainbow effect on the label text.
func (i *Indicator) Advance() {
	i.frame = (i.frame + 1) % len(i.frames)
	i.seed++
}

// Show makes the indicator visible.
func (i *Indicator) Show() { i.visible.Store(true) }

// Hide makes the indicator invisible.
func (i *Indicator) Hide() { i.visible.Store(false) }

// Visible reports whether the indicator is currently shown. Race-safe:
// read from the animation ticker goroutine while writers run on the app
// goroutine via QueueUpdateDraw.
func (i *Indicator) Visible() bool { return i.visible.Load() }

// Spinner returns the current spinner frame character.
func (i *Indicator) Spinner() string {
	if i.visible.Load() {
		return i.frames[i.frame]
	}
	return " " // blank when hidden
}

// Render returns the full tview-tagged indicator line.
func (i *Indicator) Render() string {
	if !i.visible.Load() {
		return ""
	}
	spinner := i.frames[i.frame]
	full := "  " + spinner + " " + i.label
	return lolcat.Rainbow(full, i.seed)
}
