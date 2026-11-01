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
	"github.com/buchenberg/yaah/internal/worldinfo"
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
	// provider and model track SetModel, so config-option tests can
	// assert the switch reached the session.
	provider string
	model    string
	// mode tracks SetMode for session-mode tests.
	mode string
	// budgetExhausted is what TurnBudgetExhausted reports; stop-reason
	// tests set it to simulate a soft tool-turn cap ending the turn.
	budgetExhausted bool
	// reg, when set, is what ToolReg returns; question-tool and
	// elicitation tests register a question tool there.
	reg *tools.Registry
	// world is what WorldStatus reports; session/new and session/load
	// tests set it to pin the `_meta` block.
	world worldinfo.Status
}

func (f *fakeSession) TurnBudgetExhausted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.budgetExhausted
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

func (f *fakeSession) ToolReg() *tools.Registry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reg != nil {
		return f.reg
	}
	return tools.NewRegistry()
}
func (f *fakeSession) AgentSessionID() string { return f.id }

func (f *fakeSession) WorldStatus() worldinfo.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.world
}

func (f *fakeSession) SetModel(provider, model string) {
	f.mu.Lock()
	f.provider = provider
	f.model = model
	f.mu.Unlock()
}

func (f *fakeSession) ProviderName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.provider
}

func (f *fakeSession) ModelName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.model
}

func (f *fakeSession) Mode() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mode == "" {
		return SessionModeBuild
	}
	return f.mode
}

func (f *fakeSession) SetMode(mode string) error {
	if !validSessionMode(mode) {
		return fmt.Errorf("unknown mode %q", mode)
	}
	f.mu.Lock()
	f.mode = mode
	f.mu.Unlock()
	return nil
}

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
		catalog:             nil,
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

// fakeModelCatalog is a static ModelCatalog for picker tests.
type fakeModelCatalog struct{ options []ModelOption }

func (c fakeModelCatalog) Options(context.Context) []ModelOption { return c.options }

// pickerCatalog is the catalog the picker-advertisement tests wire.
func pickerCatalog() fakeModelCatalog {
	return fakeModelCatalog{options: []ModelOption{
		{Provider: "openai", Model: "gpt-5", Name: "gpt-5 (OpenAI)"},
		{Provider: "deepseek", Model: "deepseek-flash"},
	}}
}

// assertModelPicker checks one decoded configOptions set: the single
// model-category select with both catalog values and the given current
// selection.
func assertModelPicker(t *testing.T, opts []SessionConfigOption, current string) {
	t.Helper()
	if len(opts) != 1 {
		t.Fatalf("configOptions = %+v, want exactly the model picker", opts)
	}
	picker := opts[0]
	if picker.ID != ConfigOptionModelID || picker.Category != ConfigOptionCategoryModel || picker.Type != ConfigOptionTypeSelect {
		t.Fatalf("model picker = %+v, want id=%s category=%s type=%s", picker, ConfigOptionModelID, ConfigOptionCategoryModel, ConfigOptionTypeSelect)
	}
	if picker.CurrentValue != current {
		t.Errorf("currentValue = %q, want %q", picker.CurrentValue, current)
	}
	values := make([]string, 0, len(picker.Options))
	for _, o := range picker.Options {
		values = append(values, o.Value)
	}
	want := []string{"openai/gpt-5", "deepseek/deepseek-flash"}
	if len(values) != len(want) || values[0] != want[0] || values[1] != want[1] {
		t.Errorf("option values = %v, want %v", values, want)
	}
}

