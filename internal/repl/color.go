package repl

import (
	"os"
	"regexp"
	"strings"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
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

func Bold(text string) string   { return wrap(ansiBold, text) }
func Dim(text string) string    { return wrap(ansiDim, text) }
func Cyan(text string) string   { return wrap(ansiCyan, text) }
func Green(text string) string  { return wrap(ansiGreen, text) }
func Yellow(text string) string { return wrap(ansiYellow, text) }

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func visibleLen(s string) int {
	return len(ansiRe.ReplaceAllString(s, ""))
}

// Banner returns the startup splash screen for the REPL.
func Banner(version string) string {
	c := func(s string) string { return wrap(ansiCyan, s) }
	d := func(s string) string { return wrap(ansiDim, s) }

	const inner = 70

	box := func(content string) string {
		pad := inner - visibleLen(content)
		if pad < 0 {
			pad = 0
		}
		return d("**") + content + strings.Repeat(" ", pad) + d("**")
	}

	s := c("*")
	sp := c("* * *")
	q := c("****")
	e := c("********")
	el := c("***********")

	// Exact reproduction of the user's ASCII art with proper spacing
	art := []string{
		box(""),
		box("         " + s + "               " + s + "               " + s + "               " + s + "            "),
		box("       " + sp + "           " + sp + "           " + sp + "           " + sp + "          "),
		box("         " + s + "               " + s + "               " + s + "               " + s + "            "),
		box(""),
		box("                                                    " + q + "              "),
		box("                                                    " + q + "              "),
		box("        " + q + "    " + q + "     " + e + "        " + e + "   " + q + "              "),
		box("         " + q + "  " + q + "            " + q + "            " + q + " " + e + "          "),
		box("          " + e + "      " + el + "    " + el + "  " + q + "  " + q + "        "),
		box("               " + q + "    " + q + "    " + q + "   " + q + "    " + q + "  " + q + "  " + q + "        "),
		box("             " + q + "       " + el + "    " + el + "  " + q + "  " + q + "        "),
		box(""),
		box("         " + s + "               " + s + "               " + s + "               " + s + "            "),
		box("       " + sp + "           " + sp + "           " + sp + "           " + sp + "          "),
		box("         " + s + "               " + s + "               " + s + "               " + s + "            "),
		box(""),
		box("      " + Bold("LOCAL-FIRST ::: VENDOR-FREE ::: STANDARDS OVER REINVENTION") + "      "),
	}

	border := strings.Repeat("*", 74)

	var buf strings.Builder
	buf.WriteString("\n")
	for _, line := range art {
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	buf.WriteString(d(border))
	buf.WriteString("\n")
	buf.WriteString("  " + Dim(version))
	buf.WriteString("\n\n\n")

	return buf.String()
}

func Prompt() string {
	return Bold("yaah") + " " + Cyan("❯") + " "
}
