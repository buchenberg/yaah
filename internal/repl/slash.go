package repl

// SlashCommand represents a recognized slash command from user input.
type SlashCommand int

const (
	CmdNone SlashCommand = iota
	CmdExit
	CmdClear
	CmdHelp
	CmdCompact
	CmdReloadRoles
	CmdLogin
	CmdLogout
)

// ParseSlashCommand checks if input is a recognized slash command.
// Returns CmdNone for regular text input and unrecognized commands.
func ParseSlashCommand(input string) SlashCommand {
	switch input {
	case "/exit", "/quit":
		return CmdExit
	case "/clear":
		return CmdClear
	case "/?", "/help":
		return CmdHelp
	case "/compact":
		return CmdCompact
	case "/reload-roles", "/roles":
		return CmdReloadRoles
	case "/login":
		return CmdLogin
	case "/logout":
		return CmdLogout
	default:
		return CmdNone
	}
}