// TestServer_modelPickerAdvertisedOnSessionNewAndLoad pins the wire
// contract Zed's model selector depends on: session/new and
// session/load results carry configOptions (the model-category select),
// built from the wired catalog and the session's current model. With no
// catalog wired, the field is omitted entirely.
func TestServer_modelPickerAdvertisedOnSessionNewAndLoad(t *testing.T) {
	t.Run("session/new advertises the picker", func(t *testing.T) {
		h := newACPHarness(t, &fakeSession{id: "sess-m1", provider: "openai", model: "gpt-5"})
		h.server.catalog = pickerCatalog()
		initialize(t, h)
		cwd, _ := json.Marshal(h.cwd)
		if err := h.send("session/new", 2, fmt.Sprintf(`{"cwd":%s,"mcpServers":[]}`, cwd)); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error != nil {
			t.Fatalf("session/new error: %+v", msg.Error)
		}
		var result SessionNewResult
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			t.Fatalf("session/new result: %v", err)
		}
		assertModelPicker(t, result.ConfigOptions, "openai/gpt-5")
		assertSessionModes(t, result.Modes, SessionModeBuild)
	})

	t.Run("session/load re-advertises the picker", func(t *testing.T) {
		h := newACPHarness(t, &fakeSession{id: "sess-m2", provider: "deepseek", model: "deepseek-flash"})
		h.server.catalog = pickerCatalog()
		initialize(t, h)
		if err := h.send("session/load", 2, loadParams(t, h, "sess-m2")); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error != nil {
			t.Fatalf("session/load error: %+v", msg.Error)
		}
		var result SessionLoadResult
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			t.Fatalf("session/load result: %v", err)
		}
		assertModelPicker(t, result.ConfigOptions, "deepseek/deepseek-flash")
		assertSessionModes(t, result.Modes, SessionModeBuild)
	})

	t.Run("no catalog omits configOptions", func(t *testing.T) {
		h := newACPHarness(t, nil)
		initialize(t, h)
		cwd, _ := json.Marshal(h.cwd)
		if err := h.send("session/new", 2, fmt.Sprintf(`{"cwd":%s,"mcpServers":[]}`, cwd)); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error != nil {
			t.Fatalf("session/new error: %+v", msg.Error)
		}
		if strings.Contains(string(msg.Result), "configOptions") {
			t.Errorf("session/new result = %s, want no configOptions without a wired catalog", msg.Result)
		}
	})
}

// TestServer_setConfigOptionSwitchesModel pins the picker round trip: a
// client echoing back an advertised value switches the session's model,
// and the response carries the recomputed picker state.
func TestServer_setConfigOptionSwitchesModel(t *testing.T) {
	sess := &fakeSession{id: "sess-m3", provider: "openai", model: "gpt-5"}
	h := newACPHarness(t, sess)
	h.server.catalog = pickerCatalog()
	initialize(t, h)
	if id := newSession(t, h); id != "sess-m3" {
		t.Fatalf("session/new returned %q, want sess-m3", id)
	}
	if err := h.send("session/set_config_option", 2, `{"sessionId":"sess-m3","configId":"model","value":"deepseek/deepseek-flash"}`); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error != nil {
		t.Fatalf("set_config_option error: %+v", msg.Error)
	}
	var result SetConfigOptionResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("set_config_option result: %v", err)
	}
	assertModelPicker(t, result.ConfigOptions, "deepseek/deepseek-flash")
	if got := sess.ModelName(); got != "deepseek-flash" {
		t.Errorf("session model = %q, want deepseek-flash", got)
	}
	if got := sess.ProviderName(); got != "deepseek" {
		t.Errorf("session provider = %q, want deepseek", got)
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
	assertLoadResultModes(t, resp.Result, SessionModeBuild)
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
	assertLoadResultModes(t, msg.Result, SessionModeBuild)
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
	assertLoadResultModes(t, resp.Result, SessionModeBuild)
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

func assertLoadResultModes(t *testing.T, raw json.RawMessage, current string) {
	t.Helper()
	var result SessionLoadResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("load result: %v", err)
	}
	assertSessionModes(t, result.Modes, current)
}

