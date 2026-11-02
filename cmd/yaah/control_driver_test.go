package yaah

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
)

// TestSendCtrlMsg pins the shared delivery bound: a prompt is handed over when
// the consumer accepts it, and abandoned the moment the consumer goes away.
func TestSendCtrlMsg(t *testing.T) {
	t.Run("delivers to a live consumer", func(t *testing.T) {
		ch := make(chan control.Msg, 1)
		msg := &control.Status{Text: "hi"}
		if !sendCtrlMsg(ch, nil, msg) {
			t.Fatal("sendCtrlMsg = false, want delivery")
		}
		if got := <-ch; got != control.Msg(msg) {
			t.Errorf("delivered %#v, want %#v", got, msg)
		}
	})

	t.Run("gives up when the consumer is gone", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		begin := time.Now()
		if sendCtrlMsg(make(chan control.Msg), done, &control.Status{Text: "hi"}) {
			t.Fatal("sendCtrlMsg = true after the consumer went away")
		}
		if elapsed := time.Since(begin); elapsed > 2*time.Second {
			t.Fatalf("sendCtrlMsg waited %v on a closed consumer", elapsed)
		}
	})
}

// TestCtrlSurfaceNeverEnds pins the TUI transport: its channel is drained for
// the life of the process, so the surface reports no teardown.
func TestCtrlSurfaceNeverEnds(t *testing.T) {
	surface := ctrlSurface{ch: make(chan control.Msg, 1)}
	if surface.Done() != nil {
		t.Error("ctrlSurface.Done() must be nil — the TUI channel never closes")
	}
	if !surface.Send(&control.Status{Text: "hi"}) {
		t.Error("ctrlSurface.Send = false, want delivery")
	}
}

// TestInstallControlBridgesTUIPrompt pins the TUI approval path end to end: the
// installer's call-id-aware bridge hands the provider tool_call id to the
// surface and returns the user's decision.
func TestInstallControlBridgesTUIPrompt(t *testing.T) {
	ch := make(chan control.Msg, 4)
	sess := &agentSession{}
	installControlBridges(sess, ctrlSurface{ch: ch})
	if sess.approveCallFn == nil {
		t.Fatal("installer did not wire the call-id-aware approval bridge")
	}

	decided := make(chan bool, 1)
	go func() { decided <- sess.approveCallFn("call-42", "bash", `{"command":"ls"}`) }()

	select {
	case msg := <-ch:
		approval, ok := msg.(*control.Approval)
		if !ok {
			t.Fatalf("surface received %T, want *control.Approval", msg)
		}
		if approval.CallID != "call-42" {
			t.Errorf("CallID = %q, want call-42 — the id must reach the surface", approval.CallID)
		}
		if approval.Name != "bash" {
			t.Errorf("Name = %q, want bash", approval.Name)
		}
		approval.ApproveCh <- true
	case <-time.After(2 * time.Second):
		t.Fatal("approval prompt never reached the surface")
	}

	select {
	case approved := <-decided:
		if !approved {
			t.Error("bridge = false, want the surface's approval")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge never returned a decision")
	}
}

// TestInstallControlBridgesWebApprovalBounded pins the web approval path: the
// prompt reaches the SSE stream with the provider call id on it, and a stream
// that goes away ends the wait instead of leaving the approval pending forever
// (the old bridge waited on the stream with no bound at all).
func TestInstallControlBridgesWebApprovalBounded(t *testing.T) {
	buf := newSyncBuffer()
	view := &sseView{w: buf}
	ctrlCh := make(chan control.Msg, 4)
	done := make(chan struct{})
	ws := &webServer{
		view:    view,
		sseDone: done,
		ctrlCh:  ctrlCh,
		am:      newAnswerMap(),
	}
	var idGen atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forwardCtrl(ctx, ctrlCh, view, ws.am, &idGen)

	sess := &agentSession{}
	installControlBridges(sess, sseSurface{ws: ws})
	if sess.approveCallFn == nil {
		t.Fatal("installer did not wire the call-id-aware approval bridge")
	}

	decided := make(chan bool, 1)
	go func() { decided <- sess.approveCallFn("call-99", "write", `{"path":"x"}`) }()

	// The browser sees the dialog, with the tool_call id attached.
	waitForFrame(t, buf, `"call_id":"call-99"`)

	// The stream is torn down while the prompt is outstanding: the bridge must
	// deny promptly rather than wait out the answer timeout.
	begin := time.Now()
	close(done)
	select {
	case approved := <-decided:
		if approved {
			t.Error("bridge approved after the stream was torn down")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approval blocked on a stream that was gone")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("approval took %v after the stream was torn down", elapsed)
	}
}

// TestWebApprovalTimesOut pins the bounded decision wait on the web surface
// itself: a live stream that never answers cannot hold the approval open
// indefinitely. The timeout is supplied so the branch is reachable without
// waiting out the shared 5m bound.
func TestWebApprovalTimesOut(t *testing.T) {
	buf := newSyncBuffer()
	view := &sseView{w: buf}
	ctrlCh := make(chan control.Msg, 4)
	ws := &webServer{
		view:    view,
		sseDone: make(chan struct{}),
		ctrlCh:  ctrlCh,
		am:      newAnswerMap(),
	}
	var idGen atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forwardCtrl(ctx, ctrlCh, view, ws.am, &idGen)

	begin := time.Now()
	if control.ApproveWithin(sseSurface{ws: ws}, 40*time.Millisecond, "call-1", "bash", "{}") {
		t.Fatal("approval approved with no client answer")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("approval waited %v past its bound", elapsed)
	}
	waitForFrame(t, buf, "ctrl.approval")
}

// TestInstallControlBridgesQuestion pins the question half of the installer: the
// question tool is routed through the shared bridge and answers come back
// formatted.
func TestInstallControlBridgesQuestion(t *testing.T) {
	ch := make(chan control.Msg, 4)
	sess := &agentSession{
		toolReg: tools.NewRegistry(),
	}
	question := &tools.QuestionTool{}
	sess.toolReg.Register(question)

	installControlBridges(sess, ctrlSurface{ch: ch})

	answers := make(chan []string, 1)
	go func() {
		answers <- question.Handler([]tools.QuestionEntry{
			{Header: "Deploy", Question: "Ship it?", Options: []tools.QuestionOption{{Label: "yes"}}},
		})
	}()

	select {
	case msg := <-ch:
		question, ok := msg.(*control.Question)
		if !ok {
			t.Fatalf("surface received %T, want *control.Question", msg)
		}
		question.AnswerCh <- "yes"
	case <-time.After(2 * time.Second):
		t.Fatal("question prompt never reached the surface")
	}

	select {
	case got := <-answers:
		if len(got) != 1 || got[0] != "Deploy: yes" {
			t.Errorf("answers = %v, want [Deploy: yes]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("question handler never returned")
	}
}

// TestInstallControlBridgesWithoutQuestionTool pins the defensive path: a
// session whose registry has no question tool still gets the approval bridge
// and nothing panics.
func TestInstallControlBridgesWithoutQuestionTool(t *testing.T) {
	sess := &agentSession{}
	installControlBridges(sess, ctrlSurface{ch: make(chan control.Msg, 1)})
	if sess.approveCallFn == nil {
		t.Fatal("approval bridge missing for a session without a question tool")
	}
}
