package tui

import "charm.land/lipgloss/v2"

// roleColors maps sub-agent role names to their display color, matching
// the TUI2 palette (internal/tui2/colors/rolecolors.go). The color
// applies to the role label; the robot icon is not colored.
var roleColors = map[string]string{
	"analyst":          "#00afff", // cyan
	"developer":        "#00d700", // green
	"reviewer":         "#ffd700", // yellow
	"tester":           "#d700ff", // magenta
	"checker":          "#ffffff", // white
	"counter":          "#ff8700", // orange
	"security_auditor": "#d70000", // red
	"goat-joke-teller": "#ff5faf", // hot pink
	"golang-developer": "#00af87", // teal
	"golang-tester":    "#87ff00", // lime
	"grump":            "#808080", // grey
}

// roleStyle returns the bold, role-colored style for a sub-agent label.
// Unknown roles fall back to a plain bold style.
func roleStyle(role string) lipgloss.Style {
	if hex, ok := roleColors[role]; ok {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(hex))
	}
	return lipgloss.NewStyle().Bold(true)
}
