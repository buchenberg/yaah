package repl

import (
	"os"

	"github.com/buchenberg/yaah/internal/banner"
)

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[36m"
)

var useColor = true

func InitNoColor() {
	useColor = os.Getenv("NO_COLOR") == ""
}

func wrap(prefix, text string) string {
	if !useColor {
		return text
	}
	return prefix + text + ansiReset
}

func Bold(text string) string { return wrap(ansiBold, text) }
func Dim(text string) string  { return wrap(ansiDim, text) }
func Cyan(text string) string { return wrap(ansiCyan, text) }

// Banner returns the startup splash screen for the REPL.
func Banner(version string) string {
	return banner.Render(version)
}

func Prompt() string {
	return Bold("yaah") + " " + Cyan("❯") + " "
}
