package tui

import "charm.land/bubbles/v2/key"

// keyMap holds all declarative key bindings for the TUI.
// It powers both the input handler and the auto-generated help footer.
type keyMap struct {
	Up        key.Binding
	Down      key.Binding
	PageUp    key.Binding
	PageDown  key.Binding
	Top       key.Binding
	Bottom    key.Binding
	Quit      key.Binding
	Help      key.Binding
	Search    key.Binding
	Commands  key.Binding
	NextMatch key.Binding
	PrevMatch key.Binding
	Copy      key.Binding
	Reasoning key.Binding
	Verbose   key.Binding
	Submit    key.Binding
	Cancel    key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "scroll up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "scroll down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("PgUp", "page up"),
	),
	PageDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("PgDn", "page down"),
	),
	Top: key.NewBinding(
		key.WithKeys("home"),
		key.WithHelp("Home", "jump to top"),
	),
	Bottom: key.NewBinding(
		key.WithKeys("end"),
		key.WithHelp("End", "jump to bottom"),
	),
	Quit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Search: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "search"),
	),
	Commands: key.NewBinding(
		key.WithKeys(":"),
		key.WithHelp(":", "commands"),
	),
	NextMatch: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "next match"),
	),
	PrevMatch: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "prev match"),
	),
	Copy: key.NewBinding(
		key.WithKeys("ctrl+y"),
		key.WithHelp("ctrl+y", "copy last response"),
	),
	Reasoning: key.NewBinding(
		key.WithKeys("ctrl+t"),
		key.WithHelp("ctrl+t", "toggle reasoning"),
	),
	Verbose: key.NewBinding(
		key.WithKeys("ctrl+g"),
		key.WithHelp("ctrl+g", "toggle verbose"),
	),
	Submit: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "send message"),
	),
	Cancel: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel / back"),
	),
}
