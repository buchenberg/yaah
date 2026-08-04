package tui2

import "github.com/gdamore/tcell/v2"

// Action is a named user action dispatched by the keymap.
type Action int

const (
	ActionNone Action = iota
	ActionQuit
	ActionClear
	ActionHelp
	ActionSearch
	ActionCommand
	ActionToggleReasoning
	ActionToggleTools
	ActionToggleSubAgents
	ActionSend
	ActionCancel
	ActionScrollUp
	ActionScrollDown
	ActionPageUp
	ActionPageDown
	ActionTop
	ActionBottom
	ActionNextPanel
	ActionPrevPanel
	ActionFocusInput
	ActionFocusMessages
	ActionFocusInfopane
)

// Binding maps a tcell key event to an Action with a display label and help
// text (used for the auto-generated help overlay).
type Binding struct {
	Key      tcell.Key
	Rune     rune
	Mod      tcell.ModMask
	Action   Action
	Label    string
	HelpText string
}

// DefaultBindings returns the standard keybindings for the TUI.
func DefaultBindings() []Binding {
	return []Binding{
		{Key: tcell.KeyCtrlC, Action: ActionQuit, Label: "Ctrl+C", HelpText: "quit"},
		{Key: tcell.KeyEscape, Action: ActionCancel, Label: "Esc", HelpText: "cancel / back"},
		{Key: tcell.KeyCtrlL, Action: ActionClear, Label: "Ctrl+L", HelpText: "clear screen"},
		{Rune: '?', Action: ActionHelp, Label: "?", HelpText: "help"},
		{Rune: '/', Action: ActionSearch, Label: "/", HelpText: "search in messages"},
		{Key: tcell.KeyCtrlP, Action: ActionCommand, Label: "Ctrl+P", HelpText: "command palette"},
		{Key: tcell.KeyCtrlR, Action: ActionToggleReasoning, Label: "Ctrl+R", HelpText: "toggle reasoning blocks"},
		{Key: tcell.KeyCtrlT, Action: ActionToggleTools, Label: "Ctrl+T", HelpText: "toggle tool blocks"},
		{Key: tcell.KeyCtrlS, Action: ActionToggleSubAgents, Label: "Ctrl+S", HelpText: "toggle sub-agent blocks"},
		{Key: tcell.KeyEnter, Action: ActionSend, Label: "Enter", HelpText: "send message"},
		{Key: tcell.KeyUp, Action: ActionScrollUp, Label: "↑", HelpText: "scroll up"},
		{Key: tcell.KeyDown, Action: ActionScrollDown, Label: "↓", HelpText: "scroll down"},
		{Rune: 'j', Action: ActionScrollDown, Label: "j", HelpText: "scroll down"},
		{Rune: 'k', Action: ActionScrollUp, Label: "k", HelpText: "scroll up"},
		{Key: tcell.KeyPgUp, Action: ActionPageUp, Label: "PgUp", HelpText: "page up"},
		{Key: tcell.KeyPgDn, Action: ActionPageDown, Label: "PgDn", HelpText: "page down"},
		{Rune: 'g', Action: ActionTop, Label: "g", HelpText: "jump to top (double)"},
		{Rune: 'G', Action: ActionBottom, Label: "G", HelpText: "jump to bottom"},
		{Key: tcell.KeyHome, Action: ActionTop, Label: "Home", HelpText: "jump to top"},
		{Key: tcell.KeyEnd, Action: ActionBottom, Label: "End", HelpText: "jump to bottom"},
		{Key: tcell.KeyTab, Action: ActionNextPanel, Label: "Tab", HelpText: "next panel"},
		{Key: tcell.KeyBacktab, Action: ActionPrevPanel, Label: "Shift+Tab", HelpText: "previous panel"},
	}
}

// Match reports whether a tcell key event matches this binding.
func (b Binding) Match(ev *tcell.EventKey) bool {
	if b.Key != 0 && ev.Key() == b.Key {
		if b.Mod != 0 {
			return ev.Modifiers() == b.Mod
		}
		// Key-based bindings (e.g., Ctrl+P, arrows) may arrive with
		// ModCtrl on Windows where tcell reports the modifier explicitly.
		return ev.Modifiers() == tcell.ModNone || ev.Modifiers() == tcell.ModCtrl
	}
	if b.Rune != 0 && ev.Rune() == b.Rune {
		if b.Mod != 0 {
			return ev.Modifiers() == b.Mod
		}
		// Printable characters like ':' or '?' require Shift on US keyboards,
		// so accept Shift (or no modifier) for rune bindings without an explicit mod.
		return ev.Modifiers() == tcell.ModNone || ev.Modifiers() == tcell.ModShift
	}
	return false
}

// Translate looks up the Action for a tcell key event.
func Translate(ev *tcell.EventKey, bindings []Binding) Action {
	for _, b := range bindings {
		if b.Match(ev) {
			return b.Action
		}
	}
	return ActionNone
}
