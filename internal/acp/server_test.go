package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// fakeSession is a controllable Session for server tests.
type fakeSession struct {
	mu      sync.Mutex
	prompt  func(ctx context.Context, prompt string) (string, bool, error)
	view    agent.View
	ctrl    chan<- control.Msg
	approve func(callID, name, args string) bool
	closed  int
	// id is the persisted conversation id AgentSessionID reports; set
	// at construction and never mutated.
	id string
	// history is what History returns for session/load replay tests.
	history []types.Message
}

func (f *fakeSession) RunPrompt(ctx context.Context, prompt string) (string, bool, error) {
	if f.prompt == nil {
		return "ok", false, nil
	}
	return f.prompt(ctx, prompt)
}
func (f *fakeSession) SetView(v agent.View)           { f.mu.Lock(); f.view = v; f.mu.Unlock() }
func (f *fakeSession) SetCtrlCh(c chan<- control.Msg) { f.mu.Lock(); f.ctrl = c; f.mu.Unlock() }
func (f *fakeSession) SetApproveCallFn(fn func(callID, name, args string) bool) {
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
	return fn("call-1", name, args)
}

func (f *fakeSession) Close() { f.mu.Lock(); f.closed++; f.mu.Unlock() }

// closedCount reports how many times Close was called, under lock.
func (f *fakeSession) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeSession) ToolReg() *tools.Registry { return tools.NewRegistry() }
func (f *fakeSession) AgentSessionID() string   { return f.id }

// History returns a copy of the fake's history, mirroring
// agentSession.History.
func (f *fakeSession) History() []types.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.Message, len(f.history))
	copy(out, f.history)
	return out
}

// currentView returns the view the server installed, under lock.
func (f *fakeSession) currentView() agent.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

// acpHarness wires a Server to in-memory pipes and exposes
// send/recv with timeouts.
type acpHarness struct {
	inW     io.Writer
	send    func(method string, id any, params string) error
	recv    func(t *testing.T) Message
	done    func()
	resumes func() []string
	cwd     string
	// server is the wired Server instance, for tests that flip optional
	// plumbing (ListSessions, DeleteSession) before traffic starts.
	server *Server
}

// newACPHarness wires a Server whose factory always yields sess (or a
// default fake used by session/new-first flows). Use
// newACPHarnessFactory for tests that need to model failed builds or
// per-resume-id sessions.
func newACPHarness(t *testing.T, sess Session) *acpHarness {
	t.Helper()
	return newACPHarnessFactory(t, func(string) (Session, error) {
		if sess != nil {
			return sess, nil
		}
		return &fakeSession{id: "sess-test"}, nil
	})
}

