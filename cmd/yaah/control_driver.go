// control_driver.go holds the cmd-side half of the control-plane bridge in
// internal/control: the transport adapters that turn a surface's channel or
// SSE stream into a control.Surface, and the installer that wires the shared
// question and approval bridges onto a session.
//
// The prompt policy itself — what is sent, how long a round-trip may take,
// what happens when nobody answers — lives in internal/control, so the TUI,
// the web UI and the ACP server cannot each grow their own copy of it.
package yaah

import (
	"time"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
)

// sendCtrlMsg delivers one control-plane prompt onto a consumer's channel,
// bounded by the shared send timeout and, when done is non-nil, by that
// consumer going away. It reports false when the prompt could not be handed
// over, which is the caller's cue to fall back instead of waiting.
func sendCtrlMsg(ch chan<- control.Msg, done <-chan struct{}, msg control.Msg) bool {
	timer := time.NewTimer(control.SendTimeout)
	defer timer.Stop()
	select {
	case ch <- msg:
		return true
	case <-done:
		return false
	case <-timer.C:
		return false
	}
}

// ctrlSurface is control.Surface over a channel that lives as long as the
// surface does — the TUI's shape. The channel is created once and drained by
// the tview loop until the process exits, so it never goes away and the
// send timeout is the only bound on a delivery.
type ctrlSurface struct{ ch chan<- control.Msg }

// Send implements control.Surface.
func (s ctrlSurface) Send(msg control.Msg) bool { return sendCtrlMsg(s.ch, nil, msg) }

// Done implements control.Surface: the TUI's channel stays answerable for the
// life of the process, so it never closes.
func (s ctrlSurface) Done() <-chan struct{} { return nil }

// installControlBridges wires the shared question and approval bridges onto
// sess for one surface. Every UI surface uses this one installer; only the
// transport differs.
//
// Both bridges are call-id aware: the approval bridge is the Session's
// SetApproveCallFn seam, so a decision travels with the provider's tool_call
// id all the way to the surface instead of being reduced to name+args.
func installControlBridges(sess *agentSession, surface control.Surface) {
	if reg := sess.ToolReg(); reg != nil {
		if qt, ok := reg.Get("question").(*tools.QuestionTool); ok {
			qt.Handler = func(entries []tools.QuestionEntry) []string {
				return control.AskQuestions(surface, entries)
			}
		}
	}
	sess.SetApproveCallFn(func(callID, name, args string) bool {
		return control.Approve(surface, callID, name, args)
	})
}
