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
	NextMatch key.Binding
	PrevMatch key.Binding
	Copy      key.Binding
	Reasoning key.Binding
	Submit    key.Binding
	Cancel    key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "scroll up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "scroll down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup", "ctrl+u"),
		key.WithHelp("PgUp", "page up"),
	),
	PageDown: key.NewBinding(
		key.WithKeys("pgdown", "ctrl+d"),
		key.WithHelp("PgDn", "page down"),
	),
	Top: key.NewBinding(
		key.WithKeys("home", "g"),
		key.WithHelp("Home/g", "jump to top"),
	),
	Bottom: key.NewBinding(
		key.WithKeys("end", "G"),
		key.WithHelp("End/G", "jump to bottom"),
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
	Submit: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "send message"),
	),
	Cancel: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel / back"),
	),
}

// footerBindings returns the 5 most important bindings for the always-visible
// footer hint bar. Ordered by priority.
func footerBindings() []key.Binding {
	return []key.Binding{
		keys.Help,
		keys.Search,
		keys.Copy,
		keys.Reasoning,
		keys.Quit,
	}
}

// footerKeyMap wraps the footer bindings to implement help.KeyMap.
type footerKeyMap struct{}

func (f footerKeyMap) ShortHelp() []key.Binding {
	return footerBindings()
}

func (f footerKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{footerBindings()}
}