// newACPHarnessFactory wires a Server with an explicit session
// factory. The factory receives the resume id a session/new or
// session/load requested ("" for fresh); the harness's resumes method
// reports the ids it was called with, in order.
func newACPHarnessFactory(t *testing.T, factory func(resumeID string) (Session, error)) *acpHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	var (
		resumeMu  sync.Mutex
		resumeIDs []string
	)

	s := &Server{
		version: "test",
		logf:    func(string, ...any) {},
		stdin:   inR,
		stdout:  outW,
		newSession: func(build BuildOptions) (Session, error) {
			resumeMu.Lock()
			resumeIDs = append(resumeIDs, build.ResumeID)
			resumeMu.Unlock()
			return factory(build.ResumeID)
		},
		AutoAnswerQuestions: true,
		AutoContinue:        true,
		PermissionTimeout:   time.Second,
		pendingReq:          make(map[string]chan clientResponse),
		sessions:            make(map[string]*sessionState),
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
		server: s,
		inW:    inW,
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
		resumes: func() []string {
			resumeMu.Lock()
			defer resumeMu.Unlock()
			out := make([]string, len(resumeIDs))
			copy(out, resumeIDs)
			return out
		},
		cwd: t.TempDir(),
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
		`"loadSession":true`,
		`"agentInfo"`,
		`"authMethods":[{"id":`,
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

// TestServer_initializeAdvertisesAuthMethods pins the authMethods wire
// shape: a non-empty catalog of agent-typed methods, each with an id and
// a name and none carrying a `type` — a "terminal" method would be
// illegal here because the client did not enable the auth terminal
// capability.
func TestServer_initializeAdvertisesAuthMethods(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{}}`); err != nil {
		t.Fatal(err)
	}
	var result struct {
		AuthMethods []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Type        string `json:"type"`
		} `json:"authMethods"`
	}
	if err := json.Unmarshal(h.recv(t).Result, &result); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if len(result.AuthMethods) == 0 {
		t.Fatal("initialize advertised no auth methods")
	}
	for i, m := range result.AuthMethods {
		if m.ID == "" || m.Name == "" {
			t.Errorf("authMethods[%d] = %+v, want id and name", i, m)
		}
		if m.Type != "" {
			t.Errorf("authMethods[%d] (%s) sets type %q; agent methods omit type", i, m.ID, m.Type)
		}
	}
}

// TestServer_authenticate pins the authenticate contract: a declared
// method id succeeds with an empty result object, an undeclared one is
// invalid params, and neither path requires a credential to be present.
func TestServer_authenticate(t *testing.T) {
	t.Run("declared method succeeds with an empty result", func(t *testing.T) {
		h := newACPHarness(t, &fakeSession{id: "sess-test"})
		initialize(t, h)
		if err := h.send("authenticate", 2, `{"methodId":"openai-api-key"}`); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error != nil {
			t.Fatalf("authenticate error: %+v", msg.Error)
		}
		if got := strings.TrimSpace(string(msg.Result)); got != "{}" {
			t.Fatalf("authenticate result = %s, want empty object", got)
		}
	})

	t.Run("unknown method is invalid params", func(t *testing.T) {
		h := newACPHarness(t, &fakeSession{id: "sess-test"})
		initialize(t, h)
		if err := h.send("authenticate", 2, `{"methodId":"not-a-method"}`); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error == nil || msg.Error.Code != codeInvalidParams {
			t.Fatalf("response = %+v, want %d invalid params", msg, codeInvalidParams)
		}
		if !strings.Contains(msg.Error.Message, "not-a-method") {
			t.Errorf("error message %q does not name the rejected method", msg.Error.Message)
		}
	})
}

// newSession performs the handshake and creates a session, returning
// its id.
func newSession(t *testing.T, h *acpHarness) string {
	t.Helper()
	cwd, _ := json.Marshal(h.cwd)
	if err := h.send("session/new", 2, fmt.Sprintf(`{"cwd":%s,"mcpServers":[]}`, cwd)); err != nil {
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

// loadParams builds session/load params for the harness's cwd.
func loadParams(t *testing.T, h *acpHarness, id string) string {
	t.Helper()
	cwd, _ := json.Marshal(h.cwd)
	return fmt.Sprintf(`{"sessionId":%q,"cwd":%s,"mcpServers":[]}`, id, cwd)
}

// TestServer_initializeNegotiatesV1 checks the handshake shape required
// by ACP v1 clients (integer version, agentCapabilities, authMethods).
func TestServer_initializeNegotiatesV1(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
	initialize(t, h)
}

// TestServer_initializeWithoutClientInfo covers the minimal handshake a
// strict ACP client may send: clientInfo and clientCapabilities are
// both optional in v1, so initialize must answer instead of
// dereferencing the nil clientInfo (which used to panic the process).
func TestServer_initializeWithoutClientInfo(t *testing.T) {
	tests := []struct {
		name   string
		params string
	}{
		{name: "version only", params: `{"protocolVersion":1}`},
		{name: "empty params", params: `{}`},
		{name: "caps but no info", params: `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true},"terminal":true}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newACPHarness(t, &fakeSession{id: "sess-test"})
			if err := h.send("initialize", 1, tc.params); err != nil {
				t.Fatal(err)
			}
			msg := h.recv(t)
			if msg.Error != nil {
				t.Fatalf("initialize returned error: %+v", msg.Error)
			}
			if !strings.Contains(string(msg.Result), `"name":"yaah"`) {
				t.Errorf("initialize result missing agent info: %s", msg.Result)
			}
		})
	}
}

