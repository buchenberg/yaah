package repl

import "strings"

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
	CmdAgents
	CmdAgent
	CmdMode
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
	case "/agents":
		return CmdAgents
	}
	// /agent takes an optional persona name: the bare command reports the
	// current persona, a name switches this session. Checked after the
	// exact switch so "/agents" is not read as "/agent".
	if input == "/agent" || strings.HasPrefix(input, "/agent ") {
		return CmdAgent
	}
	// /mode takes an optional mode word: the bare command reports the
	// current mode axis, a word switches this session.
	if input == "/mode" || strings.HasPrefix(input, "/mode ") {
		return CmdMode
	}
	return CmdNone
}

// SlashAgentArg extracts the persona name from an /agent input line,
// "" for the bare command. ParseSlashCommand must have returned
// CmdAgent for the input.
func SlashAgentArg(input string) string {
	return strings.TrimSpace(strings.TrimPrefix(input, "/agent"))
}

// SlashModeArg extracts the mode word from a /mode input line, "" for
// the bare command. ParseSlashCommand must have returned CmdMode for
// the input.
func SlashModeArg(input string) string {
	return strings.TrimSpace(strings.TrimPrefix(input, "/mode"))
}
