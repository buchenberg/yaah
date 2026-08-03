package types

import (
	"github.com/buchenberg/yaah/internal/todo"
)

// CtrlMsg is the sealed interface for control-plane messages sent from
// tool handlers and session infrastructure to a UI consumer (TUI, REPL, etc.).
// Each concrete type implements ctrlMarker().
type CtrlMsg interface {
	ctrlMarker()
}

// CtrlStatus carries a notification string for display.
type CtrlStatus struct{ Text string }

func (*CtrlStatus) ctrlMarker() {}

// CtrlError carries an error for display.
type CtrlError struct{ Err error }

func (*CtrlError) ctrlMarker() {}

// CtrlOption is a single choice in a question or approval prompt.
type CtrlOption struct {
	Label       string
	Description string
}

// CtrlQuestion carries an interactive question to display.
type CtrlQuestion struct {
	Header   string
	Question string
	Options  []CtrlOption
	Multiple bool
	AnswerCh chan<- string
}

func (*CtrlQuestion) ctrlMarker() {}

// CtrlApproval carries a tool approval prompt.
type CtrlApproval struct {
	Name      string
	Args      string
	ApproveCh chan<- bool
}

func (*CtrlApproval) ctrlMarker() {}

// CtrlModelList carries the set of available model identifiers.
type CtrlModelList struct {
	Models        []string
	ProviderNames map[string]string
}

func (*CtrlModelList) ctrlMarker() {}

// CtrlTodos carries an updated todo list.
type CtrlTodos struct {
	Items []todo.Item
}

func (*CtrlTodos) ctrlMarker() {}

// CtrlContextInfo carries context window usage statistics.
type CtrlContextInfo struct {
	Tokens int
	Window int
}

func (*CtrlContextInfo) ctrlMarker() {}

// CtrlFallback is sent when the LLM client falls back to an alternative
// provider. The TUI should update its header to reflect the new provider.
type CtrlFallback struct {
	Provider string
	Model    string
}

func (*CtrlFallback) ctrlMarker() {}

// CtrlDone is a sentinel sent once when the session closes.
// The goroutine forwarding from controlCh to prog.Send must detect
// this type and return to avoid a leaked goroutine after the TUI exits.
type CtrlDone struct{}

func (*CtrlDone) ctrlMarker() {}
