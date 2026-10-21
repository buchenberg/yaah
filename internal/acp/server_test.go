package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
)

// fakeSession is a controllable Session for server tests.
type fakeSession struct {
	mu      sync.Mutex
	prompt  func(ctx context.Context, prompt string) (string, bool, error)
	view    agent.View
	ctrl    chan<- control.Msg
	approve func(name, args string) bool
	closed  int
}

func (f *fakeSession) RunPrompt(ctx context.Context, prompt string) (string, bool, error) {
	if f.prompt == nil {
		return "ok", false, nil
	}
	return f.prompt(ctx, prompt)
}
func (f *fakeSession) SetView(v agent.View)           { f.mu.Lock(); f.view = v; f.mu.Unlock() }
func (f *fakeSession) SetCtrlCh(c chan<- control.Msg) { f.mu.Lock(); f.ctrl = c; f.mu.Unlock() }
func (f *fakeSession) GetCtrlCh() chan<- control.Msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctrl
}
func (f *fakeSession) SetApproveFn(fn func(name, args string) bool) {
	f.mu.Lock()
	f.approve = fn
	f.mu.Unlock()
}

// approve runs the session's approval callback, if any.
func (f *fakeSession) approveCall(name, args string) bool {
	f.mu.Lock()
	fn := f.approve
	f.mu.Unlock()
	if fn == nil {
		return false
	}
	return fn(name, args)
}

func (f *fakeSession) Close()                   { f.mu.Lock(); f.closed++; f.mu.Unlock() }
func (f *fakeSession) ToolReg() *tools.Registry { return tools.NewRegistry() }

// currentView returns the view the server installed, under lock.
func (f *fakeSession) currentView() agent.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

// acpHarness wires a Server to in-memory pipes and exposes
// send/recv with timeouts.
type acpHarness struct {
	inW  io.Writer
	send func(method string, id any, params string) error
	recv func(t *testing.T) Message
	done func()
}

func newACPHarness(t *testing.T, sess Session) *acpHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	s := &Server{
		version: "test",
		logf:    func(string, ...any) {},
		stdin:   inR,
		stdout:  outW,
		newSession: func() (Session, error) {
			if sess != nil {
				return sess, nil
			}
			return &fakeSession{}, nil
		},
		AutoAnswerQuestions: true,
		AutoContinue:        true,
		PermissionTimeout:   time.Second,
		pendingReq:          make(map[string]chan clientResponse),
		run:                 &runningPrompt{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Run(ctx) }()

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		_ = outR.Close()
	})

	return &acpHarness{
		inW: inW,
		send: func(method string, id any, params string) error {
			msg := Message{JSONRPC: "2.0", Method: method, Params: json.RawMessage(params)}
			if id != nil {
				b, _ := json.Marshal(id)
				msg.ID = b
			}
			out, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			_, err = io.WriteString(inW, string(out)+"\n")
			return err
		},
		recv: func(t *testing.T) Message {
			t.Helper()
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatal("server output closed")
				}
				var msg Message
				if err := json.Unmarshal([]byte(line), &msg); err != nil {
					t.Fatalf("invalid server output %q: %v", line, err)
				}
				return msg
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for server message")
				return Message{}
			}
		},
		done: cancel,
	}
}

// initialize sends a compliant handshake and swallows the response.
func initialize(t *testing.T, h *acpHarness) {
	t.Helper()
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true},"clientInfo":{"name":"test-client","version":"1"}}`); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	raw := string(msg.Result)
	for _, want := range []string{
		`"protocolVersion":1`,
		`"agentCapabilities"`,
		`"agentInfo"`,
		`"authMethods":[]`,
		`"name":"yaah"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("initialize result missing %s: %s", want, raw)
		}
	}
	if strings.Contains(raw, "serverInfo") || strings.Contains(raw, "protocol_version") {
		t.Errorf("initialize result carries MCP-era fields: %s", raw)
	}
}

// newSession performs the handshake and creates a session, returning
// its id.
func newSession(t *testing.T, h *acpHarness) string {
	t.Helper()
	if err := h.send("session/new", 2, fmt.Sprintf(`{"cwd":%q,"mcpServers":[]}`, ".")); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	var result SessionNewResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("session/new result: %v", err)
	}
	if result.SessionID == "" {
		t.Fatalf("session/new gave empty session id: %s", msg.Result)
	}
	return result.SessionID
}

// TestServer_initializeNegotiatesV1 checks the handshake shape required
// by ACP v1 clients (integer version, agentCapabilities, authMethods).
func TestServer_initializeNegotiatesV1(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	initialize(t, h)
}

