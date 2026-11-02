// bridge.go carries the control-plane delivery policy every UI surface and
// the ACP server share: how long a prompt may take to be accepted, how long
// the user may take to answer it, and what happens when either bound is hit.
//
// The transport is the one part that genuinely differs per surface — the TUI
// pushes onto a channel drained by the tview loop, the web UI serializes to
// SSE, ACP speaks JSON-RPC — so it is the one part a surface implements
// (Surface). Question and approval handling then live here once instead of
// being re-derived, with divergent timeouts, on each surface.
package control

import "time"

const (
	// SendTimeout bounds how long a tool goroutine waits for a surface to
	// accept one control-plane prompt. It keeps a stalled or detached
	// consumer from parking the tool goroutine that is running the turn.
	SendTimeout = 30 * time.Second

	// AnswerTimeout bounds how long a tool goroutine waits for the user to
	// answer a question or decide a tool approval. It is the single decision
	// timeout in the program: questions and approvals on the TUI and web
	// surfaces, and the default of ACP's session/request_permission and
	// elicitation/create waits.
	AnswerTimeout = 5 * time.Minute
)

// Surface is the transport half of the bridge: the part each UI supplies.
//
// Implementations are called from tool goroutines (the agent runs tools
// concurrently) and must not block for longer than SendTimeout.
type Surface interface {
	// Send hands one prompt to the surface, waiting at most SendTimeout for
	// it to be accepted, and reports whether the user will ever see it.
	// False means the prompt could not be delivered — no consumer attached,
	// the surface stalled, or it went away — and the caller supplies its
	// fallback rather than waiting for an answer that cannot arrive.
	//
	// The returned channel is closed when an answer to *this* prompt can no
	// longer arrive: the lane that accepted it was torn down, the run behind
	// it was abandoned, the UI exited. A nil channel means the prompt stays
	// answerable for the surface's lifetime, which is the TUI's case — its
	// channel is consumed until the process ends.
	//
	// The channel is bound at delivery time on purpose. A surface whose lane
	// can be replaced (the web's SSE stream) must report the lane it actually
	// used, not re-read current state when the caller starts waiting: by then
	// a successor lane may own the surface, and waiting on the successor's
	// channel parks the caller for the full answer timeout on a prompt that
	// was already orphaned.
	Send(msg Msg) (gone <-chan struct{}, ok bool)
}

// awaitAnswer waits for one answer on ch, bounded by timeout and by done (the
// surface going away). ok reports whether an answer arrived; the zero value is
// returned when it did not, so a caller can never mistake a torn-down surface
// for a decision.
func awaitAnswer[T any](ch <-chan T, gone <-chan struct{}, timeout time.Duration) (T, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-gone:
	case <-timer.C:
	}
	// select chooses uniformly among ready cases, so a decision that landed
	// at the same instant the surface went away — or the bound expired — can
	// still be sitting in ch. Honor it: the user answered, and discarding
	// that answer would deny a call nobody declined.
	select {
	case v := <-ch:
		return v, true
	default:
	}
	var zero T
	return zero, false
}
