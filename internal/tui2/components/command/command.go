// Package command implements the vim-style ":" command palette for TUI2.
//
// Pressing ":" slides a single-line input up from the bottom of the screen.
// Commands mirror the REPL slash commands (/exit, /clear, etc.) with
// additional TUI-specific commands.
package command

import (
	"strings"

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

// Palette is a vim-style ":" command input that appears at the bottom of the
// TUI when the user presses ":". It handles completion, history, and
// dispatches recognized commands via a callback.
type Palette struct {
	*tview.InputField

	onCommand func(cmd Cmd, arg string)
	history   []string
	histIdx   int
}

// Build creates the command palette input field.
func Build(onCommand func(cmd Cmd, arg string)) *Palette {
	p := &Palette{
		InputField: tview.NewInputField(),
		onCommand:  onCommand,
	}
	p.SetFieldBackgroundColor(tcell.ColorBlack)
	p.SetFieldTextColor(tcell.ColorWhite)
	p.SetLabel("  ")
	p.SetLabelColor(tcell.ColorYellow)
	p.SetPlaceholder("command (help, clear, compact, stop, steer, model, mcp, verbose, search...)")
	p.SetPlaceholderTextColor(tcell.ColorGray)

	p.SetDoneFunc(func(key tcell.Key) {
		input := strings.TrimSpace(p.GetText())
		if input == "" {
			return
		}
		p.history = append(p.history, input)
		p.histIdx = len(p.history)

		cmd := Parse(input)
		arg := ""
		switch {
		case cmd == CmdModel && strings.HasPrefix(input, "model "):
			arg = strings.TrimPrefix(input, "model ")
		case cmd == CmdSteer && strings.HasPrefix(input, "steer "):
			arg = strings.TrimPrefix(input, "steer ")
		case cmd == CmdFollowUp && strings.HasPrefix(input, "followup "):
			arg = strings.TrimPrefix(input, "followup ")
		case cmd == CmdSearch && strings.HasPrefix(input, "search "):
			arg = strings.TrimPrefix(input, "search ")
		}
		if cmd != CmdNone {
			p.onCommand(cmd, arg)
		}
	})

	p.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyUp:
			if len(p.history) > 0 && p.histIdx > 0 {
				p.histIdx--
				p.SetText(p.history[p.histIdx])
			}
			return nil
		case tcell.KeyDown:
			if p.histIdx < len(p.history)-1 {
				p.histIdx++
				p.SetText(p.history[p.histIdx])
			} else if p.histIdx == len(p.history)-1 {
				p.histIdx = len(p.history)
				p.SetText("")
			}
			return nil
		case tcell.KeyEscape:
			p.SetText("")
			return nil
		}
		return ev
	})

	return p
}
