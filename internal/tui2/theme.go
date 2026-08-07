package tui2

import (
	"os"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Theme holds the terminal color scheme used by all TUI2 components.
// When NoColor is true, renderers must omit all color tags.
type Theme struct {
	Accent  string
	Dim     string
	NoColor bool // set when NO_COLOR env is present
}

// DefaultTheme returns the dark-terminal theme used when no color overrides
// are active.
func DefaultTheme() Theme {
	return Theme{
		Accent: colors.Accent,
		Dim:    colors.Dim,
	}
}

// DetectTheme selects a Theme based on the current terminal environment.
// When NO_COLOR is set, NoColor is true and renderers must suppress all
// color-tag output. Terminal background detection is a TODO for light-mode.
func DetectTheme() Theme {
	if os.Getenv("NO_COLOR") != "" {
		return Theme{NoColor: true}
	}
	return DefaultTheme()
}
