package colors

import (
	"os"
	"strings"
)

type Theme struct {
	Accent      string
	Dim         string
	User        string
	System      string
	Error       string
	ToolBg      string
	CodeBg      string
	InputBorder string
	NoColor     bool

	ToolColors map[string]string
	RoleColors map[string]string
}

func newToolColors() map[string]string {
	return map[string]string{
		"read":                   "#87afff",
		"write":                  "#ffd700",
		"edit":                   "#ffd700",
		"delete":                 "#d70000",
		"patch":                  "#af5fff",
		"sed":                    "#af5fff",
		"replace":                "#af5fff",
		"grep":                   "#ff8700",
		"glob":                   "#ff8700",
		"ls":                     "#5fd700",
		"file_info":              "#5fd700",
		"http":                   "#00afff",
		"webfetch":               "#00afff",
		"bash":                   "#00d700",
		"powershell":             "#0087ff",
		"git":                    "#d78700",
		"diff":                   "#d78700",
		"bisect":                 "#d78700",
		"go_test":                "#00afd7",
		"go_outline":             "#00afd7",
		"go_refactor":            "#00afd7",
		"staticcheck":            "#00afd7",
		"go_mod":                 "#00afd7",
		"json_query":             "#afaf00",
		"calculate":              "#af5faf",
		"todowrite":              "#5faf00",
		"question":               "#ffaf00",
		"plan":                   "#5f87af",
		"list_subagents":         "#5f87af",
		"skill":                  "#ff5f5f",
		"role":                   "#d787ff",
		"memory_search":          "#ff87af",
		"memory_add":             "#ff87af",
		"memory_update":          "#ff87af",
		"memory_delete":          "#ff87af",
		"memory_search_sessions": "#ff87af",
		"background_process":     "#afafaf",
		"task":                   "#5fafd7",
	}
}

func newRoleColors() map[string]string {
	return map[string]string{
		"analyst":          "#00afff",
		"developer":        "#00d700",
		"reviewer":         "#ffd700",
		"tester":           "#d700ff",
		"checker":          "#ffffff",
		"counter":          "#ff8700",
		"security_auditor": "#d70000",
		"goat-joke-teller": "#ff5faf",
		"golang-developer": "#00af87",
		"golang-tester":    "#87ff00",
		"grump":            "#808080",
	}
}

func NewDarkTheme() Theme {
	return Theme{
		Accent:      "#00afff",
		Dim:         "#5f5f5f",
		User:        "#00afff",
		System:      "#888888",
		Error:       "#ff5555",
		ToolBg:      "#1a1a2e",
		CodeBg:      "#1e1e2e",
		InputBorder: "#ff87af",
		ToolColors:  newToolColors(),
		RoleColors:  newRoleColors(),
	}
}

func NewLightTheme() Theme {
	return Theme{
		Accent:      "#005faf",
		Dim:         "#9e9e9e",
		User:        "#005faf",
		System:      "#666666",
		Error:       "#d70000",
		ToolBg:      "#eeeeee",
		CodeBg:      "#f0f0f0",
		InputBorder: "#d75f87",
		ToolColors:  newToolColors(),
		RoleColors:  newRoleColors(),
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
	return th.Accent
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
	return "[" + color + "::b]" + text + "[-]"
}

func (th *Theme) DimTag() string {
	if th.NoColor {
		return ""
	}
	return "[" + th.Dim + "::d" + "]"
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
