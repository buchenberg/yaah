package tui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Theme holds semantic color tokens for every visual element in the TUI.
// Values are ANSI color strings (e.g. "39", "#7D56F4"). An empty string
// means "terminal default" (used for NO_COLOR / monochrome mode).
// Styles are reassigned via ApplyTheme at startup or on theme switch.
type Theme struct {
	Title      string
	User       string
	UserBg     string
	Assistant  string
	Tool       string
	ToolBg     string
	System     string
	SystemBg   string
	Status     string
	StatusBg   string
	Spinner    string
	Code       string
	Thinking   string
	Toggle     string
	ListBullet string
	ListItem   string
	Tree       string
	TreeItem   string
	CmdBorder  string
	CmdName    string
	CmdDesc    string
}

// Default dark theme — the original yaah palette.
var DarkTheme = Theme{
	Title:      "39",
	User:       "14",
	UserBg:     "24",
	Assistant:  "252",
	Tool:       "243",
	ToolBg:     "236",
	System:     "243",
	SystemBg:   "236",
	Status:     "243",
	StatusBg:   "236",
	Spinner:    "39",
	Code:       "214",
	Thinking:   "240",
	Toggle:     "240",
	ListBullet: "99",
	ListItem:   "252",
	Tree:       "240",
	TreeItem:   "252",
	CmdBorder:  "99",
	CmdName:    "39",
	CmdDesc:    "243",
}

// Light theme — tuned for light terminal backgrounds.
var LightTheme = Theme{
	Title:      "25",
	User:       "26",
	UserBg:     "153",
	Assistant:  "235",
	Tool:       "244",
	ToolBg:     "251",
	System:     "244",
	SystemBg:   "251",
	Status:     "244",
	StatusBg:   "251",
	Spinner:    "25",
	Code:       "130",
	Thinking:   "246",
	Toggle:     "246",
	ListBullet: "55",
	ListItem:   "235",
	Tree:       "246",
	TreeItem:   "235",
	CmdBorder:  "55",
	CmdName:    "25",
	CmdDesc:    "244",
}

// catppuccinMocha maps Catppuccin Mocha palette to 256-color ANSI
// approximations. Full palette support requires a truecolor terminal.
var catppuccinMocha = Theme{
	Title:      "39",
	User:       "14",
	UserBg:     "24",
	Assistant:  "252",
	Tool:       "243",
	ToolBg:     "236",
	System:     "243",
	SystemBg:   "236",
	Status:     "243",
	StatusBg:   "236",
	Spinner:    "39",
	Code:       "214",
	Thinking:   "240",
	Toggle:     "240",
	ListBullet: "99",
	ListItem:   "252",
	Tree:       "240",
	TreeItem:   "252",
	CmdBorder:  "99",
	CmdName:    "39",
	CmdDesc:    "243",
}

// namedThemes holds extra themes beyond the built-in dark/light.
var namedThemes = map[string]Theme{
	"catppuccin-mocha": catppuccinMocha,
	"catppuccin-latte": LightTheme,
	"dracula":          DarkTheme,
	"nord":             DarkTheme,
	"gruvbox":          DarkTheme,
	"tokyo-night":      DarkTheme,
}

// monochromeTheme uses empty strings for all colors — the terminal's
// default foreground and background. Only weight (bold) and shape
// (italic) attributes are preserved.
var monochromeTheme = Theme{}

// colorOrNone returns lipgloss.NoColor{} for empty strings (terminal default)
// and lipgloss.Color(s) for explicit color strings.
func colorOrNone(s string) color.Color {
	if s == "" {
		return lipgloss.NoColor{}
	}
	return lipgloss.Color(s)
}

// init applies the default dark theme so styles are never zero-valued.
func init() {
	ApplyTheme(DarkTheme)
}

// ApplyTheme reassigns all package-level style variables from the given theme.
func ApplyTheme(t Theme) {
	titleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(colorOrNone(t.Title))

	userStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(colorOrNone(t.User))

	userBgStyle = lipgloss.NewStyle().
		Background(colorOrNone(t.UserBg))

	assistantStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Assistant))

	toolStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Tool))

	toolBgStyle = lipgloss.NewStyle().
		Background(colorOrNone(t.ToolBg))

	systemStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.System))

	systemBgStyle = lipgloss.NewStyle().
		Background(colorOrNone(t.SystemBg))

	statusStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Status)).
		Background(colorOrNone(t.StatusBg)).
		Padding(0, 1)

	spinnerStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Spinner))

	codeStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Code))

	boldStyle = lipgloss.NewStyle().
		Bold(true)

	italicStyle = lipgloss.NewStyle().
		Italic(true)

	thinkingStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Thinking)).
		Italic(true)

	toggleStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Toggle))

	listBulletStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.ListBullet)).
		MarginRight(1)

	listItemStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.ListItem))

	treeStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.Tree))

	treeItemStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.TreeItem))

	commandPaletteStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorOrNone(t.CmdBorder)).
		Padding(0, 1)

	commandNameStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(colorOrNone(t.CmdName)).
		Width(12)

	commandDescStyle = lipgloss.NewStyle().
		Foreground(colorOrNone(t.CmdDesc))
}

// DetectTheme picks the appropriate theme based on environment variables
// and terminal background detection, in priority order:
//  1. NO_COLOR → monochrome (no-color.org)
//  2. YAAH_THEME env var → named theme
//  3. Terminal background detection → dark or light
func DetectTheme() Theme {
	if v := os.Getenv("NO_COLOR"); v != "" {
		return monochromeTheme
	}

	if name := os.Getenv("YAAH_THEME"); name != "" {
		if t, ok := namedThemes[strings.ToLower(name)]; ok {
			return t
		}
	}

	if lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		return DarkTheme
	}
	return LightTheme
}