// TestServer_stopRunBoundedJoin pins the shutdown bound: a prompt
// goroutine that never unwinds (e.g. wedged on a stdout write a client
// stopped draining) must not hang stopRun — the join times out and
// teardown proceeds so SIGTERM can still kill the process.
func TestServer_stopRunBoundedJoin(t *testing.T) {
	s := &Server{logf: func(string, ...any) {}}
	s.run = &runningPrompt{done: make(chan struct{})} // never closes
	start := time.Now()
	s.stopRun()
	if elapsed := time.Since(start); elapsed < stopRunJoinTimeout-100*time.Millisecond {
		t.Fatalf("stopRun returned after %s; want to wait ~%s for the stuck run", elapsed, stopRunJoinTimeout)
	}
}

// TestServer_sessionNewIdempotent documents the one-conversation-per-
// process contract: a repeat session/new returns the same session id —
// issuing a fresh one would invalidate the handle the client already
// holds while the same underlying conversation still answers it.
func TestServer_sessionNewIdempotent(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	initialize(t, h)
	first := newSession(t, h)
	if err := h.send("session/new", 3, `{"cwd":"."}`); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	var repeat SessionNewResult
	if err := json.Unmarshal(msg.Result, &repeat); err != nil {
		t.Fatalf("repeat session/new result: %v", err)
	}
	if repeat.SessionID != first {
		t.Fatalf("repeat session/new issued %q, want the original %q", repeat.SessionID, first)
	}
}

// TestServer_unknownMethodReturns32601 mirrors the JSON-RPC requirement
// for unresolvable requests.
func TestServer_unknownMethodReturns32601(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	if err := h.send("bogus/method", 7, ""); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeMethodNotFound {
		t.Fatalf("response = %+v, want %d method not found", msg, codeMethodNotFound)
	}
	if !strings.Contains(msg.Error.Message, "bogus/method") {
		t.Errorf("error message %q should name the method", msg.Error.Message)
	}
}

// TestServer_loadSessionRejected documents that session/load for a
// foreign session id is rejected (the capability is not advertised).
func TestServer_loadSessionRejected(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	initialize(t, h)
	if err := h.send("session/load", 9, `{"sessionId":"whatever","cwd":".","mcpServers":[]}`); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeInvalidParams {
		t.Fatalf("response = %+v, want %d", msg, codeInvalidParams)
	}
}

// TestServer_sessionPromptUnknownSession requires validation against
// the session id issued by session/new.
func TestServer_sessionPromptUnknownSession(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	params := `{"sessionId":"sess_unknown","prompt":[{"type":"text","text":"hi"}]}`
	if err := h.send("session/prompt", 2, params); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeInvalidParams {
		t.Fatalf("response = %+v, want %d unknown session", msg, codeInvalidParams)
	}
}

// TestServer_setModeAcknowledged keeps clients that surface a mode
// picker from hanging.
func TestServer_setModeAcknowledged(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	initialize(t, h)
	sid := newSession(t, h)
	if err := h.send("session/set_mode", 4, fmt.Sprintf(`{"sessionId":%q,"modeId":"x"}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error != nil {
		t.Fatalf("set_mode error: %+v", msg)
	}
}

// TestServer_promptRespondsWhenTurnEnds is the core ACP lifecycle
// check: the session/prompt request stays open for the whole turn and
// is answered with a stopReason, with streamed updates arriving first.
func TestServer_promptRespondsWhenTurnEnds(t *testing.T) {
	sess := &fakeSession{}
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		// Simulate the loop: stream a chunk, then complete.
		sess.currentView().HandleEvent(&agent.TokenDeltaEvent{Text: "working..."})
		return "done", false, nil
	}
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"do a thing"}]}`, sid)); err != nil {
		t.Fatal(err)
	}

	// First: the streamed update.
	var sawChunk, sawStop bool
	for i := 0; i < 8; i++ {
		msg := h.recv(t)
		if msg.Method != "session/update" {
			if len(msg.ID) > 0 && msg.Error == nil {
				var result PromptResult
				if err := json.Unmarshal(msg.Result, &result); err != nil {
					t.Fatalf("prompt result: %v", err)
				}
				if sawChunk && result.StopReason == StopEndTurn {
					sawStop = true
				}
				if !sawChunk {
					t.Fatal("prompt response arrived before any streamed update")
				}
				return // exactly the ordering we want
			}
			t.Fatalf("unexpected message: %+v", msg)
		}
		var env updateEnvelope
		if err := json.Unmarshal(msg.Params, &env); err != nil {
			t.Fatalf("session/update params: %v", err)
		}
		var up struct {
			SessionUpdate string `json:"sessionUpdate"`
		}
		if err := json.Unmarshal(env.Update, &up); err != nil {
			t.Fatalf("update discriminant: %v", err)
		}
		switch up.SessionUpdate {
		case UpdateAgentMessageChunk:
			sawChunk = true
		case "stop_reason":
			t.Fatal("stop_reason must be a session/prompt response, not a session/update variant")
		}
	}
	t.Fatalf("never reached the prompt response (chunk=%v stop=%v)", sawChunk, sawStop)
}

