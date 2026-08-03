// Package banner builds the figlet/lolcat title banner.
package banner

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/banner"
	"github.com/rivo/tview"
)

// Build creates the figlet/lolcat title banner and returns the total number
// of lines so the caller can size the header dynamically.
func Build() (lines int, tv *tview.TextView) {
	tv = tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(false)

	art, tagline, _ := banner.GeneratePlain()

	var b strings.Builder
	charIdx := 0
	for _, line := range strings.Split(art, "\n") {
		for _, r := range line {
			cr, cg, cb := banner.LolcatRGB(charIdx)
			fmt.Fprintf(&b, "[#%02x%02x%02x]%c[-]", cr, cg, cb, r)
			charIdx++
		}
		b.WriteString("\n")
		lines++
	}

	// Dim tagline below the art (inline hex to avoid bracket nesting).
	b.WriteString(fmt.Sprintf("[#00afff::d]%s[-]", tagline))
	lines++

	tv.SetText(b.String())
	return lines, tv
}
