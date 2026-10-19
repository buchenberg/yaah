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

func tcOpen() string { return "<" + "tool_call>" }

func trOpen() string { return "<" + "tool_response>" }

func trClose() string { return "</" + "tool_response>" }

func fnOpen(name string) string { return "<" + "function=" + name + ">" }

func fnClose() string { return "</" + "function>" }

func paramOpen(name string) string { return "<" + "parameter=" + name + ">" }

func paramClose() string { return "</" + "parameter>" }

// issue314Fixture is the exact truncated dialect leak from GitHub issue
// #314 (counter role, prism-ml/bonsai-27b via LM Studio, max_turns: 1):
// an attribute-style bash call in a tool_call wrapper whose function and
// wrapper closing tags never arrived.
var issue314Fixture = strings.Join([]string{
	tcOpen(),
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

// attrToolDefs builds the offered-tool schemas used by the typed-recovery
// tests: a write tool whose content is a required string, and a bash tool
// with a required string command and an integer timeout.
func attrToolDefs() []types.ToolDef {
	return []types.ToolDef{
		{Type: "function", Function: types.ToolFn{
			Name:       "write",
			Parameters: json.RawMessage(`{"type":"object","properties":{"filePath":{"type":"string"},"content":{"type":"string"}},"required":["filePath","content"]}`),
		}},
		{Type: "function", Function: types.ToolFn{
			Name:       "bash",
			Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"integer"}},"required":["command"]}`),
		}},
	}
}

// truncatedWriteFixture is the data-loss case from the #333 review: a
// write call whose filePath arrived complete but whose content parameter
// was cut off mid-value. Recovering it with only filePath would let
// WriteTool default content to "" and erase the file.
var truncatedWriteFixture = strings.Join([]string{
	fnOpen("write"),
	paramOpen("filePath") + "main.go" + paramClose(),
	paramOpen("content"),
	"package main — and then the stream died",
}, "\n")

func TestParseAttrToolCalls(t *testing.T) {
	t.Run("no dialect", func(t *testing.T) {
		content := "Here is the result of your request."
		cleaned, calls, rejected, ok := parseAttrToolCalls(content, new(int), nil)
		if ok {
			t.Fatal("expected no attribute dialect detected")
		}
		if cleaned != content {
			t.Fatalf("content modified: %q", cleaned)
		}
		if len(calls) != 0 || len(rejected) != 0 {
			t.Fatalf("expected 0 calls and 0 rejections, got %d and %d", len(calls), len(rejected))
		}
	})

	t.Run("issue 314 fixture", func(t *testing.T) {
		cleaned, calls, rejected, ok := parseAttrToolCalls(issue314Fixture, new(int), nil)
		if !ok {
			t.Fatal("expected attribute dialect detected")
		}
		if len(rejected) != 0 {
			t.Fatalf("expected no rejections, got %v", rejected)
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

	t.Run("tool_response wrapper still recovered", func(t *testing.T) {
		content := strings.Join([]string{
			trOpen(),
			fnOpen("read"),
			paramOpen("filePath") + "main.go" + paramClose(),
			fnClose(),
			trClose(),
		}, "\n")
		cleaned, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
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
		cleaned, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
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
			tcOpen(),
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
		cleaned, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
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
		_, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
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
		cleaned, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
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
		_, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
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

	t.Run("scalar parameter stays a string without a schema", func(t *testing.T) {
		// The dialect carries no type markers, so without a schema only
		// embedded JSON objects/arrays are structural; everything else
		// stays a string.
		content := strings.Join([]string{
			fnOpen("bash"),
			paramOpen("timeout") + "15" + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if got := attrArgs(t, calls[0])["timeout"]; got != "15" {
			t.Fatalf("expected string \"15\", got %v (%T)", got, got)
		}
	})

	// — Fenced and quoted examples are prose, never calls (#333 review). —

	t.Run("fenced example is not recovered", func(t *testing.T) {
		content := strings.Join([]string{
			"The model leaked this markup:",
			"",
			"```text",
			fnOpen("bash"),
			paramOpen("command") + "rm -rf /" + paramClose(),
			fnClose(),
			"```",
			"",
			"Nothing above was executed.",
		}, "\n")
		cleaned, calls, rejected, ok := parseAttrToolCalls(content, new(int), attrToolDefs())
		if ok {
			t.Fatalf("fenced example must not be recovered, got calls %+v rejected %v", calls, rejected)
		}
		if cleaned != content {
			t.Fatalf("content modified: %q", cleaned)
		}
	})

	t.Run("unterminated fence protects to end", func(t *testing.T) {
		content := strings.Join([]string{
			"Example below:",
			"```",
			fnOpen("bash"),
			paramOpen("command") + "echo hi" + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
		if ok || len(calls) != 0 {
			t.Fatalf("markup inside an unterminated fence must stay prose, got %+v", calls)
		}
	})

	t.Run("blockquoted example is not recovered", func(t *testing.T) {
		content := strings.Join([]string{
			"The trace showed:",
			"> " + fnOpen("bash"),
			"> " + paramOpen("command") + "rm -rf /" + paramClose(),
			"> " + fnClose(),
		}, "\n")
		_, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
		if ok || len(calls) != 0 {
			t.Fatalf("quoted example must stay prose, got %+v", calls)
		}
	})

	t.Run("mid-line mention is not recovered", func(t *testing.T) {
		content := "A degraded model emits " + fnOpen("bash") + " inline instead of a structured call."
		_, calls, _, ok := parseAttrToolCalls(content, new(int), nil)
		if ok || len(calls) != 0 {
			t.Fatalf("mid-line prose mention must stay prose, got %+v", calls)
		}
	})

	// — Unsafe synthesis is rejected, never dispatched (#333 review). —

	t.Run("truncated parameter is rejected", func(t *testing.T) {
		cleaned, calls, rejected, ok := parseAttrToolCalls(truncatedWriteFixture, new(int), attrToolDefs())
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if len(calls) != 0 {
			t.Fatalf("truncated write must not be synthesized, got %+v", calls)
		}
		if len(rejected) != 1 || !strings.Contains(rejected[0], "mid-parameter") {
			t.Fatalf("expected a mid-parameter rejection, got %v", rejected)
		}
		// The markup stays in the content so the report guard sees it.
		if !strings.Contains(cleaned, "parameter=content") {
			t.Fatalf("rejected markup must remain in cleaned content, got %q", cleaned)
		}
	})

	t.Run("complete block with truncated parameter is rejected", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("write"),
			paramOpen("filePath") + "main.go" + paramClose(),
			paramOpen("content") + "half a file",
			fnClose(),
		}, "\n")
		_, calls, rejected, ok := parseAttrToolCalls(content, new(int), attrToolDefs())
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if len(calls) != 0 || len(rejected) != 1 {
			t.Fatalf("expected rejection not synthesis, got calls %+v rejected %v", calls, rejected)
		}
	})

	t.Run("missing required argument is rejected", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("write"),
			paramOpen("filePath") + "main.go" + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, rejected, ok := parseAttrToolCalls(content, new(int), attrToolDefs())
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if len(calls) != 0 {
			t.Fatalf("write without required content must not be synthesized, got %+v", calls)
		}
		if len(rejected) != 1 || !strings.Contains(rejected[0], `missing required argument "content"`) {
			t.Fatalf("expected a missing-required rejection, got %v", rejected)
		}
	})

	t.Run("unknown tool skips required-argument validation", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("mystery"),
			paramOpen("x") + "1" + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, rejected, ok := parseAttrToolCalls(content, new(int), attrToolDefs())
		if !ok || len(calls) != 1 || len(rejected) != 0 {
			t.Fatalf("unknown tool should synthesize (dispatch fails explicitly), got calls %+v rejected %v", calls, rejected)
		}
	})

	// — Schema-typed recovery (#333 review). —

	t.Run("declared string stays a string even when it looks like JSON", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("write"),
			paramOpen("filePath") + "config.json" + paramClose(),
			paramOpen("content") + `{"enabled": true}` + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, rejected, ok := parseAttrToolCalls(content, new(int), attrToolDefs())
		if !ok || len(rejected) != 0 {
			t.Fatalf("expected clean recovery, got rejected %v", rejected)
		}
		got := attrArgs(t, calls[0])["content"]
		if s, isStr := got.(string); !isStr || s != `{"enabled": true}` {
			t.Fatalf("write content must stay a string per its schema, got %v (%T)", got, got)
		}
	})

	t.Run("declared integer is decoded as a number", func(t *testing.T) {
		content := strings.Join([]string{
			fnOpen("bash"),
			paramOpen("command") + "go test ./..." + paramClose(),
			paramOpen("timeout") + "15" + paramClose(),
			fnClose(),
		}, "\n")
		_, calls, _, ok := parseAttrToolCalls(content, new(int), attrToolDefs())
		if !ok || len(calls) != 1 {
			t.Fatalf("expected 1 call, got %+v", calls)
		}
		got := attrArgs(t, calls[0])["timeout"]
		if n, isNum := got.(float64); !isNum || n != 15 {
			t.Fatalf("bash timeout must decode to a number per its schema, got %v (%T)", got, got)
		}
	})
}

