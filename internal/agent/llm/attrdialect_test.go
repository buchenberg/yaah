package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// The helpers below assemble dialect fixtures from fragments so the test
// source itself never contains a complete literal markup tag (which would
// trip tooling that watches for leaked dialect content). They mirror the
// grammar documented in attrdialect.go.

func trOpen() string { return "<" + "tool_response>" }

func trClose() string { return "</" + "tool_response>" }

func fnOpen(name string) string { return "<" + "function=" + name + ">" }

func fnClose() string { return "</" + "function>" }

func paramOpen(name string) string { return "<" + "parameter=" + name + ">" }

func paramClose() string { return "</" + "parameter>" }

// issue314Fixture is the exact truncated dialect leak from GitHub issue
// #314 (counter role, prism-ml/bonsai-27b via LM Studio, max_turns: 1):
// a wrapped attribute-style bash call whose function and wrapper closing
// tags never arrived.
var issue314Fixture = strings.Join([]string{
	trOpen(),
	fnOpen("bash"),
	paramOpen("command"),
	`find .../internal -name "*.go" ...`,
	paramClose(),
}, "\n")

func attrArgs(t *testing.T, call types.ToolCall) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("invalid arguments JSON %q: %v", call.Function.Arguments, err)
	}
	return args
}

func TestParseAttrToolCalls(t *testing.T) {
	t.Run("no dialect", func(t *testing.T) {
		content := "Here is the result of your request."
		cleaned, calls, ok := parseAttrToolCalls(content, new(int))
		if ok {
			t.Fatal("expected no attribute dialect detected")
		}
		if cleaned != content {
			t.Fatalf("content modified: %q", cleaned)
		}
		if len(calls) != 0 {
			t.Fatalf("expected 0 calls, got %d", len(calls))
		}
	})

	t.Run("issue 314 fixture", func(t *testing.T) {
		cleaned, calls, ok := parseAttrToolCalls(issue314Fixture, new(int))
		if !ok {
			t.Fatal("expected attribute dialect detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned content, got %q", cleaned)
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Function.Name != "bash" {
			t.Fatalf("expected bash, got %q", calls[0].Function.Name)
		}
		if calls[0].Type != "function" {
			t.Fatalf("expected function type, got %q", calls[0].Type)
		}
		if calls[0].ID != "attr_0" {
			t.Fatalf("expected id attr_0, got %q", calls[0].ID)
		}
		args := attrArgs(t, calls[0])
		want := `find .../internal -name "*.go" ...`
		if args["command"] != want {
			t.Fatalf("expected command=%q, got %v", want, args["command"])
		}
	})

	t.Run("complete wrapped call", func(t *testing.T) {
		content := strings.Join([]string{
			trOpen(),
			fnOpen("read"),
			paramOpen("filePath") + "main.go" + paramClose(),
			fnClose(),
			trClose(),
		}, "\n")
		cleaned, calls, ok := parseAttrToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 1 || calls[0].Function.Name != "read" {
			t.Fatalf("expected 1 read call, got %+v", calls)
		}
		if got := attrArgs(t, calls[0])["filePath"]; got != "main.go" {
			t.Fatalf("expected filePath=main.go, got %v", got)
		}
	})

	t.Run("unwrapped form", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("git"),
			paramOpen("action") + "status" + paramClose(),
			fnClose(),
		}, "\n")
		cleaned, calls, ok := parseAttrToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialect detected without wrapper")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 1 || calls[0].Function.Name != "git" {
			t.Fatalf("expected 1 git call, got %+v", calls)
		}
		if got := attrArgs(t, calls[0])["action"]; got != "status" {
			t.Fatalf("expected action=status, got %v", got)
		}
	})

	t.Run("multiple calls in one message", func(t *testing.T) {
		content := strings.Join([]string{
			trOpen(),
			fnOpen("grep"),
			paramOpen("pattern") + "TODO" + paramClose(),
			fnClose(),
			trClose(),
			trOpen(),
			fnOpen("read"),
			paramOpen("filePath") + "go.mod" + paramClose(),
			fnClose(),
			trClose(),
		}, "\n")
		cleaned, calls, ok := parseAttrToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 2 {
			t.Fatalf("expected 2 calls, got %d", len(calls))
		}
		if calls[0].Function.Name != "grep" || calls[1].Function.Name != "read" {
			t.Fatalf("expected grep then read, got %q and %q", calls[0].Function.Name, calls[1].Function.Name)
		}
		if calls[0].ID != "attr_0" || calls[1].ID != "attr_1" {
			t.Fatalf("expected ids attr_0/attr_1, got %q and %q", calls[0].ID, calls[1].ID)
		}
		if calls[1].Index != 1 {
			t.Fatalf("expected second call index 1, got %d", calls[1].Index)
		}
	})

	t.Run("multi-line parameter value", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("write"),
			paramOpen("content"),
			"line one",
			"line two",
			"line three",
			paramClose(),
			fnClose(),
		}, "\n")
		_, calls, ok := parseAttrToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		want := "line one\nline two\nline three"
		if got := attrArgs(t, calls[0])["content"]; got != want {
			t.Fatalf("expected content=%q, got %q", want, got)
		}
	})

	t.Run("prose around call is preserved", func(t *testing.T) {
		content := strings.Join([]string{
			"Let me search for that.",
			fnOpen("grep"),
			paramOpen("pattern") + "TODO" + paramClose(),
			fnClose(),
			"I will report back.",
		}, "\n")
		cleaned, calls, ok := parseAttrToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if !strings.Contains(cleaned, "Let me search for that.") || !strings.Contains(cleaned, "I will report back.") {
			t.Fatalf("prose lost in cleaned content: %q", cleaned)
		}
		if strings.Contains(cleaned, "function=") {
			t.Fatalf("markup left in cleaned content: %q", cleaned)
		}
	})

	t.Run("JSON object parameter embedded verbatim", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("spawn"),
			paramOpen("config") + `{"role": "counter", "max_turns": 1}` + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, ok := parseAttrToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		cfg, isObj := attrArgs(t, calls[0])["config"].(map[string]any)
		if !isObj {
			t.Fatalf("expected config embedded as JSON object, got %v", attrArgs(t, calls[0])["config"])
		}
		if cfg["role"] != "counter" {
			t.Fatalf("expected role=counter, got %v", cfg["role"])
		}
	})

	t.Run("scalar parameter stays a string", func(t *testing.T) {
		// The dialect carries no type markers, so only embedded JSON
		// objects/arrays are structural; everything else stays a string.
		content := strings.Join([]string{
			fnOpen("bash"),
			paramOpen("timeout") + "15" + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, ok := parseAttrToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if got := attrArgs(t, calls[0])["timeout"]; got != "15" {
			t.Fatalf("expected string \"15\", got %v (%T)", got, got)
		}
	})
}

func TestRecoverDialectToolCalls(t *testing.T) {
	t.Run("attribute dialect recovered at shared seam", func(t *testing.T) {
		cleaned, calls, ok := recoverDialectToolCalls(issue314Fixture, new(int))
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 1 || calls[0].Function.Name != "bash" {
			t.Fatalf("expected 1 bash call, got %+v", calls)
		}
	})

	t.Run("DSML still recovered", func(t *testing.T) {
		content := "<\uff5c\uff5cDSML\uff5c\uff5ctool_calls>\n" +
			"<\uff5c\uff5cDSML\uff5c\uff5cinvoke name=\"read\">\n" +
			"<\uff5c\uff5cDSML\uff5c\uff5cparameter name=\"filePath\" string=\"true\">main.go</\uff5c\uff5cDSML\uff5c\uff5cparameter>\n" +
			"</\uff5c\uff5cDSML\uff5c\uff5cinvoke>\n" +
			"</\uff5c\uff5cDSML\uff5c\uff5ctool_calls>"
		cleaned, calls, ok := recoverDialectToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected DSML detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 1 || calls[0].Function.Name != "read" {
			t.Fatalf("expected 1 read call, got %+v", calls)
		}
		if calls[0].ID != "dsml_0" {
			t.Fatalf("expected dsml-prefixed id, got %q", calls[0].ID)
		}
	})

	t.Run("both dialects share one counter", func(t *testing.T) {
		content := strings.Join([]string{
			"<\uff5c\uff5cDSML\uff5c\uff5ctool_calls>",
			"<\uff5c\uff5cDSML\uff5c\uff5cinvoke name=\"read\">",
			"<\uff5c\uff5cDSML\uff5c\uff5cparameter name=\"filePath\" string=\"true\">main.go</\uff5c\uff5cDSML\uff5c\uff5cparameter>",
			"</\uff5c\uff5cDSML\uff5c\uff5cinvoke>",
			"</\uff5c\uff5cDSML\uff5c\uff5ctool_calls>",
			fnOpen("grep"),
			paramOpen("pattern") + "TODO" + paramClose(),
			fnClose(),
		}, "\n")
		cleaned, calls, ok := recoverDialectToolCalls(content, new(int))
		if !ok {
			t.Fatal("expected dialects detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 2 {
			t.Fatalf("expected 2 calls, got %d", len(calls))
		}
		if calls[0].ID != "dsml_0" || calls[1].ID != "attr_1" {
			t.Fatalf("expected ids dsml_0/attr_1 from the shared counter, got %q and %q", calls[0].ID, calls[1].ID)
		}
	})
}

func TestCheckTruncatedStream_AttrDialectRecovery(t *testing.T) {
	msg, finish, _, _, err := checkTruncatedStream(issue314Fixture, nil, "stop", "model-x", "", types.Usage{}, new(int))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finish != "stop" {
		t.Fatalf("expected finish stop, got %q", finish)
	}
	if msg.Content != "" {
		t.Fatalf("expected markup stripped from content, got %q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 recovered tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("expected bash, got %q", msg.ToolCalls[0].Function.Name)
	}
	if got := attrArgs(t, msg.ToolCalls[0])["command"]; !strings.Contains(got.(string), "find") {
		t.Fatalf("expected find command in args, got %v", got)
	}
}

// attrDialectProvider answers with the issue #314 fixture as plain
// content, forcing the non-streaming recovery seam in Call.
type attrDialectProvider struct{}

func (attrDialectProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: issue314Fixture},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}, nil
}

func TestCall_AttrDialectRecovery(t *testing.T) {
	c := &Client{
		Provider:   attrDialectProvider{},
		Model:      "test-model",
		MaxRetries: 1,
	}
	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if res.Message.Content != "" {
		t.Fatalf("expected markup stripped from content, got %q", res.Message.Content)
	}
	if len(res.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 recovered tool call, got %d", len(res.Message.ToolCalls))
	}
	if res.Message.ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("expected bash, got %q", res.Message.ToolCalls[0].Function.Name)
	}
	if res.Message.ToolCalls[0].ID != "attr_0" {
		t.Fatalf("expected id attr_0, got %q", res.Message.ToolCalls[0].ID)
	}
}
