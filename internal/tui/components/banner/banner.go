package banner

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/banner"
	"github.com/rivo/tview"
)

// Build creates the figlet title banner and returns the total number
// of lines so the caller can size the header dynamically.
func Build(dimColor string) (lines int, tv *tview.TextView) {
	tv = tview.NewTextView().
		SetTextAlign(tview.AlignLeft).
		SetDynamicColors(true).
		SetWordWrap(false)

	art, tagline, _ := banner.GeneratePlain()

	// Lolcat rainbow on the figlet art — per-character colors via the
	// shared banner.LolcatRGB function. This is the same lolcat effect
	// used by the REPL banner renderer.
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

	// Tagline in dim gray (not blue). [-:-:-] is the full reset — [-]
	// alone leaves the dim attribute active.
	b.WriteString(fmt.Sprintf("[%s::d]%s[-:-:-]", dimColor, tagline))
	lines++

	tv.SetText(b.String())
	return lines, tv
}
