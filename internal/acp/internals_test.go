package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/todo"
)

func TestTruncate_RuneSafeBoundaries(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "h..."},
		{"hello", 3, "hel"},
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"héllo", 4, "h..."},
		{"héllo", 2, "hé"},
		{"", 5, ""},
	}
	for _, tc := range tests {
		if got := truncate(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestCompactHintAndTextContent(t *testing.T) {
	if got := compactHint("  one\n two\tthree  "); got != "one two three" {
		t.Errorf("compactHint = %q", got)
	}
	got := textContent("hi")
	if got.Type != "text" || got.Text != "hi" {
		t.Errorf("textContent = %+v", got)
	}
}

func TestRawOf(t *testing.T) {
	raw := json.RawMessage(`{"a":1}`)
	if got := rawOf(raw); string(got) != string(raw) {
		t.Errorf("rawOf(raw) = %s", got)
	}
	if got := rawOf(map[string]int{"a": 1}); string(got) != `{"a":1}` {
		t.Errorf("rawOf(map) = %s", got)
	}
}

func TestToolKind(t *testing.T) {
	tests := map[string]string{
		"read": KindRead, "grep": KindRead, "glob": KindRead, "ls": KindRead, "memory": KindRead, "prompt": KindRead,
		"write": KindEdit, "edit": KindEdit, "replace": KindEdit,
		"delete": KindDelete,
		"bash":   KindExecute, "powershell": KindExecute, "git": KindExecute, "process": KindExecute, "background": KindExecute,
		"webfetch": KindFetch, "http": KindFetch, "web_search": KindFetch, "fetch": KindFetch,
		"question": KindThink, "think": KindThink, "todo": KindEdit, "todowrite": KindEdit, "plan": KindEdit,
		"unknown": KindOther,
	}
	for name, want := range tests {
		if got := toolKind(name); got != want {
			t.Errorf("toolKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPermissionOptionsAndTitle(t *testing.T) {
	opts := permissionOptions("bash")
	if len(opts) != 4 {
		t.Fatalf("options = %+v, want four", opts)
	}
	wantKinds := []string{PermAllowOnce, PermAllowAlways, PermRejectOnce, PermRejectAlways}
	for i, opt := range opts {
		if opt.OptionID == "" || opt.Name == "" {
			t.Errorf("option %d = %+v, want id/name", i, opt)
		}
		if opt.Kind != wantKinds[i] {
			t.Errorf("option %d kind = %q, want %q", i, opt.Kind, wantKinds[i])
		}
	}
	if got := permissionTitle("bash", "  echo   hi  "); got != "bash: echo hi" {
		t.Errorf("permissionTitle = %q", got)
	}
	if got := permissionTitle("bash", " \n\t "); got != "bash" {
		t.Errorf("empty permissionTitle = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := permissionTitle("bash", long); len([]rune(got)) > len("bash: ")+60 {
		t.Errorf("long permissionTitle length = %d", len([]rune(got)))
	}
}

func TestRememberPermission(t *testing.T) {
	s := NewServer(nil, "test", nil, nil)
	s.sessions["s"] = &sessionState{id: "s"}
	s.rememberPermission("s", "bash", PermAllowAlways)
	s.rememberPermission("s", "write", PermRejectAlways)
	st := s.sessions["s"]
	if !st.permAllow["bash"] {
		t.Error("allow_always was not remembered")
	}
	if !st.permDeny["write"] {
		t.Error("reject_always was not remembered")
	}
	s.rememberPermission("missing", "bash", PermAllowAlways)
}

func TestProtocolRoundTrips(t *testing.T) {
	t.Run("content blocks", func(t *testing.T) {
		in := ContentBlock{Type: "resource_link", URI: "file:///x/y.ts", Name: "y.ts"}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out ContentBlock
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.Type != in.Type || out.URI != in.URI || out.Name != in.Name {
			t.Errorf("round trip = %+v, want %+v", out, in)
		}
		embedded := ContentBlock{Type: "resource", Resource: &ResourceBundle{URI: "file:///x", MimeType: "text/plain", Text: "hi"}}
		raw, _ = json.Marshal(embedded)
		if !strings.Contains(string(raw), `"resource":{"uri":"file:///x"`) {
			t.Errorf("embedded resource JSON = %s", raw)
		}
	})
	t.Run("session results", func(t *testing.T) {
		in := SessionNewResult{
			SessionID: "s",
			Modes:     sessionModeState(SessionModePlan),
			ConfigOptions: []SessionConfigOption{{
				ID: "model", Name: "Model", Type: ConfigOptionTypeSelect, CurrentValue: "p/m",
				Options: []SessionConfigSelectOption{{Value: "p/m", Name: "m"}},
			}},
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out SessionNewResult
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.SessionID != "s" || out.Modes == nil || out.Modes.CurrentModeID != SessionModePlan {
			t.Errorf("session result round trip = %+v", out)
		}
		if len(out.ConfigOptions) != 1 || out.ConfigOptions[0].CurrentValue != "p/m" {
			t.Errorf("config options = %+v", out.ConfigOptions)
		}
	})
	t.Run("prompt result and mode update", func(t *testing.T) {
		for _, in := range []any{
			PromptResult{StopReason: StopMaxTurnRequests},
			CurrentModeUpdate{SessionUpdate: "current_mode_update", CurrentModeID: SessionModePlan},
			SessionResumeResult{Modes: sessionModeState(SessionModeBuild)},
		} {
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) == 0 {
				t.Errorf("empty JSON for %T", in)
			}
		}
	})
	t.Run("permission result", func(t *testing.T) {
		var out RequestPermissionResult
		if err := json.Unmarshal([]byte(`{"outcome":{"outcome":"selected","optionId":"allow-always"}}`), &out); err != nil {
			t.Fatal(err)
		}
		if out.Outcome.Outcome != "selected" || out.Outcome.OptionID != "allow-always" {
			t.Errorf("permission result = %+v", out)
		}
	})
}

func TestForwardOne_TranslatesControlMessages(t *testing.T) {
	var got []capturedUpdate
	s := NewServer(nil, "test", nil, nil)
	send := func(sessionID string, update any) { got = append(got, capturedUpdate{sessionID, update}) }
	ch := make(chan control.Msg, 1)

	if !forwardOne(&control.Status{Text: "working"}, "s", send, s, ch) {
		t.Fatal("status should continue")
	}
	if !forwardOne(&control.Error{Err: errors.New("boom")}, "s", send, s, ch) {
		t.Fatal("error should continue")
	}
	if !forwardOne(&control.Fallback{Provider: "p", Model: "m"}, "s", send, s, ch) {
		t.Fatal("fallback should continue")
	}
	if !forwardOne(&control.ContextInfo{Tokens: 10, Window: 100}, "s", send, s, ch) {
		t.Fatal("context info should continue")
	}
	if !forwardOne(&control.Todos{Items: []todo.Item{{Content: "step", Status: "cancelled", Priority: "high"}}}, "s", send, s, ch) {
		t.Fatal("todos should continue")
	}
	answer := make(chan bool, 1)
	if !forwardOne(&control.Continue{MaxIter: 3, AnswerCh: answer}, "s", send, s, ch) {
		t.Fatal("continue should continue")
	}
	if !<-answer {
		t.Error("auto-continue did not answer true")
	}
	if len(got) != 6 {
		t.Fatalf("updates = %d, want 6", len(got))
	}
	if chunk, ok := got[0].update.(ChunkUpdate); !ok || chunk.Content.Text != "working" {
		t.Errorf("status update = %+v", got[0].update)
	}
	if usage, ok := got[3].update.(UsageUpdate); !ok || usage.Used != 10 || usage.Size != 100 {
		t.Errorf("usage update = %+v", got[3].update)
	}
	if plan, ok := got[4].update.(PlanUpdate); !ok || len(plan.Entries) != 1 || plan.Entries[0].Status != PlanPending {
		t.Errorf("plan update = %+v", got[4].update)
	}
	if forwardOne(&control.Done{}, "s", send, s, ch) {
		t.Error("done should stop the forwarder")
	}
}

func TestForwardCtrl_FlushDrainsQueue(t *testing.T) {
	s := NewServer(nil, "test", nil, nil)
	var got []capturedUpdate
	send := func(sessionID string, update any) { got = append(got, capturedUpdate{sessionID, update}) }
	ch := make(chan control.Msg, 3)
	ch <- &control.Status{Text: "one"}
	ch <- &control.Status{Text: "two"}
	flush := make(chan struct{})
	done := make(chan struct{})
	go s.forwardCtrl(context.Background(), ch, "s", send, flush, done)
	close(flush)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forwardCtrl did not finish after flush")
	}
	if len(got) != 2 {
		t.Fatalf("flushed updates = %d, want 2", len(got))
	}
}

func TestForwardCtrl_CancelStops(t *testing.T) {
	s := NewServer(nil, "test", nil, nil)
	ch := make(chan control.Msg)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go s.forwardCtrl(ctx, ch, "s", func(string, any) {}, make(chan struct{}), done)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forwardCtrl did not stop on cancel")
	}
}

type capturedUpdate struct {
	sessionID string
	update    any
}
