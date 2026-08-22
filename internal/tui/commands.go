package tui2

import (
	"github.com/buchenberg/yaah/internal/tui2/components/command"
)

// HandleCommand dispatches a colon command.
func (t *TUI2) HandleCommand(cmd command.Cmd, arg string) {
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
		if t.OnSteer != nil && arg != "" {
			t.OnSteer(arg)
		}
	case command.CmdFollowUp:
		if t.OnFollowUp != nil && arg != "" {
			t.OnFollowUp(arg)
		}
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
		t.searchMessages(arg)
	default:
	}
}

func (t *TUI2) showCommandList() {
	entries := append([]command.Entry{}, command.DefaultEntries()...)
	command.ShowList(t.App, t.Pages, entries, func(cmd command.Cmd) {
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
		t.HandleCommand(cmd, "")
	}, func() {
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
	})
	t.focus = focusCommandPalette
}

func (t *TUI2) toggleCommandPalette() {
	if t.Pages.HasPage(command.PageName) {
		t.Pages.RemovePage(command.PageName)
		t.App.SetFocus(t.Input)
		t.focus = focusNormal
	} else {
		t.showCommandList()
	}
}