func assertSessionModes(t *testing.T, state *SessionModeState, current string) {
	t.Helper()
	if state == nil {
		t.Fatal("session modes missing")
	}
	if state.CurrentModeID != current {
		t.Errorf("currentModeId = %q, want %q", state.CurrentModeID, current)
	}
	ids := make(map[string]bool)
	for _, mode := range state.AvailableModes {
		if mode.ID == "" || mode.Name == "" {
			t.Errorf("mode = %+v, want id and name", mode)
		}
		ids[mode.ID] = true
	}
	if !ids[SessionModeBuild] || !ids[SessionModePlan] {
		t.Errorf("available modes = %+v, want build and plan", state.AvailableModes)
	}
}

// TestServer_sessionModes pins the Plan/Build picker: session/new advertises
// both modes, set_mode switches the session, emits current_mode_update, and
// rejects unknown ids.
func TestServer_sessionModes(t *testing.T) {
	sess := &fakeSession{id: "sess-modes"}
	h := newACPHarness(t, sess)
	initialize(t, h)
	cwd, _ := json.Marshal(h.cwd)
	if err := h.send("session/new", 2, fmt.Sprintf(`{"cwd":%s,"mcpServers":[]}`, cwd)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	var created SessionNewResult
	if err := json.Unmarshal(msg.Result, &created); err != nil {
		t.Fatalf("session/new result: %v", err)
	}
	assertSessionModes(t, created.Modes, SessionModeBuild)
	if sess.Mode() != SessionModeBuild {
		t.Fatalf("fresh session mode = %q, want build", sess.Mode())
	}

	if err := h.send("session/set_mode", 4, fmt.Sprintf(`{"sessionId":%q,"modeId":"plan"}`, created.SessionID)); err != nil {
		t.Fatal(err)
	}
	update := h.recv(t)
	if update.Method != "session/update" {
		t.Fatalf("first set_mode message = %+v, want current_mode_update notification", update)
	}
	var params struct {
		SessionID string            `json:"sessionId"`
		Update    CurrentModeUpdate `json:"update"`
	}
	if err := json.Unmarshal(update.Params, &params); err != nil {
		t.Fatalf("mode update params: %v", err)
	}
	if params.SessionID != created.SessionID || params.Update.SessionUpdate != "current_mode_update" || params.Update.CurrentModeID != SessionModePlan {
		t.Fatalf("mode update = %+v", params)
	}
	if ack := h.recv(t); ack.Error != nil || !strings.Contains(string(ack.ID), "4") {
		t.Fatalf("set_mode ack = %+v", ack)
	}
	if sess.Mode() != SessionModePlan {
		t.Fatalf("session mode = %q, want plan", sess.Mode())
	}

	if err := h.send("session/set_mode", 5, fmt.Sprintf(`{"sessionId":%q,"modeId":"bogus"}`, created.SessionID)); err != nil {
		t.Fatal(err)
	}
	rejected := h.recv(t)
	if rejected.Error == nil || rejected.Error.Code != codeInvalidParams {
		t.Fatalf("unknown mode response = %+v, want invalid params", rejected)
	}
	if sess.Mode() != SessionModePlan {
		t.Fatalf("rejected mode switched the session to %q", sess.Mode())
	}
}

// TestServer_setModeSameModeIsSilent keeps the ack fast when the client
// echoes the mode it already has.
func TestServer_setModeSameModeIsSilent(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-test"})
	initialize(t, h)
	sid := newSession(t, h)
	if err := h.send("session/set_mode", 4, fmt.Sprintf(`{"sessionId":%q,"modeId":"build"}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error != nil || msg.Method == "session/update" {
		t.Fatalf("set_mode same mode = %+v, want plain ack", msg)
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

// TestServer_promptStopReasonMaxTurnRequests covers both budget ends: a
// turn that produced text only after the soft cap stripped tools, and a
// hard iteration-limit error. Both must answer max_turn_requests so the
// client can offer a continuation instead of treating the turn as done.
func TestServer_promptStopReasonMaxTurnRequests(t *testing.T) {
	tests := []struct {
		name string
		sess *fakeSession
	}{
		{
			name: "soft cap stripped tools",
			sess: &fakeSession{
				id:              "sess-test",
				budgetExhausted: true,
				prompt: func(ctx context.Context, prompt string) (string, bool, error) {
					return "I was about to keep going", false, nil
				},
			},
		},
		{
			name: "iteration limit",
			sess: &fakeSession{
				id: "sess-test",
				prompt: func(ctx context.Context, prompt string) (string, bool, error) {
					return "", false, agent.MaxIterationsError{MaxIter: 5}
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newACPHarness(t, tc.sess)
			initialize(t, h)
			sid := newSession(t, h)

			if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"do a thing"}]}`, sid)); err != nil {
				t.Fatal(err)
			}
			msg := h.recv(t)
			if msg.Error != nil {
				t.Fatalf("prompt produced an RPC error, want stopReason max_turn_requests: %+v", msg)
			}
			var result PromptResult
			if err := json.Unmarshal(msg.Result, &result); err != nil {
				t.Fatalf("prompt result: %v (raw=%s)", err, msg.Result)
			}
			if result.StopReason != StopMaxTurnRequests {
				t.Fatalf("stopReason = %q, want %q", result.StopReason, StopMaxTurnRequests)
			}
		})
	}
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
	if err := h.send("session/set_mode", 4, fmt.Sprintf(`{"sessionId":%q,"modeId":"build"}`, sid)); err != nil {
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

func TestServer_multimodalPromptBlocksRejected(t *testing.T) {
	for _, blockType := range []string{"image", "audio"} {
		t.Run(blockType, func(t *testing.T) {
			h := newACPHarness(t, &fakeSession{id: "sess-test"})
			initialize(t, h)
			sid := newSession(t, h)
			params := fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":%q,"data":"AAAA","mimeType":"application/octet-stream"}]}`, sid, blockType)
			if err := h.send("session/prompt", 6, params); err != nil {
				t.Fatal(err)
			}
			msg := h.recv(t)
			if msg.Error == nil || msg.Error.Code != codeInvalidParams {
				t.Fatalf("%s prompt response = %+v, want invalid params", blockType, msg)
			}
			if !strings.Contains(msg.Error.Message, blockType) {
				t.Errorf("error %q does not name %s", msg.Error.Message, blockType)
			}
		})
	}
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
	s := NewServer(func(BuildOptions) (Session, error) { return &fakeSession{id: "sess-test"}, nil }, "t", nil, nil)
	if !s.AutoAnswerQuestions || !s.AutoContinue {
		t.Fatal("defaults must enable auto-answer/auto-continue")
	}
}

// TestServer_permissionAlwaysKindsOffered checks the decision menu:
// all four option kinds (allow/reject × once/always) are offered so
// rendering clients enable their "Always for <tool>" buttons.
func TestServer_permissionAlwaysKindsOffered(t *testing.T) {
	sess := &fakeSession{id: "sess-test"}
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		_ = sess.approveCall("bash", `{"command":"ls"}`)
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"exec"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recvRequest(t, "session/request_permission")
	var params RequestPermissionParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("request_permission params: %v", err)
	}
	kinds := make([]string, 0, len(params.Options))
	for _, o := range params.Options {
		kinds = append(kinds, o.Kind)
		if o.OptionID == "" || o.Name == "" {
			t.Fatalf("option missing id/name: %+v", o)
		}
	}
	want := []string{PermAllowOnce, PermAllowAlways, PermRejectOnce, PermRejectAlways}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("option kinds = %v, want %v", kinds, want)
	}
	if err := h.reply(msg.ID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}); err != nil {
		t.Fatal(err)
	}
}

