package tui2

import "github.com/gdamore/tcell/v2"

// Action is a named user action dispatched by the keymap.
type Action int

const (
	ActionNone Action = iota
	ActionQuit
	ActionClear
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
)

// Binding maps a tcell key event to an Action with a display label and help
// text (used for the auto-generated help overlay).
type Binding struct {
	Key      tcell.Key
	Mod      tcell.ModMask
	Action   Action
	Label    string
	HelpText string
}

// DefaultBindings returns the standard keybindings for the TUI.
// Only Ctrl+ combinations, Esc, Enter, arrows, navigation keys, and Tab
// are bound. All other actions go through the command palette (Ctrl+P).
func DefaultBindings() []Binding {
	return []Binding{
		{Key: tcell.KeyCtrlC, Action: ActionQuit, Label: "Ctrl+C", HelpText: "quit"},
		{Key: tcell.KeyEscape, Action: ActionCancel, Label: "Esc", HelpText: "cancel / back"},
		{Key: tcell.KeyCtrlL, Action: ActionClear, Label: "Ctrl+L", HelpText: "clear screen"},
		{Key: tcell.KeyCtrlP, Action: ActionCommand, Label: "Ctrl+P", HelpText: "command palette"},
		{Key: tcell.KeyCtrlR, Action: ActionToggleReasoning, Label: "Ctrl+R", HelpText: "toggle reasoning blocks"},
		{Key: tcell.KeyCtrlT, Action: ActionToggleTools, Label: "Ctrl+T", HelpText: "toggle tool blocks"},
		{Key: tcell.KeyCtrlS, Action: ActionToggleSubAgents, Label: "Ctrl+S", HelpText: "toggle sub-agent blocks"},
		{Key: tcell.KeyEnter, Action: ActionSend, Label: "Enter", HelpText: "send message / follow-up"},
		{Key: tcell.KeyUp, Action: ActionScrollUp, Label: "↑", HelpText: "scroll up"},
		{Key: tcell.KeyDown, Action: ActionScrollDown, Label: "↓", HelpText: "scroll down"},
		{Key: tcell.KeyPgUp, Action: ActionPageUp, Label: "PgUp", HelpText: "page up"},
		{Key: tcell.KeyPgDn, Action: ActionPageDown, Label: "PgDn", HelpText: "page down"},
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
		return ev.Modifiers() == tcell.ModNone || ev.Modifiers() == tcell.ModCtrl
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
