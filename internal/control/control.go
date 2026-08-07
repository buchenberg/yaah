// Package control defines the control-plane message types sent from
// tool handlers and session infrastructure to a UI consumer (TUI, REPL,
// web, ACP). These are distinct from the provider wire-format types in
// internal/types — they carry UI-level concerns (questions, approvals,
// status updates, todo lists, context info) rather than LLM request/response
// shapes.
//
// Extracting these from internal/types breaks the types → todo dependency,
// allowing the provider wire-format package to remain dependency-free.
package control

import (
	"github.com/buchenberg/yaah/internal/todo"
)

// Msg is the sealed interface for control-plane messages sent from
// tool handlers and session infrastructure to a UI consumer (TUI, REPL, etc.).
// Each concrete type implements marker().
type Msg interface {
	marker()
}

// Status carries a notification string for display.
type Status struct{ Text string }

func (*Status) marker() {}

// Error carries an error for display.
type Error struct{ Err error }

func (*Error) marker() {}

// Option is a single choice in a question or approval prompt.
type Option struct {
	Label       string
	Description string
}

// Question carries an interactive question to display.
type Question struct {
	Header   string
	Question string
	Options  []Option
	Multiple bool
	AnswerCh chan<- string
}

func (*Question) marker() {}

// Approval carries a tool approval prompt.
type Approval struct {
	Name      string
	Args      string
	ApproveCh chan<- bool
}

func (*Approval) marker() {}

// Continue asks the host to prompt whether to continue after reaching
// the maximum number of agent iterations. Write true (continue) or false
// (stop) to AnswerCh.
type Continue struct {
	MaxIter  int
	AnswerCh chan<- bool
}

func (*Continue) marker() {}

// ModelList carries the set of available model identifiers.
type ModelList struct {
	Models        []string
	ProviderNames map[string]string
}

func (*ModelList) marker() {}

// Todos carries an updated todo list.
type Todos struct {
	Items []todo.Item
}

func (*Todos) marker() {}

// ContextInfo carries context window usage statistics.
type ContextInfo struct {
	Tokens           int
	Window           int
	LastPromptTokens int // real provider-reported prompt tokens from last turn
}

func (*ContextInfo) marker() {}

// Fallback is sent when the LLM client falls back to an alternative
// provider. The TUI should update its header to reflect the new provider.
type Fallback struct {
	Provider string
	Model    string
}

func (*Fallback) marker() {}

// Done is a sentinel sent once when the session closes.
// The goroutine forwarding from controlCh to prog.Send must detect
// this type and return to avoid a leaked goroutine after the TUI exits.
type Done struct{}

func (*Done) marker() {}