// TestServer_permissionAlwaysRemembers checks that allow_always
// suppresses the next request for the same tool within the session:
// two approveCall rounds produce exactly one permission request.
func TestServer_permissionAlwaysRemembers(t *testing.T) {
	sess := &fakeSession{id: "sess-test"}
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		g1 := sess.approveCall("bash", `{"command":"first"}`)
		g2 := sess.approveCall("bash", `{"command":"second"}`)
		if !g1 || !g2 {
			t.Errorf("both calls must be granted, got %v/%v", g1, g2)
		}
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"exec twice"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recvRequest(t, "session/request_permission")
	if err := h.reply(msg.ID, map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "allow-always"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		msg = h.recv(t)
		if msg.Method == "session/request_permission" {
			t.Fatal("always-allow must suppress the second permission request")
		}
		if msg.Method == "" && msg.Error == nil {
			var result PromptResult
			if err := json.Unmarshal(msg.Result, &result); err == nil && result.StopReason == StopEndTurn {
				return
			}
		}
	}
	t.Fatal("prompt never completed")
}

// TestServer_permissionRejectAlwaysDenies checks that reject_always
// auto-denies the next call for that tool without a second request,
// and a different tool still gets asked.
func TestServer_permissionRejectAlwaysDenies(t *testing.T) {
	sess := &fakeSession{id: "sess-test"}
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		d1 := !sess.approveCall("bash", `{"command":"first"}`)
		d2 := !sess.approveCall("bash", `{"command":"second"}`)
		g3 := sess.approveCall("edit", `{"filePath":"/repo/a.go"}`)
		if !d1 || !d2 || !g3 {
			t.Errorf("bash must be denied twice, edit granted, got %v/%v/%v", d1, d2, g3)
		}
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"exec"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recvRequest(t, "session/request_permission")
	if err := h.reply(msg.ID, map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "reject-always"},
	}); err != nil {
		t.Fatal(err)
	}
	// The edit call (never remembered) must still promote one request.
	for i := 0; i < 16; i++ {
		msg = h.recv(t)
		if msg.Method == "session/request_permission" {
			var params RequestPermissionParams
			_ = json.Unmarshal(msg.Params, &params)
			if params.ToolCall.Name != "edit" {
				t.Fatalf("request for remembered tool: %+v", params.ToolCall)
			}
			if err := h.reply(msg.ID, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow-once"}}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if msg.Method == "" && msg.Error == nil {
			var result PromptResult
			if err := json.Unmarshal(msg.Result, &result); err == nil && result.StopReason == StopEndTurn {
				return
			}
		}
	}
	t.Fatal("prompt never completed")
}

