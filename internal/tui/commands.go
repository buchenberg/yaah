package tui

import (
	"github.com/buchenberg/yaah/internal/tui/components/command"
)

// HandleCommand dispatches a colon command. It reports whether the command
// consumed the input — false when it only complained (missing argument, no turn
// to steer, unwired command), so the caller can keep the text for editing
// instead of silently deleting what the user typed.
func (t *App) HandleCommand(cmd command.Cmd, arg string) bool {
	switch cmd {
	case command.CmdQuit:
		t.Stop()
		return true
	case command.CmdClear:
		t.doClear()
		return true
	case command.CmdHelp:
		t.ShowHelp()
		return true
	case command.CmdCompact:
		if t.OnCompact != nil {
			t.OnCompact()
			return true
		}
		return false
	case command.CmdStop:
		if t.OnStop != nil {
			t.OnStop()
			return true
		}
		if t.OnAbort != nil {
			t.OnAbort()
			return true
		}
		return false
	case command.CmdSteer:
		if arg == "" {
			t.SetEphemeral("usage: " + command.Usage(command.CmdSteer))
			return false
		}
		// Steering targets a turn in flight. With nothing running the text would
		// sit in the steer channel until SteerMiddleware.PrepareStep drains it
		// into the FIRST step of the next turn — silently steering an unrelated,
		// later run. Refuse instead.
		if !t.agentActive {
			t.SetEphemeral("no turn is running — nothing to steer")
			return false
		}
		if t.OnSteer == nil {
			t.SetEphemeral("steering is unavailable in this session")
			return false
		}
		t.OnSteer(arg)
		return true
	case command.CmdFollowUp:
		if arg == "" {
			t.SetEphemeral("usage: " + command.Usage(command.CmdFollowUp))
			return false
		}
		// With no turn running there is nothing to follow up on, and the text
		// would otherwise leak into a later turn's follow-up drain. Sending it
		// as a prompt is what the user means.
		if !t.agentActive {
			if t.OnSubmit == nil {
				t.SetEphemeral("no turn is running and there is nowhere to send this")
				return false
			}
			t.SetEphemeral("no turn is running — sent as a message")
			t.OnSubmit(arg)
			return true
		}
		if t.OnFollowUp == nil {
			t.SetEphemeral("follow-ups are unavailable in this session")
			return false
		}
		t.OnFollowUp(arg)
		return true
	case command.CmdVerbose:
		t.verbose = !t.verbose
		t.refreshMessages()
		return true
	case command.CmdTop:
		t.Messages.ScrollTo(0, 0)
		return true
	case command.CmdBottom:
		t.Messages.ScrollToEnd()
		return true
	case command.CmdBanner:
		t.toggleBanner()
		return true
	case command.CmdPlan, command.CmdBuild:
		mode := "plan"
		if cmd == command.CmdBuild {
			mode = "build"
		}
		if t.OnModeSelect != nil {
			t.OnModeSelect(mode)
		}
		return true
	case command.CmdModel:
		t.ShowModelPicker(t.availableModels, t.providerNames, func(model string) {
			if t.OnModelSelect != nil {
				t.OnModelSelect(model)
			}
		})
		return true
	case command.CmdSearch:
		if arg == "" {
			t.SetEphemeral("usage: " + command.Usage(command.CmdSearch))
			return false
		}
		t.searchMessages(arg)
		return true
	default:
		// Reachable for commands that are declared and parseable but have no
		// handler yet (roles, login, logout, session, mcp). Say so rather than
		// accepting the input and doing nothing.
		if name := command.Name(cmd); name != "" {
			t.SetEphemeral(":" + name + " is not available in this session")
		}
		return false
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
		t.HandleCommand(cmd, "") // palette has no argument to consume
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
