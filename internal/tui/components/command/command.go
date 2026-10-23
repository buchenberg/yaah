// Package command implements the colon command parser and command palette for the TUI.
//
// Pressing Ctrl+P shows a list-based command palette. Commands mirror the
// REPL slash commands with additional TUI-specific commands.
package command

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui/components/modal"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Cmd is a recognized colon command.
type Cmd int

const (
	CmdNone Cmd = iota
	CmdQuit
	CmdClear
	CmdHelp
	CmdCompact
	CmdStop
	CmdSteer
	CmdFollowUp
	CmdVerbose
	CmdSearch
	CmdTop
	CmdBottom
	CmdBanner
	CmdReloadRoles
	CmdLogin
	CmdLogout
	CmdSession
	CmdMCP
	CmdModel
)

// Parse maps a colon command string (without the leading ":") to a Cmd.
//
// A bare command name parses too: Parse("steer") is CmdSteer with no argument,
// so callers can report a missing argument instead of an unknown command.
func Parse(input string) Cmd {
	cmd, _ := Split(input)
	return cmd
}

// commandNames maps every recognized command name to its Cmd. It is the single
// table behind Split and Name, so parsing and printing cannot drift apart.
var commandNames = []struct {
	name string
	cmd  Cmd
}{
	{"q", CmdQuit}, {"quit", CmdQuit}, {"exit", CmdQuit},
	{"clear", CmdClear},
	{"h", CmdHelp}, {"help", CmdHelp},
	{"compact", CmdCompact},
	{"stop", CmdStop},
	{"steer", CmdSteer},
	{"followup", CmdFollowUp},
	{"verbose", CmdVerbose},
	{"search", CmdSearch},
	{"top", CmdTop},
	{"bottom", CmdBottom},
	{"banner", CmdBanner},
	{"roles", CmdReloadRoles},
	{"login", CmdLogin},
	{"logout", CmdLogout},
	{"session", CmdSession},
	{"mcp", CmdMCP},
	{"model", CmdModel},
}

// takesArg reports whether cmd consumes the rest of the line.
func takesArg(cmd Cmd) bool {
	switch cmd {
	case CmdSteer, CmdFollowUp, CmdSearch, CmdModel:
		return true
	default:
		return false
	}
}

// Split parses a colon command string (without the leading ":") into its
// command and argument.
//
// The argument is empty for commands that take none, and also when a required
// argument was omitted; RequiresArg tells those two cases apart. Unrecognized
// input yields CmdNone.
func Split(input string) (Cmd, string) {
	name, arg, _ := strings.Cut(strings.TrimSpace(input), " ")
	arg = strings.TrimSpace(arg)
	for _, e := range commandNames {
		if e.name == name {
			if !takesArg(e.cmd) {
				return e.cmd, ""
			}
			return e.cmd, arg
		}
	}
	return CmdNone, ""
}

// Name returns the canonical name of cmd — the first entry in the command
// table, so aliases print as their primary spelling. It is empty for CmdNone.
func Name(cmd Cmd) string {
	for _, e := range commandNames {
		if e.cmd == cmd {
			return e.name
		}
	}
	return ""
}

// RequiresArg reports whether cmd is useless without an argument.
func RequiresArg(cmd Cmd) bool {
	switch cmd {
	case CmdSteer, CmdFollowUp, CmdSearch:
		return true
	default:
		return false
	}
}

// Usage returns the canonical invocation for cmd, shown when an argument is
// missing. It is empty for commands that take no argument.
func Usage(cmd Cmd) string {
	switch cmd {
	case CmdSteer:
		return ":steer <text>"
	case CmdFollowUp:
		return ":followup <text>"
	case CmdSearch:
		return ":search <query>"
	case CmdModel:
		return ":model [name]"
	default:
		return ""
	}
}

// Entry is a labelled command with description shown in the palette.
type Entry struct {
	Label string
	Desc  string
	Cmd   Cmd
}

// DefaultEntries returns the standard command palette entries.
func DefaultEntries() []Entry {
	return []Entry{
		{"help", "Show keybindings and commands", CmdHelp},
		{"clear", "Clear conversation", CmdClear},
		{"compact", "Compact context window", CmdCompact},
		{"stop", "Abort running agent", CmdStop},
		{"steer", "Inject steering text (requires arg)", CmdSteer},
		{"followup", "Queue text as a follow-up message (requires arg)", CmdFollowUp},
		{"model", "Switch model", CmdModel},
		{"search", "Search messages (requires arg)", CmdSearch},
		{"verbose", "Toggle verbose mode", CmdVerbose},
		{"banner", "Toggle banner", CmdBanner},
		{"top", "Scroll to top", CmdTop},
		{"bottom", "Scroll to bottom", CmdBottom},
		{"quit", "Exit yaah", CmdQuit},
	}
}

// PageName is the tview Pages name used for the command list modal.
const PageName = "cmdlist_modal"

// ShowList displays the command palette as a list-based modal.
// onSelect is called with the chosen command when an entry is selected.
// onDismiss is called when the modal is dismissed with Escape.
func ShowList(app *tview.Application, pages *tview.Pages, entries []Entry, onSelect func(Cmd), onDismiss func()) {
	list := tview.NewList().
		ShowSecondaryText(true).
		SetHighlightFullLine(true).
		SetWrapAround(false)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSecondaryTextColor(tcell.ColorGray).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tcell.ColorDarkCyan)

	for _, e := range entries {
		e := e
		list.AddItem(e.Label, e.Desc, 0, func() {
			pages.RemovePage(PageName)
			onSelect(e.Cmd)
		})
	}

	list.SetBorder(true).
		SetTitle(" Commands ").
		SetTitleColor(tcell.ColorYellow)

	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			pages.RemovePage(PageName)
			onDismiss()
			return nil
		}
		return ev
	})

	flex := modal.Wrap(list)

	pages.AddPage(PageName, flex, true, true)
	app.SetFocus(list)
}
