package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestLowerMessage_AssistantContentNull(t *testing.T) {
	msg := types.Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: "{}"}}},
	}

	w := lowerMessage(msg, lowerOptions{})
	if w.Content != nil {
		t.Errorf("assistant with only tool_calls should have nil content, got %q", *w.Content)
	}

	data, _ := json.Marshal(w)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if raw["content"] != nil {
		t.Errorf("JSON content should be null, got %v", raw["content"])
	}
}

func TestLowerMessage_AssistantContentPresent(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hello"}

	w := lowerMessage(msg, lowerOptions{})
	if w.Content == nil || *w.Content != "hello" {
		t.Errorf("assistant with text should have content, got %v", w.Content)
	}
}

func TestLowerMessage_AssistantEmptyContentNoToolCalls(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: ""}

	w := lowerMessage(msg, lowerOptions{})
	if w.Content == nil || *w.Content != "" {
		t.Errorf("assistant with no tool_calls should have empty-string content, got %v", w.Content)
	}
}

func TestLowerMessage_ThinkingModeAlwaysSetsReasoning(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hi", ReasoningContent: ""}

	w := lowerMessage(msg, lowerOptions{ThinkingMode: true})
	if w.ReasoningContent == nil {
		t.Fatal("thinking mode should always set reasoning_content on assistant")
	}
	if *w.ReasoningContent != "" {
		t.Errorf("expected empty reasoning_content, got %q", *w.ReasoningContent)
	}

	data, _ := json.Marshal(w)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if _, ok := raw["reasoning_content"]; !ok {
		t.Error("reasoning_content must be present in JSON for thinking mode")
	}
}

func TestLowerMessage_NonThinkingOmitsEmptyReasoning(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hi", ReasoningContent: ""}

	w := lowerMessage(msg, lowerOptions{})
	if w.ReasoningContent != nil {
		t.Errorf("non-thinking mode should omit empty reasoning_content, got %q", *w.ReasoningContent)
	}
}

func TestLowerMessage_NonThinkingPreservesReasoning(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hi", ReasoningContent: "let me think"}

	w := lowerMessage(msg, lowerOptions{})
	if w.ReasoningContent == nil || *w.ReasoningContent != "let me think" {
		t.Errorf("non-thinking mode should preserve non-empty reasoning_content")
	}
}

func TestLowerMessage_UserContentAlwaysPresent(t *testing.T) {
	msg := types.Message{Role: "user", Content: ""}

	w := lowerMessage(msg, lowerOptions{})
	if w.Content == nil {
		t.Fatal("user message should always have content set")
	}
}

func TestLowerRequest_ThinkingMode(t *testing.T) {
	req := types.ChatRequest{
		Model: "deepseek-r1",
		Messages: []types.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi", ReasoningContent: "thinking..."},
			{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "x", Arguments: "{}"}}}},
		},
	}

	wire := lowerRequest(req, lowerOptions{ThinkingMode: true, AllowPromotion: true})

	if wire.Messages[1].ReasoningContent == nil || *wire.Messages[1].ReasoningContent != "thinking..." {
		t.Error("first assistant should preserve reasoning")
	}
	if wire.Messages[2].ReasoningContent == nil {
		t.Error("second assistant should have empty reasoning_content in thinking mode")
	}
	if wire.Messages[2].Content != nil {
		t.Error("second assistant should have null content (tool_calls only)")
	}
}

func TestMergeSystemMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys1"},
		{Role: "system", Content: "sys2"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "system", Content: "sys3"},
		{Role: "user", Content: "next"},
	}

	out := mergeSystemMessages(msgs)

	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if out[0].Role != "user" || out[0].Content != "sys1\n\nsys2\n\nhello" {
		t.Errorf("first user should have merged content, got role=%s content=%q", out[0].Role, out[0].Content)
	}
	if out[1].Role != "assistant" || out[1].Content != "hi" {
		t.Errorf("second should be assistant, got role=%s", out[1].Role)
	}
	if out[2].Role != "user" || out[2].Content != "sys3\n\nnext" {
		t.Errorf("third should be merged user, got role=%s content=%q", out[2].Role, out[2].Content)
	}
}