// TestServer_stopRunBoundedJoin pins the shutdown bound: a prompt
// goroutine that never unwinds (e.g. wedged on a stdout write a client
// stopped draining) must not hang stopRun — the join times out and
// teardown proceeds so SIGTERM can still kill the process.
func TestServer_stopRunBoundedJoin(t *testing.T) {
	s := &Server{
		logf:     func(string, ...any) {},
		sessions: map[string]*sessionState{"sess-test": {run: &runningPrompt{done: make(chan struct{})}}},
	}
	start := time.Now()
	s.stopRun()
	if elapsed := time.Since(start); elapsed < stopRunJoinTimeout-100*time.Millisecond {
		t.Fatalf("stopRun returned after %s; want to wait ~%s for the stuck run", elapsed, stopRunJoinTimeout)
	}
}

// TestServer_sessionNewDistinctIDs pins ACP-SESSION-002: every
// session/new creates a new conversation and returns a distinct id.
func TestServer_sessionNewDistinctIDs(t *testing.T) {
	next := 0
	h := newACPHarnessFactory(t, func(string) (Session, error) {
		next++
		return &fakeSession{id: fmt.Sprintf("sess-%d", next)}, nil
	})
	initialize(t, h)
	first := newSession(t, h)
	second := newSession(t, h)
	if first == second {
		t.Fatalf("two session/new calls returned the same id %q", first)
	}
}

// TestServer_sessionNewExposesAgentSessionID pins the session-id
// contract session/load depends on: the ACP id handed to the client is
// the underlying persisted conversation id, so a fresh process can
// re-attach to the same conversation by that name.
func TestServer_sessionNewExposesAgentSessionID(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-abc123"})
	initialize(t, h)
	if got := newSession(t, h); got != "sess-abc123" {
		t.Fatalf("session/new returned %q, want the underlying agent session id %q", got, "sess-abc123")
	}
}

// loadProbe decodes a session/update notification's replay chunk into
// "variant:text" for ordered assertions.
func loadProbe(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var env updateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("update envelope: %v", err)
	}
	var chunk ChunkUpdate
	if err := json.Unmarshal(env.Update, &chunk); err != nil {
		t.Fatalf("update payload: %v", err)
	}
	return chunk.SessionUpdate + ":" + chunk.Content.Text
}

// TestServer_sessionLoadReplaysHistory covers the happy path: a fresh
// process loads a persisted conversation, streams user/assistant text
// as session/update notifications before responding, skips
// conversation internals (synthetic tool-call text, tool results,
// system rows, and the persisted user-role guidance rows that carry
// agent-internal steering/checkpoint/conflict/background-delivery
// text rather than user input), and stays promptable afterwards.
func TestServer_sessionLoadReplaysHistory(t *testing.T) {
	fac := &fakeSession{
		id: "sess-load-1",
		history: []types.Message{
			{Role: "user", Content: "fix the flake"},
			{Role: "user", Content: "[STEER] stop doing that, use the other approach"},
			{Role: "user", Content: "[STEER] No new evidence in the last 3 tool calls."},
			{Role: "assistant", Content: "on it"},
			{Role: "assistant", Content: "[tool:question] asked",
				ToolCalls: []types.ToolCall{{Function: types.ToolCallFn{Name: "question"}}}},
			{Role: "tool", Content: "answer", ToolCallID: "call-1"},
			{Role: "user", Content: "SUPERVISOR: the previous turn failed while executing tools: boom. Retry or report."},
			{Role: "assistant", Content: "done"},
			{Role: "user", Content: "CONFLICT: Parallel workers modified the same file(s)."},
			{Role: "user", Content: "[BACKGROUND worker] reviewed the logs\njob finished cleanly."},
			{Role: "system", Content: "compaction summary"},
		},
	}
	h := newACPHarness(t, fac)
	initialize(t, h)
	if err := h.send("session/load", 3, loadParams(t, h, "sess-load-1")); err != nil {
		t.Fatal(err)
	}

	var replayed []string
	var resp Message
	for {
		msg := h.recv(t)
		if msg.Method == "session/update" {
			replayed = append(replayed, loadProbe(t, msg.Params))
			continue
		}
		resp = msg
		break
	}

	if resp.Error != nil {
		t.Fatalf("session/load returned error: %+v", resp.Error)
	}
	if raw := string(resp.Result); raw != "{}" {
		t.Errorf("session/load result = %s, want {}", raw)
	}
	want := []string{
		UpdateUserMessageChunk + ":fix the flake",
		UpdateAgentMessageChunk + ":on it",
		UpdateAgentMessageChunk + ":done",
	}
	if len(replayed) != len(want) {
		t.Fatalf("replayed %v, want %v", replayed, want)
	}
	for i := range want {
		if replayed[i] != want[i] {
			t.Errorf("replay[%d] = %q, want %q", i, replayed[i], want[i])
		}
	}
	if res := h.resumes(); len(res) != 1 || res[0] != "sess-load-1" {
		t.Errorf("factory called with %v, want [sess-load-1]", res)
	}

	// A loaded conversation is promptable: the run-state reset left by
	// the load must not wedge the next turn.
	if err := h.send("session/prompt", 4, `{"sessionId":"sess-load-1","prompt":[{"type":"text","text":"next"}]}`); err != nil {
		t.Fatal(err)
	}
	for {
		msg := h.recv(t)
		if msg.Method == "session/update" {
			continue
		}
		if msg.Error != nil {
			t.Fatalf("prompt after load returned error: %+v", msg.Error)
		}
		if !strings.Contains(string(msg.Result), `"stopReason":"end_turn"`) {
			t.Errorf("prompt after load: %s", msg.Result)
		}
		break
	}
}