// TestServer_questionElicitation drives the question-tool path when the
// client advertises elicitation.form: the modal arrives as
// elicitation/create, the chosen label reaches the session, and a
// second (headless) session on an unadvertising client auto-answers.
func TestServer_questionElicitation(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	qt := &tools.QuestionTool{}
	reg.Register(qt)
	sess := &fakeSession{id: "sess-test", reg: reg}
	var answer string
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		reg := sess.ToolReg()
		qt := reg.Get("question").(*tools.QuestionTool)
		if qt.Handler == nil {
			t.Fatal("question tool handler not installed")
		}
		answers := qt.Handler([]tools.QuestionEntry{{
			Question: "which way?", Header: "Direction",
			Options: []tools.QuestionOption{{Label: "left", Description: "go left"}, {Label: "right", Description: "go right"}},
		}})
		answer = answers[0]
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	// Handshake with the form capability.
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{"elicitation":{"form":true}},"clientInfo":{"name":"test-client","version":"1"}}`); err != nil {
		t.Fatal(err)
	}
	h.recv(t)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"ask"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recvRequest(t, "elicitation/create")
	var ep ElicitationParams
	if err := json.Unmarshal(msg.Params, &ep); err != nil {
		t.Fatalf("elicitation params: %v", err)
	}
	if ep.Mode != ElicitationModeForm || ep.SessionID != sid || ep.Message != "Direction: which way?" {
		t.Fatalf("elicitation = %+v", ep)
	}
	var schema map[string]any
	if err := json.Unmarshal(ep.RequestedSchema, &schema); err != nil {
		t.Fatalf("requestedSchema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("requestedSchema must be an object schema: %v", schema)
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil || props["answer"] == nil {
		t.Fatalf("schema missing answer property: %v", schema)
	}
	if err := h.reply(msg.ID, map[string]any{
		"action": "accept", "content": map[string]any{"answer": "right"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		msg = h.recv(t)
		if msg.Method == "" && msg.Error == nil {
			// The turn is over; the tool result carries the answer.
			if answer != "Direction: right" {
				t.Fatalf("answer = %q, want the chosen label", answer)
			}
			return
		}
	}
	t.Fatal("prompt never completed after elicitation")
}

// TestServer_questionAutoAnswerWithoutElicitation checks the legacy
// fallback: without the form capability the question renders as
// status prose and auto-answers with the first option.
func TestServer_questionAutoAnswerWithoutElicitation(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	qt := &tools.QuestionTool{}
	reg.Register(qt)
	sess := &fakeSession{id: "sess-test", reg: reg}
	var answer string
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		qt := sess.ToolReg().Get("question").(*tools.QuestionTool)
		answer = qt.Handler([]tools.QuestionEntry{{
			Question: "which way?", Header: "Direction",
			Options: []tools.QuestionOption{{Label: "left"}, {Label: "right"}},
		}})[0]
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"ask"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	var sawStatus bool
	for i := 0; i < 16; i++ {
		msg := h.recv(t)
		if msg.Method == "session/update" {
			var env updateEnvelope
			if err := json.Unmarshal(msg.Params, &env); err != nil {
				continue
			}
			var chunk ChunkUpdate
			if err := json.Unmarshal(env.Update, &chunk); err == nil &&
				chunk.SessionUpdate == UpdateAgentMessageChunk &&
				strings.Contains(chunk.Content.Text, "Direction") {
				sawStatus = true
			}
			continue
		}
		if msg.Method == "" && msg.Error == nil && sawStatus {
			if answer != "Direction: left" {
				t.Fatalf("answer = %q, want first option auto-answered", answer)
			}
			return
		}
	}
	t.Fatal("turn never completed")
}

// TestServer_questionElicitationDeclineStaysEmpty checks that a
// declined modal is recorded as an empty answer — the auto-answer
// default must not override a user who was offered the question and
// declined it.
func TestServer_questionElicitationDeclineStaysEmpty(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	qt := &tools.QuestionTool{}
	reg.Register(qt)
	sess := &fakeSession{id: "sess-test", reg: reg}
	var answer string
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		qt := sess.ToolReg().Get("question").(*tools.QuestionTool)
		answer = qt.Handler([]tools.QuestionEntry{{
			Question: "proceed?", Header: "Go",
			Options: []tools.QuestionOption{{Label: "yes"}, {Label: "no"}},
		}})[0]
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{"elicitation":{"form":true}}}`); err != nil {
		t.Fatal(err)
	}
	h.recv(t)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"ask"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recvRequest(t, "elicitation/create")
	if err := h.reply(msg.ID, map[string]any{"action": "decline"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		msg = h.recv(t)
		if msg.Method == "" && msg.Error == nil {
			if answer != "Go: " {
				t.Fatalf("answer = %q, want empty after decline", answer)
			}
			return
		}
	}
	t.Fatal("prompt never completed after decline")
}

