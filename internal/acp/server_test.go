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
	mu     sync.Mutex
	prompt func(ctx context.Context, prompt string) (string, bool, error)
	view   agent.View
	ctrl   chan<- control.Msg
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
func (f *fakeSession) Close()                   {}
func (f *fakeSession) ToolReg() *tools.Registry { return tools.NewRegistry() }

// acpHarness wires a Server to in-memory pipes and exposes
// send/recv with timeouts.
type acpHarness struct {
	send  func(method string, id any, params string) error
	recv  func(t *testing.T) Message
	close context.CancelFunc
}

func newACPHarness(t *testing.T, sess Session) *acpHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	s := &Server{
		sess:                sess,
		version:             "test",
		logf:                func(string, ...any) {},
		stdin:               inR,
		stdout:              outW,
		AutoAnswerQuestions: true,
		AutoContinue:        true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = s.Run(ctx)
		_ = inW.Close()
	}()

	lines := make(chan string, 32)
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
		send: func(method string, id any, params string) error {
			msg := Message{JSONRPC: "2.0", ID: id, Method: method}
			if params != "" {
				msg.Params = json.RawMessage(params)
			}
			data, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			_, err = io.WriteString(inW, string(data)+"\n")
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
		close: cancel,
	}
}

func TestServer_unknownMethodReturns32601(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	if err := h.send("bogus/method", 7, ""); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != -32601 {
		t.Fatalf("response = %+v, want -32601 method not found", msg)
	}
	if !strings.Contains(msg.Error.Message, "bogus/method") {
		t.Errorf("error message %q should name the method", msg.Error.Message)
	}
}

func TestServer_initializeCamelCase(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	if err := h.send("initialize", 1, ""); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	raw := string(msg.Result)
	if !strings.Contains(raw, `"protocolVersion"`) {
		t.Errorf("initialize result uses wrong casing: %s", raw)
	}
	if strings.Contains(raw, "protocol_version") || strings.Contains(raw, "server_info") {
		t.Errorf("initialize result still carries snake_case fields: %s", raw)
	}
}

func TestServer_sessionPromptRequiresKnownSession(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})

	params := `{"sessionId":"sess-unknown","prompt":[{"type":"text","text":"hi"}]}`
	if err := h.send("session/prompt", 2, params); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != -32602 {
		t.Fatalf("response = %+v, want -32602 unknown session", msg)
	}
}

func TestServer_promptStopReason(t *testing.T) {
	sess := &fakeSession{}
	h := newACPHarness(t, sess)

	if err := h.send("session/new", 1, ""); err != nil {
		t.Fatal(err)
	}
	created := h.recv(t)
	var newResult SessionNewResult
	if err := json.Unmarshal(created.Result, &newResult); err != nil {
		t.Fatalf("session/new result: %v", err)
	}

	params := fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"do a thing"}]}`, newResult.SessionID)
	if err := h.send("session/prompt", 3, params); err != nil {
		t.Fatal(err)
	}

	// First: the immediate ack.
	ack := h.recv(t)
	if ack.ID == nil || ack.Error != nil {
		t.Fatalf("ack = %+v, want result for id 3", ack)
	}

	// Then: the stop-reason update for the completed prompt.
	for {
		msg := h.recv(t)
		if msg.Method == "session/update" {
			var params SessionUpdate
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				t.Fatalf("session/update params: %v", err)
			}
			if params.Update.SessionUpdate == "stop_reason" {
				if params.Update.StopReason != "end_turn" {
					t.Fatalf("stopReason = %q, want end_turn", params.Update.StopReason)
				}
				return
			}
		}
	}
}

func TestServer_cancelWhilePromptRuns(t *testing.T) {
	started := make(chan struct{})
	sess := &fakeSession{
		prompt: func(ctx context.Context, prompt string) (string, bool, error) {
			close(started)
			<-ctx.Done()
			return "", false, ctx.Err()
		},
	}
	h := newACPHarness(t, sess)

	if err := h.send("session/new", 1, ""); err != nil {
		t.Fatal(err)
	}
	created := h.recv(t)
	var newResult SessionNewResult
	if err := json.Unmarshal(created.Result, &newResult); err != nil {
		t.Fatalf("session/new result: %v", err)
	}

	params := fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"long task"}]}`, newResult.SessionID)
	if err := h.send("session/prompt", 3, params); err != nil {
		t.Fatal(err)
	}
	ack := h.recv(t)
	if ack.ID == nil {
		t.Fatalf("expected prompt ack, got %+v", ack)
	}

	<-started

	// The read goroutine must stay responsive while the prompt runs —
	// a method reply arriving now proves the reader is not blocked on
	// the prompt (review B11).
	if err := h.send("session/set_mode", 4, `{"modeId":"x"}`); err != nil {
		t.Fatal(err)
	}

	if err := h.send("session/cancel", nil, fmt.Sprintf(`{"sessionId":%q}`, newResult.SessionID)); err != nil {
		t.Fatal(err)
	}

	gotModeReply := false
	for {
		msg := h.recv(t)
		if msg.ID != nil && !gotModeReply && fmt.Sprint(msg.ID) == "4" && msg.Error == nil {
			gotModeReply = true
			continue
		}
		if msg.Method == "session/update" {
			var params SessionUpdate
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				t.Fatalf("session/update params: %v", err)
			}
			if params.Update.SessionUpdate == "stop_reason" {
				if params.Update.StopReason != "cancelled" {
					t.Fatalf("stopReason = %q, want cancelled", params.Update.StopReason)
				}
				if !gotModeReply {
					t.Fatal("cancel landed but the reader never answered session/set_mode while the prompt ran")
				}
				return
			}
		}
	}
}

func TestServer_emptyPromptRejected(t *testing.T) {
	h := newACPHarness(t, &fakeSession{})
	if err := h.send("session/new", 1, ""); err != nil {
		t.Fatal(err)
	}
	created := h.recv(t)
	var newResult SessionNewResult
	if err := json.Unmarshal(created.Result, &newResult); err != nil {
		t.Fatalf("session/new result: %v", err)
	}
	params := fmt.Sprintf(`{"sessionId":%q,"prompt":[]}`, newResult.SessionID)
	if err := h.send("session/prompt", 5, params); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != -32602 {
		t.Fatalf("response = %+v, want -32602 empty prompt", msg)
	}
}
