package colors

import (
	"os"
	"strings"
)

// Theme holds all display colors for the TUI. Zero hardcoded hex strings
// exist outside this struct — edit here to change the entire look.
type Theme struct {
	// --- Text hierarchy ---
	Heading   string // section headers, bold primary text (cyan)
	Detail    string // info pane data values (lavender)
	Secondary string // descriptive text — task descs, elapsed times (muted purple)
	Dim       string // faded chrome — pipe separators, placeholder text (gray)

	// --- Message roles ---
	User   string // user messages (hot pink)
	System string // system/info messages (gray)
	Error  string // error messages (red)

	// --- Status ---
	Connected string // active/connected indicator (green)

	// --- Backgrounds ---
	ToolBg string // collapsible tool-block background
	CodeBg string // code-block background

	// --- Borders ---
	InputBorder     string // input field border (hot pink)
	InfoPaneBorder  string // info pane border (cyan)
	TasksPaneBorder string // tasks pane border (yellow)
	SubAgentsBorder string // sub-agents pane border (lavender)

	// --- Modifiers ---
	NoColor bool // set when NO_COLOR is in the environment

	// --- Role / tool color maps ---
	ToolColors map[string]string // tool name → hex
	RoleColors map[string]string // role name → hex

	// Tviewmd overrides — applied to markdown rendering in conversation.
	MDHeading [6]string
	MDLink    string
	MDCodeFG  string
	MDQuoteFG string
	MDHr      string
}

func newToolColors() map[string]string {
	return map[string]string{
		"read":                   "#00ffff",
		"write":                  "#ffff00",
		"edit":                   "#ffff00",
		"delete":                 "#ff0044",
		"patch":                  "#ff00ff",
		"sed":                    "#ff00ff",
		"replace":                "#ff00ff",
		"grep":                   "#ff8c00",
		"glob":                   "#ff8c00",
		"ls":                     "#00ff88",
		"file_info":              "#00ff88",
		"http":                   "#00ffff",
		"webfetch":               "#00ffff",
		"bash":                   "#00ff88",
		"powershell":             "#4488ff",
		"git":                    "#ff8c00",
		"diff":                   "#ff8c00",
		"bisect":                 "#ff8c00",
		"go_test":                "#00ffff",
		"go_outline":             "#00ffff",
		"go_refactor":            "#00ffff",
		"staticcheck":            "#00ffff",
		"go_mod":                 "#00ffff",
		"json_query":             "#ffff00",
		"calculate":              "#ff00ff",
		"todowrite":              "#00ff88",
		"question":               "#ff8c00",
		"plan":                   "#4488ff",
		"list_subagents":         "#4488ff",
		"skill":                  "#ff0044",
		"role":                   "#ff00ff",
		"memory_search":          "#ff66aa",
		"memory_add":             "#ff66aa",
		"memory_update":          "#ff66aa",
		"memory_delete":          "#ff66aa",
		"memory_search_sessions": "#ff66aa",
		"background_process":     "#cc99ff",
		"task":                   "#00ffff",
	}
}

func newRoleColors() map[string]string {
	return map[string]string{
		"analyst":          "#00ffff",
		"developer":        "#00ff88",
		"reviewer":         "#ffff00",
		"tester":           "#ff00ff",
		"checker":          "#ffffff",
		"counter":          "#ff8c00",
		"security_auditor": "#ff0044",
		"goat-joke-teller": "#ff4488",
		"golang-developer": "#00ff88",
		"golang-tester":    "#88ff00",
		"grump":            "#cc99ff",
	}
}

func NewDarkTheme() Theme {
	return Theme{
		Heading:         "#00ffff",
		Detail:          "#cc99ff",
		Secondary:       "#9988bb",
		Dim:             "#888888",
		User:            "#ff00ff",
		System:          "#888888",
		Error:           "#ff5555",
		Connected:       "#00ffff",
		ToolBg:          "#1a1a2e",
		CodeBg:          "#1e1e2e",
		InputBorder:     "#ff00ff",
		InfoPaneBorder:  "#00ffff",
		TasksPaneBorder: "#ffff00",
		SubAgentsBorder: "#cc99ff",
		ToolColors:      newToolColors(),
		RoleColors:      newRoleColors(),
		MDHeading: [6]string{
			"#00ffff", "#00ffff", "#00ffff",
			"#00ffff", "#00ffff", "#00ffff",
		},
		MDLink:    "#00ffff",
		MDCodeFG:  "#9988bb",
		MDQuoteFG: "#9988bb",
		MDHr:      "#888888",
	}
}

func NewLightTheme() Theme {
	return Theme{
		Heading:         "#005faf",
		Detail:          "#af005f",
		Secondary:       "#775588",
		Dim:             "#9e9e9e",
		User:            "#005faf",
		System:          "#666666",
		Error:           "#d70000",
		Connected:       "#005faf",
		ToolBg:          "#eeeeee",
		CodeBg:          "#f0f0f0",
		InputBorder:     "#d75f87",
		InfoPaneBorder:  "#005faf",
		TasksPaneBorder: "#7f6f00",
		SubAgentsBorder: "#7f3faf",
		ToolColors:      newToolColors(),
		RoleColors:      newRoleColors(),
		MDHeading: [6]string{
			"#005faf", "#005faf", "#005faf",
			"#005faf", "#005faf", "#005faf",
		},
		MDLink:    "#005faf",
		MDCodeFG:  "#775588",
		MDQuoteFG: "#775588",
		MDHr:      "#9e9e9e",
	}
}

func DetectTheme() Theme {
	var th Theme
	switch strings.ToLower(os.Getenv("YAARH_THEME")) {
	case "light":
		th = NewLightTheme()
	default:
		th = NewDarkTheme()
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		th.NoColor = true
	}
	return th
}

func (th *Theme) ToolHex(name string) string {
	if c, ok := th.ToolColors[name]; ok {
		return c
	}
	return th.Dim
}

func (th *Theme) RoleHex(role string) string {
	if c, ok := th.RoleColors[role]; ok {
		return c
	}
	return th.Heading
}

func (th *Theme) Tag(color, text string) string {
	if th.NoColor {
		return text
	}
	return "[" + color + "]" + text + "[-]"
}

func (th *Theme) TagBold(color, text string) string {
	if th.NoColor {
		return text
	}
	return "[" + color + "::b]" + text + "[-:-:-]"
}

func (th *Theme) DimTag() string {
	if th.NoColor {
		return ""
	}
	return "[" + th.Dim + "::d" + "]"
}

func (th *Theme) SecondaryTag() string {
	if th.NoColor {
		return ""
	}
	return "[" + th.Secondary + "]"
}

func (th *Theme) ColorTag(hex string) string {
	if th.NoColor {
		return ""
	}
	return "[" + hex + "]"
}

func (th *Theme) ResetTag() string {
	if th.NoColor {
		return ""
	}
	return "[-]"
}
