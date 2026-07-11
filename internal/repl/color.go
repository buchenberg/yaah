package repl

import (
	"fmt"
	"os"
)

// ANSI escape codes
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

// useColor is set by InitNoColor() based on the NO_COLOR env convention.
var useColor = true

// InitNoColor reads the NO_COLOR env var and sets useColor accordingly.
// Per https://no-color.org/: when NO_COLOR is present and non-empty,
// color output is disabled. An empty value does NOT disable color.
// Called once at startup, but safe to call again (e.g. in tests).
func InitNoColor() {
	useColor = os.Getenv("NO_COLOR") == ""
}

// wrap wraps text with an ANSI escape sequence if useColor is true.
func wrap(prefix, text string) string {
	if !useColor {
		return text
	}
	return prefix + text + ansiReset
}

// Bold returns text in bold (or plain if NO_COLOR is set).
func Bold(text string) string {
	return wrap(ansiBold, text)
}

// Dim returns text in dim (or plain if NO_COLOR is set).
func Dim(text string) string {
	return wrap(ansiDim, text)
}

// Cyan returns text in cyan (or plain if NO_COLOR is set).
func Cyan(text string) string {
	return wrap(ansiCyan, text)
}

// Green returns text in green (or plain if NO_COLOR is set).
func Green(text string) string {
	return wrap(ansiGreen, text)
}

// Yellow returns text in yellow (or plain if NO_COLOR is set).
func Yellow(text string) string {
	return wrap(ansiYellow, text)
}

// Banner returns the startup banner string for the REPL.
func Banner(version string) string {
	return fmt.Sprintf("\n%s %s\n%s\n\n",
		Bold("yaah"),
		Dim(version),
		Dim("Yet Another Agent Harness — type /? for help, /exit to quit"),
	)
}

// Prompt returns the input prompt string for the REPL.
func Prompt() string {
	return Bold("yaah") + " " + Cyan("❯") + " "
}
