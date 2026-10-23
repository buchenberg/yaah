package tui

import (
	"github.com/buchenberg/yaah/internal/tui/components/command"
)

// HandleCommand dispatches a colon command.
func (t *App) HandleCommand(cmd command.Cmd, arg string) {
	switch cmd {
	case command.CmdQuit:
		t.Stop()
	case command.CmdClear:
		t.doClear()
	case command.CmdHelp:
		t.ShowHelp()
	case command.CmdCompact:
		if t.OnCompact != nil {
			t.OnCompact()
		}
	case command.CmdStop:
		if t.OnStop != nil {
			t.OnStop()
		} else if t.OnAbort != nil {
			t.OnAbort()
		}
	case command.CmdSteer:
		if arg == "" {
			t.SetEphemeral("usage: " + command.Usage(command.CmdSteer))
			return
		}
		// Steering targets a turn in flight. With nothing running the text would
		// sit in the steer channel until SteerMiddleware.PrepareStep drains it
		// into the FIRST step of the next turn — silently steering an unrelated,
		// later run. Refuse instead.
		if !t.agentActive {
			t.SetEphemeral("no turn is running — nothing to steer")
			return
		}
		if t.OnSteer == nil {
			t.SetEphemeral("steering is unavailable in this session")
			return
		}
		t.OnSteer(arg)
	case command.CmdFollowUp:
		if arg == "" {
			t.SetEphemeral("usage: " + command.Usage(command.CmdFollowUp))
			return
		}
		// With no turn running there is nothing to follow up on, and the text
		// would otherwise leak into a later turn's follow-up drain. Sending it
		// as a prompt is what the user means.
		if !t.agentActive {
			if t.OnSubmit == nil {
				t.SetEphemeral("no turn is running and there is nowhere to send this")
				return
			}
			t.SetEphemeral("no turn is running — sent as a message")
			t.OnSubmit(arg)
			return
		}
		if t.OnFollowUp == nil {
			t.SetEphemeral("follow-ups are unavailable in this session")
			return
		}
		t.OnFollowUp(arg)
	case command.CmdVerbose:
		t.verbose = !t.verbose
		t.refreshMessages()
	case command.CmdTop:
		t.Messages.ScrollTo(0, 0)
	case command.CmdBottom:
		t.Messages.ScrollToEnd()
	case command.CmdBanner:
		t.toggleBanner()
	case command.CmdModel:
		t.ShowModelPicker(t.availableModels, t.providerNames, func(model string) {
			if t.OnModelSelect != nil {
				t.OnModelSelect(model)
			}
		})
	case command.CmdSearch:
		if arg == "" {
			t.SetEphemeral("usage: " + command.Usage(command.CmdSearch))
			return
		}
		t.searchMessages(arg)
	default:
		// Reachable for commands that are declared and parseable but have no
		// handler yet (roles, login, logout, session, mcp). Say so rather than
		// accepting the input and doing nothing.
		if name := command.Name(cmd); name != "" {
			t.SetEphemeral(":" + name + " is not available in this session")
		}
	}
}

// labelFor returns the palette label for cmd, or "" if cmd has no entry.
func labelFor(entries []command.Entry, cmd command.Cmd) string {
	for _, e := range entries {
		if e.Cmd == cmd {
			return e.Label
		}
	}
	return ""
}

func (t *App) showCommandList() {
	t.hideSuggestions()
	entries := append([]command.Entry{}, command.DefaultEntries()...)
	command.ShowList(t.App, t.Pages, entries, func(cmd command.Cmd) {
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
		if command.RequiresArg(cmd) {
			// The palette cannot collect an argument, so hand the user the
			// syntax in the input box rather than dispatching a silent no-op.
			t.Input.SetText(":"+labelFor(entries, cmd)+" ", true)
			t.SetEphemeral("usage: " + command.Usage(cmd))
			return
		}
		t.HandleCommand(cmd, "")
	}, func() {
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
	})
	t.focus = focusCommandPalette
}

func (t *App) toggleCommandPalette() {
	if t.Pages.HasPage(command.PageName) {
		t.Pages.RemovePage(command.PageName)
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
	} else {
		t.showCommandList()
	}
}