// TestServer_questionElicitationObjectCapability checks the
// spec-shaped capability payload: eligibility.form arrives as the
// schema's empty capability object, exactly the shape from the ACP
// schema, and must light up the elicitation path like the boolean
// spelling does.
func TestServer_questionElicitationObjectCapability(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	qt := &tools.QuestionTool{}
	reg.Register(qt)
	sess := &fakeSession{id: "sess-test", reg: reg}
	var answer string
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		qt := sess.ToolReg().Get("question").(*tools.QuestionTool)
		answer = qt.Handler([]tools.QuestionEntry{{
			Question: "which way?", Header: "Direction",
			Options: []tools.QuestionOption{{Label: "left"}, {Label: "right"}},
		}})[0]
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	// The ElicitationFormCapabilities variant on the wire: {} (not a bool).
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{"elicitation":{"form":{},"url":{}}}}`); err != nil {
		t.Fatal(err)
	}
	h.recv(t)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"ask"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recvRequest(t, "elicitation/create")
	if err := h.reply(msg.ID, map[string]any{
		"action": "accept", "content": map[string]any{"answer": "left"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		msg = h.recv(t)
		if msg.Method == "" && msg.Error == nil {
			if answer != "Direction: left" {
				t.Fatalf("answer = %q, want the chosen label", answer)
			}
			return
		}
	}
	t.Fatal("prompt never completed after elicitation")
}

// TestServer_questionElicitationGarbageCapability checks that a
// malformed elicitation capability payload counts as not advertised
// instead of failing the initialize decode entirely.
func TestServer_questionElicitationGarbageCapability(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	qt := &tools.QuestionTool{}
	reg.Register(qt)
	sess := &fakeSession{id: "sess-test", reg: reg}
	var answer string
	sess.mu.Lock()
	sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
		qt := sess.ToolReg().Get("question").(*tools.QuestionTool)
		answer = qt.Handler([]tools.QuestionEntry{{
			Question: "which way?", Header: "Direction",
			Options: []tools.QuestionOption{{Label: "left"}, {Label: "right"}},
		}})[0]
		return "done", false, nil
	}
	sess.mu.Unlock()
	h := newACPHarness(t, sess)
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{"elicitation":{"form":42}}}`); err != nil {
		t.Fatal(err)
	}
	h.recv(t)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"ask"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	// No elicitation request may arrive; the legacy auto-answer path
	// (first option) answers the turn.
	var sawElicitation bool
	for i := 0; i < 16; i++ {
		msg := h.recv(t)
		if msg.Method == "elicitation/create" {
			sawElicitation = true
		}
		if msg.Method == "" && msg.Error == nil {
			if sawElicitation {
				t.Fatal("garbage capability must not route through elicitation")
			}
			if answer != "Direction: left" {
				t.Fatalf("answer = %q, want auto-answered first option", answer)
			}
			return
		}
	}
	t.Fatal("turn never completed")
}