// TestServer_sessionLoadUnknownSessionRejected covers the
// foreign-session-id rejection for session/load: an id the session
// store never issued fails the underlying build, and the server
// surfaces that as invalid params.
func TestServer_sessionLoadUnknownSessionRejected(t *testing.T) {
	h := newACPHarnessFactory(t, func(resumeID string) (Session, error) {
		return nil, fmt.Errorf("cannot resume session %s: session not found or has no messages", resumeID)
	})
	initialize(t, h)
	if err := h.send("session/load", 5, loadParams(t, h, "sess-ghost")); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeInvalidParams {
		t.Fatalf("response = %+v, want invalid params", msg)
	}
	if !strings.Contains(msg.Error.Message, "sess-ghost") {
		t.Errorf("error should name the rejected id: %s", msg.Error.Message)
	}
	if res := h.resumes(); len(res) != 1 || res[0] != "sess-ghost" {
		t.Errorf("factory called with %v, want [sess-ghost]", res)
	}
}

// TestServer_sessionLoadMismatchedResumedSession pins the id invariant
// behind the load: the build must re-attach to the requested
// conversation. A factory that resumes a different id is rejected
// before anything replays, the half-built session is dropped (not
// left serving a conversation the client never negotiated), and the
// server stays usable for a session/new afterwards.
func TestServer_sessionLoadMismatchedResumedSession(t *testing.T) {
	built := &fakeSession{id: "sess-someone-else"}
	h := newACPHarnessFactory(t, func(resumeID string) (Session, error) {
		return built, nil
	})
	initialize(t, h)
	if err := h.send("session/load", 8, loadParams(t, h, "sess-requested")); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Method == "session/update" {
		t.Fatalf("replay update streamed before the rejection: %v", msg)
	}
	if msg.Error == nil || msg.Error.Code != codeInvalidParams {
		t.Fatalf("response = %+v, want invalid params", msg)
	}
	for _, id := range []string{"sess-someone-else", "sess-requested"} {
		if !strings.Contains(msg.Error.Message, id) {
			t.Errorf("error should name %q: %s", id, msg.Error.Message)
		}
	}
	if built.closedCount() == 0 {
		t.Error("mismatched resumed session must be dropped after the rejection")
	}

	// The dropped build must not wedge the server: a session/new
	// proceeds normally.
	if got := newSession(t, h); got != "sess-someone-else" {
		t.Errorf("session/new after rejection returned %q, want sess-someone-else", got)
	}
}

