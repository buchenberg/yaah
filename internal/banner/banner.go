// Package banner provides the figlet + lolcat startup banner shared
// between the CLI REPL and the TUI.
//
// The rainbow coloring algorithm is vendored from
// github.com/flaviocopes/gololcat (which is package main and not
// importable as a library).
package banner

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/lsferreira42/figlet-go/figlet"
)

const Tagline = "For agents by agents"
const Title = "yaah"

// lolcatRGB maps a character index to an RGB triple using the same
// sine-wave algorithm as gololcat / the original lolcat.
func lolcatRGB(i int) (int, int, int) {
	f := 0.1
	return int(math.Sin(f*float64(i)+0)*127 + 128),
		int(math.Sin(f*float64(i)+2*math.Pi/3)*127 + 128),
		int(math.Sin(f*float64(i)+4*math.Pi/3)*127 + 128)
}

// Generate renders "yaah" as figlet ASCII art with lolcat rainbow
// coloring, followed by the tagline and subtitle. Returns the styled
// banner string and its line count. Falls back to plain "yaah" if
// figlet fails. Respects the NO_COLOR environment variable.
func Generate() (string, int) {
	art, err := figlet.Render(Title, figlet.WithFont("fonts/standard"))
	if err != nil || strings.TrimSpace(art) == "" {
		return Title, 1
	}

	lines := strings.Split(strings.TrimRight(art, "\n"), "\n")
	noColor := os.Getenv("NO_COLOR") != ""

	dim := "\033[2m"
	reset := "\033[0m"
	if noColor {
		dim, reset = "", ""
	}

	var b strings.Builder
	charIdx := 0
	for _, line := range lines {
		for _, r := range line {
			if noColor {
				b.WriteRune(r)
			} else {
				cr, cg, cb := lolcatRGB(charIdx)
				fmt.Fprintf(&b, "\033[38;2;%d;%d;%dm%c\033[0m", cr, cg, cb, r)
			}
			charIdx++
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dim)
	b.WriteString(Tagline)
	b.WriteString(reset)

	return b.String(), len(lines) + 3
}

// Render returns the full CLI startup banner: figlet art with subtitle,
// a dim version line, and trailing blank lines.
func Render(version string) string {
	art, _ := Generate()

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(art)
	b.WriteString("\n\n")
	b.WriteString("  \033[2m")
	b.WriteString(version)
	b.WriteString("\033[0m")
	b.WriteString("\n\n")
	return b.String()
}