// TestServer_compactionCapabilityGated checks the full live path: a
// client advertising session.compaction gets the ID-addressed
// compaction_update / compaction_summary_chunk updates for compaction
// events emitted during a prompt turn; a client without the cap gets
// the plain text fallback.
func TestServer_compactionCapabilityGated(t *testing.T) {
	run := func(t *testing.T, initCaps string) []Message {
		sess := &fakeSession{id: "sess-test"}
		sess.mu.Lock()
		sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
			sess.mu.Lock()
			v := sess.view
			sess.mu.Unlock()
			v.HandleEvent(&agent.CompactionStartedEvent{BeforeTokens: 10, TargetTokens: 4, Reason: "threshold"})
			v.HandleEvent(&agent.CompactionDoneEvent{BeforeTokens: 10, AfterTokens: 4, SavingsPct: 0.5, Method: "single", ElapsedSeconds: 2})
			return "ok", false, nil
		}
		sess.mu.Unlock()
		h := newACPHarness(t, sess)
		if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":`+initCaps+`}`); err != nil {
			t.Fatal(err)
		}
		h.recv(t)
		sid := newSession(t, h)
		if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"go"}]}`, sid)); err != nil {
			t.Fatal(err)
		}
		var updates []Message
		for i := 0; i < 16; i++ {
			msg := h.recv(t)
			if msg.Method == "" && msg.Error == nil {
				break
			}
			updates = append(updates, msg)
		}
		return updates
	}

	t.Run("with cap", func(t *testing.T) {
		updates := run(t, `{"session":{"compaction":{}}}`)
		var sawUpsert, sawChunk bool
		for _, m := range updates {
			params := map[string]any{}
			if json.Unmarshal([]byte(m.Params), &params) != nil {
				continue
			}
			params, _ = params["update"].(map[string]any)
			if params == nil {
				continue
			}
			switch params["sessionUpdate"] {
			case UpdateCompaction:
				sawUpsert = true
				if params["status"] != CompactionInProgress && params["status"] != CompactionCompleted {
					t.Fatalf("upsert status = %v", params["status"])
				}
				if params["compactionId"] != "cmp-1" {
					t.Fatalf("compactionId = %v", params["compactionId"])
				}
			case UpdateCompactionSummaryChunk:
				sawChunk = true
				if params["compactionId"] != "cmp-1" {
					t.Fatalf("chunk compactionId = %v", params["compactionId"])
				}
			}
		}
		if !sawUpsert || !sawChunk {
			t.Fatalf("missed variants: upsert=%v chunk=%v in updates %v", sawUpsert, sawChunk, updates)
		}
	})

	t.Run("with cap, second turn", func(t *testing.T) {
		// The id allocator is session-lifetime: a fresh view for a new
		// prompt turn must continue the sequence, not reset to cmp-1
		// (which would patch the previous turn's card in place).
		sess := &fakeSession{id: "sess-test"}
		turns := 0
		sess.mu.Lock()
		sess.prompt = func(ctx context.Context, prompt string) (string, bool, error) {
			sess.mu.Lock()
			v := sess.view
			sess.mu.Unlock()
			turns++
			v.HandleEvent(&agent.CompactionStartedEvent{BeforeTokens: turns, TargetTokens: turns, Reason: "threshold"})
			return "ok", false, nil
		}
		sess.mu.Unlock()
		h := newACPHarness(t, sess)
		if err := h.send("initialize", 1, `{"protocolVersion":1,"clientCapabilities":{"session":{"compaction":{}}}}`); err != nil {
			t.Fatal(err)
		}
		h.recv(t)
		sid := newSession(t, h)
		lastID := ""
		for n := 1; n <= 2; n++ {
			if err := h.send("session/prompt", n+3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"go"}]}`, sid)); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("cmp-%d", n)
			for i := 0; i < 16; i++ {
				msg := h.recv(t)
				if msg.Method == "" && msg.Error == nil {
					break
				}
				if len(msg.Params) == 0 {
					continue
				}
				var params struct {
					Update json.RawMessage `json:"update"`
				}
				if json.Unmarshal([]byte(msg.Params), &params) != nil {
					continue
				}
				var upd struct {
					SessionUpdate string           `json:"sessionUpdate"`
					CompactionID  string           `json:"compactionId"`
					Summary       []map[string]any `json:"summary"`
				}
				if json.Unmarshal(params.Update, &upd) != nil || upd.SessionUpdate != UpdateCompaction {
					continue
				}
				if upd.CompactionID != want {
					t.Fatalf("turn %d card id = %v, want %v (id namespace must not reset per turn)", n, upd.CompactionID, want)
				}
				if len(upd.Summary) != 1 || upd.Summary[0]["text"] != fmt.Sprintf("compacting (%d→%d tokens, threshold)", n, n) {
					t.Fatalf("turn %d summary = %v, want the readable start line", n, upd.Summary)
				}
				lastID = upd.CompactionID
			}
			if lastID != want {
				t.Fatalf("turn %d: no compaction_update with id %v observed", n, want)
			}
		}
	})

	t.Run("without cap", func(t *testing.T) {
		updates := run(t, `{}`)
		if len(updates) == 0 {
			t.Fatal("no updates were emitted; gating must not suppress the stream")
		}
		sawFallback := false
		for _, m := range updates {
			params := map[string]any{}
			if json.Unmarshal([]byte(m.Params), &params) != nil {
				continue
			}
			params, _ = params["update"].(map[string]any)
			if params == nil {
				continue
			}
			variant, _ := params["sessionUpdate"].(string)
			if variant == UpdateCompaction || variant == UpdateCompactionSummaryChunk {
				t.Fatalf("structured compaction update leaked without capability: %v", params)
			}
			if variant == UpdateAgentMessageChunk {
				content, _ := params["content"].(map[string]any)
				if text, _ := content["text"].(string); strings.HasPrefix(text, "[compacting") {
					sawFallback = true
				}
			}
		}
		if !sawFallback {
			t.Fatal("documented plain-text compaction fallback (agent_message_chunk '[compacting …]') not emitted")
		}
	})
}