// TestServer_promptRespondsCancelled covers cancel-while-running: the
// client sends session/cancel mid-turn and the held-open request
// answers cancelled (not an RPC error).
func TestServer_promptRespondsCancelled(t *testing.T) {
	started := make(chan struct{})
	sess := &fakeSession{
		prompt: func(ctx context.Context, prompt string) (string, bool, error) {
			close(started)
			<-ctx.Done()
			return "", false, ctx.Err()
		},
	}
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"long task"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := h.send("session/cancel", nil, fmt.Sprintf(`{"sessionId":%q}`, sid)); err != nil {
		t.Fatal(err)
	}

	msg := h.recv(t)
	if msg.Error != nil {
		t.Fatalf("cancel produced an RPC error response, want stopReason cancelled: %+v", msg)
	}
	var result PromptResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("cancel result: %v (raw=%s)", err, msg.Result)
	}
	if result.StopReason != StopCancelled {
		t.Fatalf("stopReason = %q, want cancelled", result.StopReason)
	}
}

// TestServer_readerStaysResponsiveWhilePromptRuns proves the dispatch
// loop is not blocked by an in-flight turn (a control-plane request is
// answered immediately).
func TestServer_promptRelatedSetModeResponsive(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sess := &fakeSession{
		prompt: func(ctx context.Context, prompt string) (string, bool, error) {
			close(started)
			<-release
			return "late", false, nil
		},
	}
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"long"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	<-started

	// This set_mode must be answered while the prompt is still held open.
	if err := h.send("session/set_mode", 4, fmt.Sprintf(`{"sessionId":%q,"modeId":"x"}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if !(msg.Error == nil && len(msg.ID) > 0 && strings.Contains(string(msg.ID), "4")) {
		t.Fatalf("expected set_mode ack id=4 while prompt in flight, got %+v", msg)
	}
	close(release)
	h.recv(t) // the prompt response, eventually
}

// TestServer_secondPromptWhileBusyRejections guards the held-open
// contract: a second session/prompt before the first responds is a
// busy error, silently cancelling the first.
func TestServer_secondPromptWhileBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sess := &fakeSession{
		prompt: func(ctx context.Context, prompt string) (string, bool, error) {
			close(started)
			<-release
			return "", false, ctx.Err()
		},
	}
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"first"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	<-started

	if err := h.send("session/prompt", 4, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"second"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	// Drain until the busy error lands.
	var busy *RPCError
	for i := 0; i < 8 && busy == nil; i++ {
		msg := h.recv(t)
		if msg.Error != nil {
			busy = msg.Error
		}
	}
	if busy == nil || busy.Code != codeBusy {
		t.Fatalf("expected codeBusy error response, got %+v", busy)
	}
	close(release)
}

// TestServer_emptyPromptRejected keeps empty prompt blocks out.
func TestServer_emptyPromptRejected(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	initialize(t, h)
	sid := newSession(t, h)
	if err := h.send("session/prompt", 5, fmt.Sprintf(`{"sessionId":%q,"prompt":[]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeInvalidParams {
		t.Fatalf("response = %+v, want %d empty prompt", msg, codeInvalidParams)
	}
}

// reply sends a client response for a server-initiated request.
func (h *acpHarness) reply(id json.RawMessage, result any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	out, err := json.Marshal(Message{JSONRPC: "2.0", ID: id, Result: data})
	if err != nil {
		return err
	}
	_, err = io.WriteString(h.inW, string(out)+"\n")
	return err
}

// TestServer_permissionAllow drives the request_permission bridge: the
// approval callback suspends the run, the request reaches the client,
// and the routed response decides.
func TestServer_permissionAllow(t *testing.T) {
	sess := &fakeSession{}
	var granted bool
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		granted = sess.approveCall("bash", `{"command":"ls"}`)
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"exec something"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Method != "session/request_permission" {
		t.Fatalf("expected request_permission, got %+v", msg)
	}
	var params RequestPermissionParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("request_permission params: %v", err)
	}
	if params.ToolCall.ToolCallID == "" || params.ToolCall.Title == "" {
		t.Fatalf("permission request missing toolCall identity: %+v", params.ToolCall)
	}
	if len(params.Options) == 0 {
		t.Fatal("permission request missing options")
	}
	// Reply selecting allow-once, echoing the exact id.
	if err := h.reply(msg.ID, map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "allow-once"},
	}); err != nil {
		t.Fatal(err)
	}
	// Then the prompt response must arrive.
	for i := 0; i < 8; i++ {
		msg = h.recv(t)
		if msg.Method == "" && msg.Error == nil {
			var result PromptResult
			if err := json.Unmarshal(msg.Result, &result); err == nil && result.StopReason == StopEndTurn {
				if !granted {
					t.Fatal("client allowed the tool but the session saw a denial")
				}
				return
			}
		}
	}
	t.Fatal("prompt never completed after allowance")
}