// TestServer_sessionLoadSecondSessionAllowed covers the multi-session
// model: a second, independent conversation can be loaded while the
// first stays live.
func TestServer_sessionLoadSecondSessionAllowed(t *testing.T) {
	h := newACPHarnessFactory(t, func(resumeID string) (Session, error) {
		if resumeID == "" {
			return &fakeSession{id: "sess-new"}, nil
		}
		return &fakeSession{id: resumeID}, nil
	})
	initialize(t, h)
	if id := newSession(t, h); id != "sess-new" {
		t.Fatalf("session/new returned %q, want sess-new", id)
	}
	if err := h.send("session/load", 6, loadParams(t, h, "sess-other")); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error != nil {
		t.Fatalf("second load returned error: %+v", msg.Error)
	}
	if raw := string(msg.Result); raw != "{}" {
		t.Errorf("second load result = %s, want {}", raw)
	}
	if res := h.resumes(); len(res) != 2 || res[0] != "" || res[1] != "sess-other" {
		t.Errorf("factory calls = %v, want [\"\", \"sess-other\"]", res)
	}
}

// TestServer_sessionLoadSameIdReplaysAgain covers a client reloading
// the id it already established: the live conversation is replayed
// again (a load request must stream the full conversation before its
// response), answered with {}, and no second factory build happens.
func TestServer_sessionLoadSameIdReplaysAgain(t *testing.T) {
	fac := &fakeSession{
		id: "sess-live",
		history: []types.Message{
			{Role: "user", Content: "earlier question"},
			{Role: "assistant", Content: "earlier answer"},
		},
	}
	h := newACPHarness(t, fac)
	initialize(t, h)
	id := newSession(t, h)
	if id != "sess-live" {
		t.Fatalf("session/new returned %q, want sess-live", id)
	}
	if err := h.send("session/load", 7, loadParams(t, h, id)); err != nil {
		t.Fatal(err)
	}
	var replayed []string
	var resp Message
	for {
		msg := h.recv(t)
		if msg.Method == "session/update" {
			replayed = append(replayed, loadProbe(t, msg.Params))
			continue
		}
		resp = msg
		break
	}
	if resp.Error != nil {
		t.Fatalf("reload returned error: %+v", resp.Error)
	}
	if raw := string(resp.Result); raw != "{}" {
		t.Errorf("reload result = %s, want {}", raw)
	}
	want := []string{
		UpdateUserMessageChunk + ":earlier question",
		UpdateAgentMessageChunk + ":earlier answer",
	}
	if len(replayed) != len(want) {
		t.Fatalf("replayed %v, want %v", replayed, want)
	}
	for i := range want {
		if replayed[i] != want[i] {
			t.Errorf("replay[%d] = %q, want %q", i, replayed[i], want[i])
		}
	}
	if res := h.resumes(); len(res) != 1 || res[0] != "" {
		t.Errorf("factory calls = %v, want only the fresh build [\"\"]", res)
	}
}

