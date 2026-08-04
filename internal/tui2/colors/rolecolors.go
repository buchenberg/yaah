package colors

// Sub-agent role colors. Each maps to a tview hex color for the role label
// in the sub-agent block. The robot icon (🤖) is not colored — these apply
// to the role name text in the collapsed and expanded block headers.
const (
	RoleAnalyst         = "#00afff" // cyan
	RoleDeveloper       = "#00d700" // green
	RoleReviewer        = "#ffd700" // yellow
	RoleTester          = "#d700ff" // magenta
	RoleChecker         = "#ffffff" // white
	RoleCounter         = "#ff8700" // orange
	RoleSecurityAuditor = "#d70000" // red
	RoleGoatJokeTeller  = "#ff5faf" // hot pink
	RoleGolangDeveloper = "#00af87" // teal
	RoleGolangTester    = "#87ff00" // lime
	RoleGrump           = "#808080" // grey
)

// RoleHex maps a sub-agent role name to its hex color.
func RoleHex(role string) string {
	switch role {
	case "analyst":
		return RoleAnalyst
	case "developer":
		return RoleDeveloper
	case "reviewer":
		return RoleReviewer
	case "tester":
		return RoleTester
	case "checker":
		return RoleChecker
	case "counter":
		return RoleCounter
	case "security_auditor":
		return RoleSecurityAuditor
	case "goat-joke-teller":
		return RoleGoatJokeTeller
	case "golang-developer":
		return RoleGolangDeveloper
	case "golang-tester":
		return RoleGolangTester
	case "grump":
		return RoleGrump
	default:
		return Accent
	}
}

// ToolHex maps a tool name to its hex color.
func ToolHex(tool string) string {
	switch tool {
	case "read":
		return "#87afff" // light blue
	case "write", "edit":
		return "#ffd700" // gold
	case "delete":
		return "#d70000" // red
	case "patch", "sed", "replace":
		return "#af5fff" // violet
	case "grep", "glob":
		return "#ff8700" // orange
	case "ls", "file_info":
		return "#5fd700" // green
	case "http", "webfetch":
		return "#00afff" // cyan
	case "bash":
		return "#00d700" // terminal green
	case "powershell":
		return "#0087ff" // windows blue
	case "git", "diff", "bisect":
		return "#d78700" // brown-orange
	case "go_test", "go_outline", "go_refactor", "staticcheck", "go_mod":
		return "#00afd7" // go teal
	case "json_query":
		return "#afaf00" // olive
	case "calculate":
		return "#af5faf" // mauve
	case "todowrite":
		return "#5faf00" // check green
	case "question":
		return "#ffaf00" // amber
	case "plan", "list_subagents":
		return "#5f87af" // slate blue
	case "skill":
		return "#ff5f5f" // coral
	case "role":
		return "#d787ff" // light purple
	case "memory_search", "memory_add", "memory_update", "memory_delete", "memory_search_sessions":
		return "#ff87af" // pink
	case "background_process":
		return "#afafaf" // silver
	case "task":
		return "#5fafd7" // sky
	default:
		return Dim
	}
}
