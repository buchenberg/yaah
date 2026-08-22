// Package lolcat provides tview-compatible rainbow coloring for TUI2.
//
// The rainbow algorithm is ported from internal/banner (which itself is
// vendored from github.com/flaviocopes/gololcat). Instead of ANSI escape
// codes it emits tview color tags of the form [#RRGGBB]text[-].
package lolcat

import (
	"fmt"
	"math"
	"strings"
)

// RGB returns the rainbow RGB triple for character index i using the
// sine-wave algorithm shared with internal/banner.LolcatRGB.
func RGB(i int) (int, int, int) {
	f := 0.1
	return int(math.Sin(f*float64(i)+0)*127 + 128),
		int(math.Sin(f*float64(i)+2*math.Pi/3)*127 + 128),
		int(math.Sin(f*float64(i)+4*math.Pi/3)*127 + 128)
}

// Rainbow wraps text in tview color tags ([#RRGGBB]) using the rainbow
// gradient. The seed offsets the gradient start point — advance it each
// frame for a flowing rainbow effect.
//
// Example:
//
//	Rainbow("Reasoning...", 0)  → "[#ff8800]R[-][#88ff00]e[-]..."
//	Rainbow("Reasoning...", 1)  → "[#88ff00]R[-][#00ff88]e[-]..."  (shifted)
func Rainbow(text string, seed float64) string {
	var b strings.Builder
	for i, r := range text {
		cr, cg, cb := RGB(int(seed) + i)
		fmt.Fprintf(&b, "[#%02x%02x%02x]%c[-]", cr, cg, cb, r)
	}
	return b.String()
}

// StripTags removes tview color tags, returning plain text.
// Useful for measuring the display width of a tagged string.
func StripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '[':
			inTag = true
		case r == ']' && inTag:
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}