// TestServer_unknownMethodReturns32601 mirrors the JSON-RPC requirement
// for unresolvable requests.
func TestServer_unknownMethodReturns32601(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
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

// TestServer_sessionPromptUnknownSession requires validation against
// the session id issued by session/new.
func TestServer_sessionPromptUnknownSession(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
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
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
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
	sess := &fakeSession{id: "sess-test"}
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
		id: "sess-test",
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
		id: "sess-test",
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
		id: "sess-test",
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

// TestResolveMCPRefsResolvesCommands covers clients that send a bare
// executable name or a cwd-relative path instead of the absolute path
// the spec asks for.
func TestResolveMCPRefsResolvesCommands(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	refs, err := resolveMCPRefs([]McpServerRef{{Name: "bare", Command: "go"}}, t.TempDir())
	if err != nil {
		t.Fatalf("bare command: %v", err)
	}
	if !filepath.IsAbs(refs[0].Command) {
		t.Errorf("bare command = %q, want absolute", refs[0].Command)
	}
	if _, err := os.Stat(refs[0].Command); err != nil {
		t.Errorf("resolved command does not exist: %v", err)
	}

	cwd := t.TempDir()
	local := filepath.Join(cwd, "tool.exe")
	if err := os.WriteFile(local, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	refs, err = resolveMCPRefs([]McpServerRef{{Name: "local", Command: "./tool.exe"}}, cwd)
	if err != nil {
		t.Fatalf("relative command: %v", err)
	}
	if refs[0].Command != local {
		t.Errorf("relative command = %q, want %q", refs[0].Command, local)
	}

	if _, err := resolveMCPRefs([]McpServerRef{{Name: "missing", Command: "definitely-not-on-path-xyz"}}, cwd); err == nil {
		t.Error("missing bare command should fail")
	}
}

// TestServer_sessionNewRequiresAbsoluteCwd pins the ACP cwd rules: a
// missing or relative cwd is invalid params, not a silent fallback to
// the process directory.
func TestServer_sessionNewRequiresAbsoluteCwd(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
	initialize(t, h)
	for i, params := range []string{
		`{"mcpServers":[]}`,
		`{"cwd":"relative/dir","mcpServers":[]}`,
	} {
		if err := h.send("session/new", i+2, params); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error == nil || msg.Error.Code != codeInvalidParams {
			t.Fatalf("%s: response = %+v, want invalid params", params, msg)
		}
	}
}

// TestServer_parseErrorRepliesWithNullID covers JSON-RPC 2.0's parse
// error contract and keeps the connection usable.
func TestServer_parseErrorRepliesWithNullID(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
	if _, err := io.WriteString(h.inW, "{not json\n"); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeParseError {
		t.Fatalf("response = %+v, want parse error", msg)
	}
	if string(msg.ID) != "null" {
		t.Fatalf("parse error id = %s, want null", msg.ID)
	}
	initialize(t, h)
}

// TestServer_invalidRequestRepliesWith32600 covers a request with no
// method.
func TestServer_invalidRequestRepliesWith32600(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
	if err := h.send("", 9, ""); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeInvalidRequest {
		t.Fatalf("response = %+v, want invalid request", msg)
	}
}

// TestServer_emptyPromptRejected keeps empty prompt blocks out.
func TestServer_emptyPromptRejected(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
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

// recvRequest waits for a specific server-initiated request, skipping
// session/update notifications (a permission request is preceded by the
// pending tool_call it belongs to).
func (h *acpHarness) recvRequest(t *testing.T, method string) Message {
	t.Helper()
	for i := 0; i < 8; i++ {
		msg := h.recv(t)
		if msg.Method == method {
			return msg
		}
	}
	t.Fatalf("did not receive %s", method)
	return Message{}
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
	sess := &fakeSession{id: "sess-test"}
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
	announced := h.recv(t)
	if announced.Method != "session/update" {
		t.Fatalf("expected the pending tool_call update first, got %+v", announced)
	}
	var announcedEnv updateEnvelope
	if err := json.Unmarshal(announced.Params, &announcedEnv); err != nil {
		t.Fatalf("announce envelope: %v", err)
	}
	var announcedCall ToolCallUpdate
	if err := json.Unmarshal(announcedEnv.Update, &announcedCall); err != nil {
		t.Fatalf("announce update: %v", err)
	}
	if announcedCall.SessionUpdate != UpdateToolCall || announcedCall.ToolCallID != "call-1" || announcedCall.Status != StatusPending {
		t.Fatalf("pending tool_call = %+v, want tool_call call-1 pending", announcedCall)
	}

	msg := h.recvRequest(t, "session/request_permission")
	var params RequestPermissionParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("request_permission params: %v", err)
	}
	if params.ToolCall.ToolCallID != "call-1" || params.ToolCall.Title == "" {
		t.Fatalf("permission request must reuse the call id: %+v", params.ToolCall)
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
	sess := &fakeSession{id: "sess-test"}
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
	msg := h.recvRequest(t, "session/request_permission")
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
				up.Status == StatusFailed && up.ToolCallID == "call-1" {
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
	sess := &fakeSession{id: "sess-test"}
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
	_ = h.recvRequest(t, "session/request_permission")
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
	sess := &fakeSession{id: "sess-test"}
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
		id: "sess-test",
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
	s := NewServer(func(BuildOptions) (Session, error) { return &fakeSession{id: "sess-test"}, nil }, "t", nil)
	if !s.AutoAnswerQuestions || !s.AutoContinue {
		t.Fatal("defaults must enable auto-answer/auto-continue")
	}
}