func TestMergeSystemMessages_OnlySystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys1"},
		{Role: "system", Content: "sys2"},
	}

	out := mergeSystemMessages(msgs)

	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("trailing system should become user, got %s", out[0].Role)
	}
}

func TestMergeSystemMessages_NoSystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	out := mergeSystemMessages(msgs)

	if len(out) != len(msgs) {
		t.Fatalf("no merge needed, expected %d got %d", len(msgs), len(out))
	}
}

func TestCopilotMode_LowerMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hello"},
	}

	out := lowerMessages(msgs, lowerOptions{CopilotMode: true, AllowPromotion: true})

	if len(out) != 1 {
		t.Fatalf("Copilot mode should merge system into user, expected 1 msg got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("expected user role, got %s", out[0].Role)
	}
	want := "you are helpful\n\nhello"
	if out[0].Content == nil || *out[0].Content != want {
		t.Errorf("expected %q, got %v", want, out[0].Content)
	}
}

func TestMergeSystemMessages_PreservesToolCalls(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys1"},
		{Role: "system", Content: "sys2"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: "{}"}}}},
		{Role: "tool", Content: "result", ToolCallID: "c1"},
	}

	out := mergeSystemMessages(msgs)

	if len(out) != 3 {
		for i, m := range out {
			t.Logf("out[%d] role=%s content=%q tool_calls=%d", i, m.Role, m.Content, len(m.ToolCalls))
		}
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("expected system buf as user msg, got %s", out[0].Role)
	}
	if out[1].Role != "assistant" {
		t.Errorf("expected assistant, got %s", out[1].Role)
	}
	if len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "c1" {
		t.Error("tool call should be preserved on assistant")
	}
	if out[2].Role != "tool" || out[2].ToolCallID != "c1" {
		t.Error("tool result should be preserved")
	}
}

// Config is the source of truth: an explicit `thinking: false` must NOT be
// promoted by the history heuristic just because history carries reasoning.
// The resolver test cannot catch this — the override is applied here, in the
// lowering path, one layer below where thinking mode is resolved.
func TestLowerRequest_explicitThinkingFalseIsNotPromoted(t *testing.T) {
	no := false
	c := &OpenAIClient{ThinkingOverrides: map[string]*bool{"some-model": &no}}
	req := types.ChatRequest{
		Model: "some-model",
		Messages: []types.Message{
			{Role: "assistant", Content: "hi", ReasoningContent: "because reasons"},
		},
	}

	wire := lowerRequest(req, c.lowerOptions(req.Model))
	if wire.Messages[0].ReasoningContent != nil {
		t.Error("explicit thinking:false must not be promoted by history")
	}

	// Without an override the history promotion still applies, so the guard
	// above does not disable the heuristic for unconfigured models.
	req.Model = "unconfigured-model"
	wire = lowerRequest(req, c.lowerOptions(req.Model))
	if wire.Messages[0].ReasoningContent == nil {
		t.Error("unconfigured model should still promote on history")
	}
}

// TestLowerRequest_suppressReasoning proves the suppress flag reaches the wire:
// a thinking-capable model must not re-emit reasoning fields once a provider
// has rejected them, because an empty value is still a present field.
func TestLowerRequest_suppressReasoning(t *testing.T) {
	history := []types.Message{{
		Role:               "assistant",
		Content:            "hi",
		ReasoningContent:   "because",
		ReasoningSignature: "sig",
	}}

	plain := lowerRequest(types.ChatRequest{Model: "m", Messages: history}, lowerOptions{ThinkingMode: true, AllowPromotion: true})
	body, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), "reasoning_content") {
		t.Fatalf("baseline: thinking mode should send reasoning_content, got %s", body)
	}

	suppressed := lowerRequest(types.ChatRequest{
		Model:             "m",
		Messages:          history,
		SuppressReasoning: true,
	}, lowerOptions{ThinkingMode: true, AllowPromotion: true})
	body, err = json.Marshal(suppressed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{"reasoning_content", "reasoning_signature"} {
		if strings.Contains(string(body), field) {
			t.Errorf("suppressed request still carries %q: %s", field, body)
		}
	}
}

