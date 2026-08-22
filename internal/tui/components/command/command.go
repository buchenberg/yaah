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
func Parse(input string) Cmd {
	input = strings.TrimSpace(input)
	switch {
	case input == "q" || input == "quit" || input == "exit":
		return CmdQuit
	case input == "clear":
		return CmdClear
	case input == "h" || input == "help":
		return CmdHelp
	case input == "compact":
		return CmdCompact
	case input == "stop":
		return CmdStop
	case strings.HasPrefix(input, "steer "):
		return CmdSteer
	case strings.HasPrefix(input, "followup "):
		return CmdFollowUp
	case input == "verbose":
		return CmdVerbose
	case strings.HasPrefix(input, "search "):
		return CmdSearch
	case input == "top":
		return CmdTop
	case input == "bottom":
		return CmdBottom
	case input == "banner":
		return CmdBanner
	case input == "roles":
		return CmdReloadRoles
	case input == "login":
		return CmdLogin
	case input == "logout":
		return CmdLogout
	case input == "session":
		return CmdSession
	case input == "mcp":
		return CmdMCP
	case strings.HasPrefix(input, "model"):
		return CmdModel
	default:
		return CmdNone
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