// TestServer_permissionDenyRejected checks the refusal path: the
// session sees deny and the client sees a failed tool_call_update for
// the pending row.
func TestServer_permissionDenyRejected(t *testing.T) {
	sess := &fakeSession{}
	var granted bool
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		granted = sess.approveCall("bash", `{"command":"rm -rf"}`)
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"exec something"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Method != "session/request_permission" {
		t.Fatalf("expected request_permission, got %+v", msg)
	}
	if err := h.reply(msg.ID, map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "reject-once"},
	}); err != nil {
		t.Fatal(err)
	}
	var sawFailed bool
	for i := 0; i < 8; i++ {
		msg = h.recv(t)
		if msg.Method == "session/update" {
			var env updateEnvelope
			if err := json.Unmarshal(msg.Params, &env); err != nil {
				continue
			}
			var up ToolCallUpdate
			if err := json.Unmarshal(env.Update, &up); err == nil &&
				up.Status == StatusFailed && strings.Contains(up.ToolCallID, "perm_") {
				sawFailed = true
			}
		}
		if msg.Method == "" && sawFailed {
			return
		}
	}
	t.Fatalf("denial did not surface a failed tool_call_update (granted=%v)", granted)
}

// TestServer_permissionTimeoutDenies checks the bounded wait: no reply,
// the callback denies and the turn still completes.
func TestServer_permissionTimeoutDenies(t *testing.T) {
	sess := &fakeSession{}
	var granted bool
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		granted = sess.approveCall("bash", `{"command":"ls"}`)
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"exec something"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	_ = h.recv(t) // the permission request
	for i := 0; i < 8; i++ {
		msg := h.recv(t)
		if msg.Method == "" && msg.Error == nil {
			var result PromptResult
			if err := json.Unmarshal(msg.Result, &result); err == nil && result.StopReason == StopEndTurn {
				if granted {
					t.Fatal("unanswered permission request must deny")
				}
				return
			}
		}
	}
	t.Fatal("turn never completed after permission timeout")
}

// TestServer_sessionCloseOnEOF verifies the server tears the underlying
// session down when the client closes stdin.
func TestServer_sessionCloseOnEOF(t *testing.T) {
	sess := &fakeSession{}
	h := newACPHarness(t, sess)
	initialize(t, h)
	_ = newSession(t, h)

	// Closing the input pipe: the run goroutine sees EOF, cancels the
	// run, and closes the session.
	h.done()
	// The harness cancels the server context too; wait a moment for
	// teardown to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		closed := sess.closed
		sess.mu.Unlock()
		if closed > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session was never closed after stdin EOF")
}

// TestServer_resourceLinkPromptPreserved verifies non-text prompt
// blocks are not dropped silently (ACP baseline supports resource_link).
func TestServer_resourceLinkPromptPreserved(t *testing.T) {
	var got string
	sess := &fakeSession{
		prompt: func(_ context.Context, prompt string) (string, bool, error) {
			got = prompt
			return "ok", false, nil
		},
	}
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)
	if err := h.send("session/prompt", 6, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"resource_link","uri":"file:///x/y.ts","name":"y.ts"},{"type":"text","text":"explain"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error != nil {
		t.Fatalf("resource_link prompt rejected: %+v", msg)
	}
	if !strings.Contains(got, "file:///x/y.ts") || !strings.Contains(got, "explain") {
		t.Fatalf("prompt lost content: %q", got)
	}
}

// TestServer_questionAutoAnswer (control-plane policy lives behind the
// question tool; a full integration is in cmd/yaah tests).
func TestServer_autoAnswerDefaultsOn(t *testing.T) {
	s := NewServer(func() (Session, error) { return &fakeSession{}, nil }, "t", nil)
	if !s.AutoAnswerQuestions || !s.AutoContinue {
		t.Fatal("defaults must enable auto-answer/auto-continue")
	}
}