func TestRecoverDialectToolCalls(t *testing.T) {
	t.Run("attribute dialect recovered at shared seam", func(t *testing.T) {
		cleaned, calls, rejected, ok := recoverDialectToolCalls(issue314Fixture, new(int), nil)
		if !ok || len(rejected) != 0 {
			t.Fatalf("expected clean dialect detection, got ok=%v rejected=%v", ok, rejected)
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 1 || calls[0].Function.Name != "bash" {
			t.Fatalf("expected 1 bash call, got %+v", calls)
		}
	})

	t.Run("rejections surface at the seam", func(t *testing.T) {
		_, calls, rejected, ok := recoverDialectToolCalls(truncatedWriteFixture, new(int), attrToolDefs())
		if !ok {
			t.Fatal("expected dialect detected")
		}
		if len(calls) != 0 || len(rejected) != 1 {
			t.Fatalf("expected 0 calls and 1 rejection, got %+v and %v", calls, rejected)
		}
	})

	t.Run("DSML still recovered", func(t *testing.T) {
		content := "<\uff5c\uff5cDSML\uff5c\uff5ctool_calls>\n" +
			"<\uff5c\uff5cDSML\uff5c\uff5cinvoke name=\"read\">\n" +
			"<\uff5c\uff5cDSML\uff5c\uff5cparameter name=\"filePath\" string=\"true\">main.go</\uff5c\uff5cDSML\uff5c\uff5cparameter>\n" +
			"</\uff5c\uff5cDSML\uff5c\uff5cinvoke>\n" +
			"</\uff5c\uff5cDSML\uff5c\uff5ctool_calls>"
		cleaned, calls, _, ok := recoverDialectToolCalls(content, new(int), nil)
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
		cleaned, calls, _, ok := recoverDialectToolCalls(content, new(int), nil)
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
	msg, finish, _, _, err := checkTruncatedStream(issue314Fixture, nil, "stop", "model-x", "", "", types.Usage{}, new(int), nil)
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

// The truncated-write rejection must fail the stream seam explicitly
// instead of dispatching the partial call (#333 review).
func TestCheckTruncatedStream_RejectsPartialRecovery(t *testing.T) {
	_, _, _, _, err := checkTruncatedStream(truncatedWriteFixture, nil, "stop", "model-x", "", "", types.Usage{}, new(int), attrToolDefs())
	if err == nil {
		t.Fatal("expected an explicit error for markup cut mid-parameter")
	}
	if !strings.Contains(err.Error(), "could not be safely recovered") {
		t.Fatalf("error should explain the refused recovery, got: %v", err)
	}
}

// A fenced example must survive the stream seam as plain content: no
// recovered calls, no error (#333 review).
func TestCheckTruncatedStream_FencedExampleNotRecovered(t *testing.T) {
	content := strings.Join([]string{
		"The leak looked like this:",
		"```text",
		fnOpen("bash"),
		paramOpen("command") + "echo hi" + paramClose(),
		fnClose(),
		"```",
		"It never executed.",
	}, "\n")
	msg, _, _, _, err := checkTruncatedStream(content, nil, "stop", "model-x", "", "", types.Usage{}, new(int), attrToolDefs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("fenced example must not become a call, got %+v", msg.ToolCalls)
	}
	if !strings.Contains(msg.Content, "echo hi") {
		t.Fatalf("fenced example must remain in content, got %q", msg.Content)
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

// staticContentProvider answers every request with fixed content; used to
// drive the non-streaming seam with the unsafe-recovery fixtures.
type staticContentProvider struct{ content string }

func (p staticContentProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: p.content},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}, nil
}

// The truncated-write rejection must also fail the non-streaming seam
// explicitly (#333 review).
func TestCall_RejectsPartialRecovery(t *testing.T) {
	c := &Client{
		Provider:   staticContentProvider{content: truncatedWriteFixture},
		Model:      "test-model",
		MaxRetries: 1,
	}
	_, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model", Tools: attrToolDefs()})
	if err == nil {
		t.Fatal("expected an explicit error for markup cut mid-parameter")
	}
	if !strings.Contains(err.Error(), "could not be safely recovered") {
		t.Fatalf("error should explain the refused recovery, got: %v", err)
	}
}

// A fenced example must survive the non-streaming seam as plain content
// (#333 review).
func TestCall_FencedExampleNotRecovered(t *testing.T) {
	content := strings.Join([]string{
		"The leak looked like this:",
		"```text",
		fnOpen("bash"),
		paramOpen("command") + "echo hi" + paramClose(),
		fnClose(),
		"```",
		"It never executed.",
	}, "\n")
	c := &Client{
		Provider:   staticContentProvider{content: content},
		Model:      "test-model",
		MaxRetries: 1,
	}
	res, err := c.Call(context.Background(), types.ChatRequest{Model: "test-model", Tools: attrToolDefs()})
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if len(res.Message.ToolCalls) != 0 {
		t.Fatalf("fenced example must not become a call, got %+v", res.Message.ToolCalls)
	}
	if !strings.Contains(res.Message.Content, "echo hi") {
		t.Fatalf("fenced example must remain in content, got %q", res.Message.Content)
	}
}
