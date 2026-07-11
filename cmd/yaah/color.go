package yaah

import "os"

// Bold wraps text in ANSI bold (respects NO_COLOR).
func Bold(s string) string {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

// Dim wraps text in ANSI dim (respects NO_COLOR).
func Dim(s string) string {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}
