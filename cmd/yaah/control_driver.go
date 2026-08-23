// control_driver.go holds the control-plane helpers shared by the
// interactive surfaces (TUI, web): question-entry mapping, answer
// formatting, and the timeout policy for UI round-trips.
//
// Transport plumbing stays per-surface deliberately — the TUI pushes
// onto a channel consumed by the tview loop while web serializes to
// SSE with a request registry — so a full driver abstraction would
// hide more than it shares.
package yaah

import (
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
)

const (
	// ctrlSendTimeout bounds how long a tool goroutine waits for the
	// UI to accept a question/approval message before falling back.
	ctrlSendTimeout = 30 * time.Second

	// ctrlAnswerTimeout bounds how long a tool goroutine waits for the
	// user to answer. Prevents the question tool from blocking forever
	// if the UI stalls or the user walks away.
	ctrlAnswerTimeout = 5 * time.Minute
)

// buildCtrlQuestion maps a question-tool entry onto the control-plane
// message shape consumed by every UI.
func buildCtrlQuestion(e tools.QuestionEntry, ch chan string) *control.Question {
	opts := make([]control.Option, len(e.Options))
	for i, o := range e.Options {
		opts[i] = control.Option{Label: o.Label, Description: o.Description}
	}
	return &control.Question{
		Header:   e.Header,
		Question: e.Question,
		Options:  opts,
		Multiple: e.Multiple,
		AnswerCh: ch,
	}
}

// formatCtrlAnswer renders one answered entry for the tool result.
func formatCtrlAnswer(header, answer string) string {
	return fmt.Sprintf("%s: %s", header, answer)
}

// fallbackCtrlAnswer selects the first option when no answer arrives —
// better than blocking the agent turn indefinitely.
func fallbackCtrlAnswer(e tools.QuestionEntry) string {
	if len(e.Options) > 0 {
		return formatCtrlAnswer(e.Header, e.Options[0].Label)
	}
	return formatCtrlAnswer(e.Header, "")
}

// awaitCtrlAnswer waits for the UI's answer with the shared timeout
// policy, returning the fallback when it expires.
func awaitCtrlAnswer(e tools.QuestionEntry, ch chan string) string {
	select {
	case ans := <-ch:
		return formatCtrlAnswer(e.Header, ans)
	case <-time.After(ctrlAnswerTimeout):
		return fallbackCtrlAnswer(e)
	}
}