// TestLowerRequest_cacheControlRequiresOptIn pins yaah-2dgd: cache_control is
// an Anthropic wire concept. An OpenAI-compatible body must never carry it
// unless the provider opted in, because strict endpoints (Mistral, ZAI/GLM)
// reject the unknown field with 422 extra_forbidden.
func TestLowerRequest_cacheControlRequiresOptIn(t *testing.T) {
	req := types.ChatRequest{
		Model: "mistral-large-latest",
		Messages: []types.Message{
			{Role: "system", Content: "sys", CacheControl: &types.CacheControl{Type: "ephemeral"}},
			{Role: "user", Content: "hi"},
			{Role: "tool", Content: "ok", ToolCallID: "1", Name: "echo", CacheControl: &types.CacheControl{Type: "ephemeral"}},
		},
	}

	t.Run("default strips cache_control", func(t *testing.T) {
		wire := lowerRequest(req, lowerOptions{})
		for i, m := range wire.Messages {
			if m.CacheControl != nil {
				t.Errorf("message %d kept CacheControl", i)
			}
		}
		body, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(body), "cache_control") {
			t.Fatalf("OpenAI-compatible body must not carry cache_control: %s", body)
		}
	})

	t.Run("opt-in emits cache_control", func(t *testing.T) {
		body, err := json.Marshal(lowerRequest(req, lowerOptions{CacheControl: true}))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(body), `"cache_control":{"type":"ephemeral"}`) {
			t.Fatalf("opted-in provider should still emit cache_control: %s", body)
		}
	})
}

// TestOpenAIClient_SendStreamGatesCacheControl is the streaming half of the
// same gate: the Mistral 422 that started yaah-2dgd arrived on the SSE path,
// so the request body captured from SendStream must obey the same per-provider
// opt-in.
func TestOpenAIClient_SendStreamGatesCacheControl(t *testing.T) {
	capture := func(t *testing.T, optIn bool) string {
		t.Helper()
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer srv.Close()

		c := NewOpenAIClient(srv.URL, "k", 5)
		c.SupportsCacheControl = optIn
		chunks, errs := c.SendStream(context.Background(), types.ChatRequest{
			Model: "mistral-large-latest",
			Messages: []types.Message{
				{Role: "system", Content: "sys", CacheControl: &types.CacheControl{Type: "ephemeral"}},
				types.UserMsg("hi"),
			},
		})
		for range chunks {
		}
		for err := range errs {
			if err != nil {
				t.Fatalf("SendStream() error: %v", err)
			}
		}
		return got
	}

	if body := capture(t, false); strings.Contains(body, "cache_control") {
		t.Errorf("unopted streaming client leaked cache_control: %s", body)
	}
	if body := capture(t, true); !strings.Contains(body, "cache_control") {
		t.Errorf("opted-in streaming client dropped cache_control: %s", body)
	}
}

// TestOpenAIClient_SendGatesCacheControl proves the seam end to end: a body
// already stamped by PromptCachingMiddleware leaves an unopted client without
// the field, and the provider opt-in restores it.
func TestOpenAIClient_SendGatesCacheControl(t *testing.T) {
	capture := func(t *testing.T, optIn bool) string {
		t.Helper()
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		defer srv.Close()

		c := NewOpenAIClient(srv.URL, "k", 5)
		c.SupportsCacheControl = optIn
		if _, err := c.Send(context.Background(), types.ChatRequest{
			Model: "mistral-large-latest",
			Messages: []types.Message{
				{Role: "system", Content: "sys", CacheControl: &types.CacheControl{Type: "ephemeral"}},
				types.UserMsg("hi"),
			},
		}); err != nil {
			t.Fatalf("Send() error: %v", err)
		}
		return got
	}

	if body := capture(t, false); strings.Contains(body, "cache_control") {
		t.Errorf("unopted client leaked cache_control: %s", body)
	}
	if body := capture(t, true); !strings.Contains(body, "cache_control") {
		t.Errorf("opted-in client dropped cache_control: %s", body)
	}
}
