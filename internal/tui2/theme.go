package tui2

import (
	"os"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Theme holds the terminal color scheme used by all TUI2 components.
// When the zero value is used, components fall back to color defaults via the
// colors package. Light-mode support gets a second Theme value later.
type Theme struct {
	Accent string
	Dim    string
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
// Respects NO_COLOR by returning the zero theme (which means "use defaults,
// no overrides"). Terminal background detection is a TODO for light-mode.
func DetectTheme() Theme {
	if os.Getenv("NO_COLOR") != "" {
		return Theme{}
	}
	return DefaultTheme()
}
