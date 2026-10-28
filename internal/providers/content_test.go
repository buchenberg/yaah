package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// The fixtures below are the real wire shapes captured from zai-glm-5-3
// (bead yaah-050b): reasoning arrives as an ARRAY of typed blocks inside
// delta.content instead of a plain string.
func TestStreamDelta_contentShapes(t *testing.T) {
	cases := []struct {
		name          string
		line          string
		wantContent   string
		wantReasoning string
	}{
		{
			name:        "plain string",
			line:        `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			wantContent: "Hello",
		},
		{
			name:        "array of text blocks",
			line:        `data: {"id":"c1","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"Hi "},{"type":"text","text":"there"}]}}]}`,
			wantContent: "Hi there",
		},
		{
			name:          "array of thinking blocks",
			line:          `data: {"id":"c1","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"The"}],"closed":true}]},"finish_reason":null}]}`,
			wantReasoning: "The",
		},
		{
			name:          "mixed thinking and text",
			line:          `data: {"id":"c1","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"think "}]},{"type":"text","text":"answer"}]}}]}`,
			wantContent:   "answer",
			wantReasoning: "think ",
		},
		{
			name:          "reasoning-named blocks stay out of visible content",
			line:          `data: {"id":"c1","choices":[{"index":0,"delta":{"content":[{"type":"analysis","text":"hidden"},{"type":"text","text":"x"}]}}]}`,
			wantContent:   "x",
			wantReasoning: "hidden",
		},
		{
			name:        "unknown text-bearing block recovered into visible",
			line:        `data: {"id":"c1","choices":[{"index":0,"delta":{"content":[{"type":"citation","text":"cited"},{"type":"text","text":"x"}]}}]}`,
			wantContent: "citedx",
		},
		{
			name:          "string reasoning_content alongside string content",
			line:          `data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"because","content":"ok"}}]}`,
			wantContent:   "ok",
			wantReasoning: "because",
		},
		{
			name: "array content with tool_calls",
			line: `data: {"id":"c1","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"hmm"}]}],` +
				`"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
			wantReasoning: "hmm",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunk, done, err := parseSSEChunk([]byte(tc.line))
			if err != nil {
				t.Fatalf("parseSSEChunk() error: %v", err)
			}
			if done || chunk == nil || len(chunk.Choices) != 1 {
				t.Fatalf("parseSSEChunk() = %v, done=%v", chunk, done)
			}
			delta := chunk.Choices[0].Delta
			if delta.Content != tc.wantContent {
				t.Errorf("content = %q, want %q", delta.Content, tc.wantContent)
			}
			if delta.ReasoningContent != tc.wantReasoning {
				t.Errorf("reasoning = %q, want %q", delta.ReasoningContent, tc.wantReasoning)
			}
		})
	}
}

// Tool calls must survive the array-content path: the normalization must not
// swallow the rest of the delta.
func TestStreamDelta_arrayContentKeepsToolCalls(t *testing.T) {
	line := []byte(`data: {"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"reading"}],` +
		`"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}}]}`)

	chunk, _, err := parseSSEChunk(line)
	if err != nil {
		t.Fatalf("parseSSEChunk() error: %v", err)
	}
	d := chunk.Choices[0].Delta
	if d.Content != "reading" {
		t.Errorf("content = %q, want %q", d.Content, "reading")
	}
	if len(d.ToolCalls) != 1 || d.ToolCalls[0].Function.Name != "read" {
		t.Fatalf("tool calls lost: %+v", d.ToolCalls)
	}
}

// An unrecognized content shape must be an error so SendStream can warn once
// per turn; dropping it silently is what made yaah-050b invisible.
// An unrecognized content shape must not be silent (yaah-050b) and must not
// cost the delta its tool calls: the delta degrades to empty content, records
// the shape for SendStream's once-per-stream warning, and keeps its tool calls
// (review 616).
func TestStreamDelta_unknownContentShapeDegrades(t *testing.T) {
	var delta StreamDelta
	if err := json.Unmarshal([]byte(`{"content":{"type":"text","text":"hi"},"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}`), &delta); err != nil {
		t.Fatalf("object-shaped content must degrade, got %v", err)
	}
	if delta.Content != "" || delta.ReasoningContent != "" {
		t.Errorf("content = %q, reasoning = %q, want both empty", delta.Content, delta.ReasoningContent)
	}
	if len(delta.ToolCalls) != 1 {
		t.Fatalf("tool calls lost: %+v", delta.ToolCalls)
	}
	if delta.contentShapeErr == nil {
		t.Error("shape error must be recorded for the once-per-stream warning")
	}

	var clean StreamDelta
	if err := json.Unmarshal([]byte(`{"content":[]}`), &clean); err != nil {
		t.Fatalf("empty block array must decode, got %v", err)
	}
	if clean.contentShapeErr != nil {
		t.Error("clean shapes must not record a shape error")
	}
	if err := json.Unmarshal([]byte(`{"content":null}`), &clean); err != nil {
		t.Fatalf("null content must decode, got %v", err)
	}
}

// reasoning_content is normally a plain string, but an endpoint that sends
// typed blocks there must not cost the delta its visible content or tool
// calls: only the reasoning is normalized, and an unreadable reasoning shape is
// dropped on its own.
func TestStreamDelta_typedReasoningContent(t *testing.T) {
	line := []byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":[{"type":"thinking","thinking":[{"type":"text","text":"why"}]}],` +
		`"content":"answer",` +
		`"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}}]}`)

	chunk, _, err := parseSSEChunk(line)
	if err != nil {
		t.Fatalf("parseSSEChunk() error: %v", err)
	}
	d := chunk.Choices[0].Delta
	if d.ReasoningContent != "why" {
		t.Errorf("reasoning = %q, want %q", d.ReasoningContent, "why")
	}
	if d.Content != "answer" {
		t.Errorf("content = %q, want %q", d.Content, "answer")
	}
	if len(d.ToolCalls) != 1 {
		t.Fatalf("tool calls lost: %+v", d.ToolCalls)
	}

	// An unreadable reasoning shape must not fail the whole delta.
	var unwrap StreamDelta
	if err := json.Unmarshal([]byte(`{"reasoning_content":{"type":"thinking"},"content":"kept"}`), &unwrap); err != nil {
		t.Fatalf("unreadable reasoning_content must not fail the delta, got %v", err)
	}
	if unwrap.Content != "kept" {
		t.Errorf("content = %q, want %q", unwrap.Content, "kept")
	}
	if unwrap.ReasoningContent != "" {
		t.Errorf("reasoning = %q, want empty", unwrap.ReasoningContent)
	}
}

// The non-streaming path normalizes reasoning_content the same way.
func TestDecodeChatResponse_typedReasoningContent(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant",` +
		`"reasoning_content":[{"type":"thinking","thinking":[{"type":"text","text":"why"}]}],"content":"answer"},` +
		`"finish_reason":"stop"}]}`

	resp, err := decodeChatResponse([]byte(body))
	if err != nil {
		t.Fatalf("decodeChatResponse() error: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg.ReasoningContent != "why" {
		t.Errorf("reasoning = %q, want %q", msg.ReasoningContent, "why")
	}
	if msg.Content != "answer" {
		t.Errorf("content = %q, want %q", msg.Content, "answer")
	}
}

func TestDecodeContentValue_shapes(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		wantText      string
		wantReasoning string
		wantErr       bool
	}{
		{name: "empty", raw: ``, wantText: "", wantReasoning: ""},
		{name: "null", raw: `null`},
		{name: "string", raw: `"hello"`, wantText: "hello"},
		{name: "text blocks", raw: `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, wantText: "ab"},
		{name: "thinking string", raw: `[{"type":"thinking","thinking":"why"}]`, wantReasoning: "why"},
		{name: "thinking inline text", raw: `[{"type":"thinking","text":"why"}]`, wantReasoning: "why"},
		{name: "unknown block type ignored", raw: `[{"type":"image"},{"type":"text","text":"x"}]`, wantText: "x"},
		{name: "unknown text-bearing block recovered", raw: `[{"type":"citation","text":"cited"},{"type":"text","text":"x"}]`, wantText: "citedx"},
		{name: "object shape", raw: `{"type":"text"}`, wantErr: true},
		{name: "number shape", raw: `42`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, reasoning, err := decodeContentValue(json.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got text=%q reasoning=%q", text, reasoning)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeContentValue() error: %v", err)
			}
			if text != tc.wantText || reasoning != tc.wantReasoning {
				t.Errorf("decodeContentValue() = (%q, %q), want (%q, %q)",
					text, reasoning, tc.wantText, tc.wantReasoning)
			}
		})
	}
}

// The non-streaming path parses content as a string today, so an array-shaped
// message.content fails the whole response; decodeChatResponse must normalize
// it the same way the stream decoder does.
func TestDecodeChatResponse_arrayContent(t *testing.T) {
	body := `{"id":"c1","model":"zai-glm-5-3","choices":[{"index":0,"message":{"role":"assistant",` +
		`"content":[{"type":"thinking","thinking":[{"type":"text","text":"pondering"}],"closed":true},` +
		`{"type":"text","text":"42"}]},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`

	resp, err := decodeChatResponse([]byte(body))
	if err != nil {
		t.Fatalf("decodeChatResponse() error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if msg.Content != "42" {
		t.Errorf("content = %q, want %q", msg.Content, "42")
	}
	if msg.ReasoningContent != "pondering" {
		t.Errorf("reasoning = %q, want %q", msg.ReasoningContent, "pondering")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage.TotalTokens != 13 {
		t.Errorf("usage.total_tokens = %d, want 13", resp.Usage.TotalTokens)
	}
}

// End-to-end: Send must accept the array-shaped non-streaming body.
func TestOpenAIClient_SendArrayContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant",`+
			`"content":[{"type":"thinking","thinking":[{"type":"text","text":"think"}]},{"type":"text","text":"done"}]},`+
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "k", 5)
	resp, err := c.Send(context.Background(), types.ChatRequest{
		Model:    "zai-glm-5-3",
		Messages: []types.Message{types.UserMsg("hi")},
	})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if got := resp.Choices[0].Message.Content; got != "done" {
		t.Errorf("content = %q, want %q", got, "done")
	}
	if got := resp.Choices[0].Message.ReasoningContent; got != "think" {
		t.Errorf("reasoning = %q, want %q", got, "think")
	}
}

// TestDecodeChatResponse_unreadableContentDegrades pins the response path's
// degradation contract: a content shape the decoder cannot read costs the
// message its content, never its tool calls or the rest of the response —
// the streaming path degrades the same way (review 616).
func TestDecodeChatResponse_unreadableContentDegrades(t *testing.T) {
	body := []byte(`{"choices":[{"index":0,"message":{"content":{"text":123},"reasoning_content":"r","tool_calls":[{"id":"t1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	resp, err := decodeChatResponse(body)
	if err != nil {
		t.Fatalf("decodeChatResponse() error = %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("len(Choices) = %d, want 1", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if msg.Content != "" {
		t.Errorf("Content = %q, want empty", msg.Content)
	}
	if msg.ReasoningContent != "r" {
		t.Errorf("ReasoningContent = %q, want %q", msg.ReasoningContent, "r")
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "t1" {
		t.Errorf("ToolCalls = %+v, want the tool call preserved", msg.ToolCalls)
	}
}

// TestMergeReasoning pins the reasoning de-duplication: endpoints that send
// both reasoning_content and content-embedded thinking echo the same text
// twice (review 616).
func TestMergeReasoning(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		blocks string
		want   string
	}{
		{name: "field only", field: "a", want: "a"},
		{name: "blocks only", blocks: "b", want: "b"},
		{name: "equal halves collapse", field: "same", blocks: "same", want: "same"},
		{name: "distinct halves concatenate", field: "a", blocks: "b", want: "ab"},
		{name: "both empty", want: ""},
	}
	for _, tc := range cases {
		if got := mergeReasoning(tc.field, tc.blocks); got != tc.want {
			t.Errorf("mergeReasoning(%q, %q) = %q, want %q", tc.field, tc.blocks, got, tc.want)
		}
	}
}
