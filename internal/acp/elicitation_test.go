package acp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
)

// questionSession wires a fake session whose prompt asks one question through
// the ACP-installed QuestionTool handler, passing askCtx() as the caller's
// context. The answer the tool produced is recorded in answer.
func questionSession(t *testing.T, askCtx func() context.Context, answer *string) *fakeSession {
	t.Helper()
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.QuestionTool{})
	sess := &fakeSession{id: "sess-ctx", reg: reg}
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		qt, ok := sess.ToolReg().Get("question").(*tools.QuestionTool)
		if !ok || qt.Handler == nil {
			return "", false, fmt.Errorf("question tool handler not installed")
		}
		answers := qt.Handler(askCtx(), []tools.QuestionEntry{{
			Question: "which way?", Header: "Direction",
			Options: []tools.QuestionOption{{Label: "left"}, {Label: "right"}},
		}})
		if len(answers) > 0 {
			*answer = answers[0]
		}
		return "done", false, nil
	}
	sess.mu.Unlock()
	return sess
}

// handshakeForm initialises the connection advertising elicitation.form and
// opens one session, returning its id.
func handshakeForm(t *testing.T, h *acpHarness) string {
	t.Helper()
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{"elicitation":{"form":true}},"clientInfo":{"name":"test-client","version":"1"}}`); err != nil {
		t.Fatal(err)
	}
	h.recv(t)
	return newSession(t, h)
}

// TestElicitAnswer_PrefersCallerContext pins the context preference in
// elicitAnswer: the wait must end when the call that asked gives up, not only
// when the session's turn context ends. A deadline-bound tool call (the shape a
// cancelled sub-agent lane produces) has to release the modal immediately —
// before the preference existed the wait used the turn context alone, which is
// still live, so an abandoned call sat out the whole PermissionTimeout while the
// user looked at a question nobody was waiting for.
func TestElicitAnswer_PrefersCallerContext(t *testing.T) {
	var answer string
	callerCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	h := newACPHarness(t, questionSession(t, func() context.Context { return callerCtx }, &answer))
	// Long enough that only the caller's context can end this wait, but under
	// the harness's 5s receive deadline so a regression fails on the elapsed
	// assertion below rather than on a read timeout.
	h.server.PermissionTimeout = 3 * time.Second
	sid := handshakeForm(t, h)

	start := time.Now()
	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"ask"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	// The modal goes out; the client never answers it.
	if msg := h.recvRequest(t, "elicitation/create"); msg.ID == nil {
		t.Fatal("elicitation/create carried no id")
	}
	var elapsed time.Duration
	for i := 0; i < 32; i++ {
		msg := h.recv(t)
		if msg.Method == "" && msg.ID != nil {
			elapsed = time.Since(start)
			break
		}
	}
	if elapsed == 0 {
		t.Fatal("prompt never completed after the caller context expired")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("turn took %v; the caller's 150ms deadline should have released the wait, not PermissionTimeout (%v)", elapsed, h.server.PermissionTimeout)
	}
	// A withheld answer must not be auto-answered over the user's head: the
	// formatted reply keeps the header but carries no chosen label.
	if strings.Contains(answer, "left") || strings.Contains(answer, "right") {
		t.Fatalf("answer = %q, want no selected option for an abandoned elicitation", answer)
	}
}

// TestElicitAnswer_NilCallerContextFallsBackToTurn checks the other half of the
// preference: a caller with no context must still wait on the session's turn
// context rather than abort instantly, so the nil guard cannot silently turn
// every elicitation into a no-op.
func TestElicitAnswer_NilCallerContextFallsBackToTurn(t *testing.T) {
	var answer string
	h := newACPHarness(t, questionSession(t, func() context.Context { return nil }, &answer))
	h.server.PermissionTimeout = 300 * time.Millisecond
	sid := handshakeForm(t, h)

	start := time.Now()
	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"ask"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	if msg := h.recvRequest(t, "elicitation/create"); msg.ID == nil {
		t.Fatal("elicitation/create carried no id")
	}
	var elapsed time.Duration
	for i := 0; i < 32; i++ {
		msg := h.recv(t)
		if msg.Method == "" && msg.ID != nil {
			elapsed = time.Since(start)
			break
		}
	}
	if elapsed == 0 {
		t.Fatal("prompt never completed")
	}
	if elapsed < h.server.PermissionTimeout/2 {
		t.Fatalf("turn ended after %v with a nil caller context; want a wait on the turn context bounded by PermissionTimeout (%v)", elapsed, h.server.PermissionTimeout)
	}
	if strings.Contains(answer, "left") || strings.Contains(answer, "right") {
		t.Fatalf("answer = %q, want no selected option after the elicitation timed out", answer)
	}
}
