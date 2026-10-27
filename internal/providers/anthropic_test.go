package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/buchenberg/yaah/internal/types"
)

// ── lowering helpers ─────────────────────────────────────────────
// The SDK's content-block params are pointer unions; these helpers keep
// the lowering tests reading like the wire-format assertions they are.

func blockType(u anthropic.ContentBlockParamUnion) string {
	switch {
	case u.OfText != nil:
		return "text"
	case u.OfThinking != nil:
		return "thinking"
	case u.OfToolUse != nil:
		return "tool_use"
	case u.OfToolResult != nil:
		return "tool_result"
	default:
		return ""
	}
}

func blockText(u anthropic.ContentBlockParamUnion) string {
	if u.OfText != nil {
		return u.OfText.Text
	}
	return ""
}

func blockThinking(u anthropic.ContentBlockParamUnion) string {
	if u.OfThinking != nil {
		return u.OfThinking.Thinking
	}
	return ""
}

func blockToolUse(u anthropic.ContentBlockParamUnion) *anthropic.ToolUseBlockParam {
	return u.OfToolUse
}

func blockToolResult(u anthropic.ContentBlockParamUnion) *anthropic.ToolResultBlockParam {
	return u.OfToolResult
}

func textCacheControl(u anthropic.ContentBlockParamUnion) (anthropic.CacheControlEphemeralParam, bool) {
	if u.OfText != nil && u.OfText.CacheControl.Type != "" {
		return u.OfText.CacheControl, true
	}
	return anthropic.CacheControlEphemeralParam{}, false
}

// translateSSE unmarshals one raw SSE event payload into the SDK union and
// runs it through the translator, mirroring what the SDK decoder hands to
// translateAnthropicEvent in production.
func translateSSE(t *testing.T, data string, st *anthropicStreamState) (*StreamChunk, error) {
	t.Helper()
	var event anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	return translateAnthropicEvent(event, st)
}

// ── request lowering ─────────────────────────────────────────────

func TestChatRequestToAnthropic_SystemMessages(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if len(result.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(result.System))
	}
	if result.System[0].Text != "You are helpful." {
		t.Errorf("expected system text 'You are helpful.', got %q", result.System[0].Text)
	}
	// System blocks are TextBlockParams — the SDK injects the "text"
	// wire tag at marshal time (default:"text"), so the block kind is
	// asserted by the union type rather than a Type field here.

	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Errorf("expected message role 'user', got %q", result.Messages[0].Role)
	}
}

func TestChatRequestToAnthropic_Conversation(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "How are you?"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", result.Messages[0].Role)
	}
	if result.Messages[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %q", result.Messages[1].Role)
	}
	if result.Messages[2].Role != "user" {
		t.Errorf("expected third message role 'user', got %q", result.Messages[2].Role)
	}
}

func TestChatRequestToAnthropic_MergesConsecutiveUser(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "First"},
			{Role: "user", Content: "Second"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(result.Messages))
	}
	if len(result.Messages[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks in merged message, got %d", len(result.Messages[0].Content))
	}
}

func TestChatRequestToAnthropic_ToolCalls(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Get weather"},
			{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{
				{ID: "call_1", Type: "function", Function: types.ToolCallFn{Name: "get_weather", Arguments: `{"city":"NYC"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "72F sunny"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, tool_result_user), got %d", len(result.Messages))
	}

	user1 := result.Messages[0]
	if user1.Role != "user" {
		t.Errorf("expected first message role 'user', got %q", user1.Role)
	}

	assistant := result.Messages[1]
	if assistant.Role != "assistant" {
		t.Errorf("expected assistant role, got %q", assistant.Role)
	}
	hasToolUse := false
	for _, block := range assistant.Content {
		if blockType(block) == "tool_use" {
			hasToolUse = true
			toolUse := blockToolUse(block)
			if toolUse.ID != "call_1" {
				t.Errorf("expected tool_use id 'call_1', got %q", toolUse.ID)
			}
			if toolUse.Name != "get_weather" {
				t.Errorf("expected tool_use name 'get_weather', got %q", toolUse.Name)
			}
		}
	}
	if !hasToolUse {
		t.Error("expected tool_use block in assistant message")
	}

	user2 := result.Messages[2]
	if user2.Role != "user" {
		t.Errorf("expected user role for tool result, got %q", user2.Role)
	}
	hasToolResult := false
	for _, block := range user2.Content {
		if blockType(block) == "tool_result" {
			hasToolResult = true
			toolResult := blockToolResult(block)
			if toolResult.ToolUseID != "call_1" {
				t.Errorf("expected tool_result tool_use_id 'call_1', got %q", toolResult.ToolUseID)
			}
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result block in user message")
	}
}

func TestChatRequestToAnthropic_Tools(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Tools: []types.ToolDef{
			{Type: "function", Function: types.ToolFn{Name: "get_weather", Description: "Get weather", Parameters: params}},
		},
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].OfTool == nil {
		t.Fatal("expected a custom tool param")
	}
	if result.Tools[0].OfTool.Name != "get_weather" {
		t.Errorf("expected tool name 'get_weather', got %q", result.Tools[0].OfTool.Name)
	}
	if !result.Tools[0].OfTool.Description.Valid() || result.Tools[0].OfTool.Description.Or("") != "Get weather" {
		t.Errorf("expected tool description 'Get weather', got valid=%v value=%q", result.Tools[0].OfTool.Description.Valid(), result.Tools[0].OfTool.Description.Or(""))
	}
}

func TestChatRequestToAnthropic_ToolSchemaRoundTrip(t *testing.T) {
	// The tool's JSON schema must survive the lowering round-trip —
	// properties, required, and any extra JSON-Schema keys.
	schema := `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Tools: []types.ToolDef{
			{Type: "function", Function: types.ToolFn{Name: "get_weather", Description: "Get weather", Parameters: json.RawMessage(schema)}},
		},
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	var wire struct {
		Tools []struct {
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools"`
	}
	body, err := json.Marshal(lowerAnthropicRequest(req, "auto"))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if len(wire.Tools) != 1 {
		t.Fatalf("expected 1 tool on the wire, got %d", len(wire.Tools))
	}
	is := wire.Tools[0].InputSchema
	if is["type"] != "object" {
		t.Errorf("expected schema type 'object', got %v", is["type"])
	}
	if _, ok := is["properties"].(map[string]any); !ok {
		t.Errorf("expected properties object on the wire, got %T", is["properties"])
	}
	if req, ok := is["required"].([]any); !ok || len(req) != 1 || req[0] != "city" {
		t.Errorf("expected required [city] on the wire, got %v", is["required"])
	}
	if is["additionalProperties"] != false {
		t.Errorf("expected additionalProperties false to survive, got %v", is["additionalProperties"])
	}
}

func TestChatRequestToAnthropic_MaxTokensDefault(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 0,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if result.MaxTokens != 4096 {
		t.Errorf("expected MaxTokens 4096, got %d", result.MaxTokens)
	}
}

func TestChatRequestToAnthropic_MaxTokensExplicit(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 8000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if result.MaxTokens != 8000 {
		t.Errorf("expected MaxTokens 8000, got %d", result.MaxTokens)
	}
}

func TestAnthropicClient_DefaultMaxTokens(t *testing.T) {
	client := NewAnthropicClient("https://api.z.ai/api/anthropic", "sk-test", 30)
	client.DefaultMaxTokens = 16000

	req := types.ChatRequest{
		Model:    "glm-5.3-flash",
		Messages: []types.Message{{Role: "user", Content: "Hello"}},
	}
	if got := client.lower(req).MaxTokens; got != 16000 {
		t.Errorf("provider default: expected MaxTokens 16000, got %d", got)
	}

	// An explicit per-request cap must win over the provider default.
	req.MaxTokens = 32000
	if got := client.lower(req).MaxTokens; got != 32000 {
		t.Errorf("explicit cap: expected MaxTokens 32000, got %d", got)
	}
}

func TestAnthropicClient_EndpointAwareFallbackMaxTokens(t *testing.T) {
	req := types.ChatRequest{
		Model:    "claude-3-haiku",
		Messages: []types.Message{{Role: "user", Content: "Hello"}},
	}

	native := NewAnthropicClient("https://api.anthropic.com", "sk-test", 30)
	if got := native.lower(req).MaxTokens; got != nativeAnthropicMaxTokens {
		t.Errorf("native endpoint: expected %d, got %d", nativeAnthropicMaxTokens, got)
	}

	compatible := NewAnthropicClient("https://api.z.ai/api/anthropic", "sk-test", 30)
	compatibleReq := req
	compatibleReq.Model = "unknown-model"
	if got := compatible.lower(compatibleReq).MaxTokens; got != compatibleAnthropicMaxTokens {
		t.Errorf("compatible endpoint: expected %d, got %d", compatibleAnthropicMaxTokens, got)
	}
	if compatibleAnthropicMaxTokens != 16384 {
		t.Errorf("compatible fallback = %d, want 16384 (room for reasoning tokens)", compatibleAnthropicMaxTokens)
	}
}

func TestAnthropicClient_EffectiveMaxTokens(t *testing.T) {
	client := NewAnthropicClient("https://api.z.ai/api/anthropic", "sk-test", 30)
	client.DefaultMaxTokens = 32000

	if got := client.EffectiveMaxTokens(types.ChatRequest{Model: "m"}); got != 32000 {
		t.Errorf("unset request: EffectiveMaxTokens = %d, want provider default 32000", got)
	}
	if got := client.EffectiveMaxTokens(types.ChatRequest{Model: "m", MaxTokens: 4096}); got != 4096 {
		t.Errorf("explicit cap: EffectiveMaxTokens = %d, want 4096", got)
	}

	client.DefaultMaxTokens = 0
	if got := client.EffectiveMaxTokens(types.ChatRequest{Model: "m"}); got != compatibleAnthropicMaxTokens {
		t.Errorf("fallback: EffectiveMaxTokens = %d, want %d", got, compatibleAnthropicMaxTokens)
	}
	if got := client.EffectiveMaxTokens(types.ChatRequest{Model: "claude-3-haiku"}); got != 4096 {
		t.Errorf("known lower-output model: EffectiveMaxTokens = %d, want 4096", got)
	}
}

func TestAnthropicClient_OutputTokenCeiling(t *testing.T) {
	req := types.ChatRequest{Model: "m"}

	native := NewAnthropicClient("https://api.anthropic.com", "sk-test", 30)
	if got := native.OutputTokenCeiling(req); got != nativeAnthropicMaxTokens {
		t.Errorf("native ceiling = %d, want %d", got, nativeAnthropicMaxTokens)
	}

	compatible := NewAnthropicClient("https://api.z.ai/api/anthropic", "sk-test", 30)
	if got := compatible.OutputTokenCeiling(req); got != 0 {
		t.Errorf("compatible ceiling for unknown model = %d, want 0 (unknown)", got)
	}
	if got := compatible.OutputTokenCeiling(types.ChatRequest{Model: "GLM-5.3"}); got != 32768 {
		t.Errorf("GLM-5.3 ceiling = %d, want 32768", got)
	}
	if got := compatible.OutputTokenCeiling(types.ChatRequest{Model: "claude-3-5-sonnet-20250620"}); got != 8192 {
		t.Errorf("dated Claude ceiling = %d, want 8192", got)
	}
	if got := compatible.OutputTokenCeiling(types.ChatRequest{Model: "claude-3.5-sonnet"}); got != 8192 {
		t.Errorf("dotted Claude ceiling = %d, want 8192", got)
	}
}

func TestChatRequestToAnthropic_CacheControlPassthrough(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful.", CacheControl: &types.CacheControl{Type: "ephemeral"}},
			{Role: "user", Content: "Hello", CacheControl: &types.CacheControl{Type: "ephemeral"}},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if result.System[0].CacheControl.Type != "ephemeral" {
		t.Errorf("expected cache_control type 'ephemeral' on system block, got %q", result.System[0].CacheControl.Type)
	}

	cc, ok := textCacheControl(result.Messages[0].Content[0])
	if !ok {
		t.Fatal("expected cache_control on user text block")
	}
	if cc.Type != "ephemeral" {
		t.Errorf("expected cache_control type 'ephemeral', got %q", cc.Type)
	}
}

func TestChatRequestToAnthropic_ReasoningContent(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{
				Role:               "assistant",
				ReasoningContent:   "Let me think about this...",
				ReasoningSignature: "sig-abc123",
				Content:            "The answer is 42.",
			},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	blocks := result.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (thinking + text), got %d", len(blocks))
	}
	if blockType(blocks[0]) != "thinking" {
		t.Errorf("expected first block type 'thinking', got %q", blockType(blocks[0]))
	}
	// The signature must be replayed verbatim: fabricating an empty one is
	// what produced "the thinking mode must be passed back to the API".
	if got := blocks[0].OfThinking.Signature; got != "sig-abc123" {
		t.Errorf("thinking block signature = %q, want %q", got, "sig-abc123")
	}
	if blockThinking(blocks[0]) != "Let me think about this..." {
		t.Errorf("expected thinking content, got %q", blockThinking(blocks[0]))
	}
	if blockType(blocks[1]) != "text" {
		t.Errorf("expected second block type 'text', got %q", blockType(blocks[1]))
	}
}

// With reasoning_replay=auto, a thinking block without its provider
// signature is replayed as a thinking block with an empty signature:
// Anthropic-compatible endpoints accept it, and dropping the reasoning
// costs the model its chain-of-thought (yaah-v378.6). Replaying it as
// plain TEXT instead taught the model that its reasoning was visible
// output — it echoed the thinking into the answer (yaah-63wu).
func TestChatRequestToAnthropic_ThinkingWithoutSignatureReplaysAsThinking(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "assistant", ReasoningContent: "orphaned thinking", Content: "The answer is 42."},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	blocks := result.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (thinking + text), got %d", len(blocks))
	}
	if blockType(blocks[0]) != "thinking" {
		t.Fatalf("expected first block type 'thinking', got %q", blockType(blocks[0]))
	}
	if got := blocks[0].OfThinking.Signature; got != "" {
		t.Errorf("unsigned thinking block signature = %q, want empty", got)
	}
	if got := blockThinking(blocks[0]); got != "orphaned thinking" {
		t.Errorf("thinking block text = %q, want %q", got, "orphaned thinking")
	}
	for _, b := range blocks[1:] {
		if strings.Contains(blockText(b), "orphaned thinking") {
			t.Error("reasoning must not be duplicated into visible text under reasoning_replay=auto")
		}
	}
}

// reasoning_replay=auto is endpoint-aware: the native Anthropic API
// validates signatures, so unsigned thinking is omitted there, while
// Anthropic-compatible endpoints get the thinking block back.
func TestAnthropicClient_ReplayModeIsEndpointAware(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		replay  string
		want    string
	}{
		{"compatible endpoint uses auto", "https://token-plan-sgp.xiaomimimo.com/anthropic", "", "auto"},
		{"compatible endpoint without scheme", "api.deepseek.com/anthropic", "", "auto"},
		{"native endpoint degrades to omit", "https://api.anthropic.com", "", "omit"},
		{"native endpoint with trailing slash", "https://api.anthropic.com/", "", "omit"},
		{"empty base URL means native", "", "", "omit"},
		{"explicit text wins", "https://api.deepseek.com/anthropic", "text", "text"},
		{"explicit omit wins", "https://api.deepseek.com/anthropic", "omit", "omit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewAnthropicClient(tc.baseURL, "sk-test", 30)
			c.ReasoningReplay = tc.replay
			if got := c.replayMode(); got != tc.want {
				t.Errorf("replayMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The lowered request reflects the endpoint-aware mode: a compatible
// endpoint replays unsigned thinking as a thinking block, while the
// native API drops it and keeps only the visible text.
func TestAnthropicClient_LowerReplaysUnsignedThinkingByEndpoint(t *testing.T) {
	req := types.ChatRequest{
		Model:     "m",
		MaxTokens: 100,
		Messages: []types.Message{
			{Role: "assistant", ReasoningContent: "orphaned thinking", Content: "The answer is 42."},
		},
	}

	compat := NewAnthropicClient("https://api.deepseek.com/anthropic", "sk-test", 30)
	blocks := compat.lower(req).Messages[0].Content
	if len(blocks) != 2 || blockType(blocks[0]) != "thinking" {
		t.Fatalf("compatible endpoint must replay unsigned thinking as thinking, got %v blocks", len(blocks))
	}

	native := NewAnthropicClient("https://api.anthropic.com", "sk-test", 30)
	blocks = native.lower(req).Messages[0].Content
	if len(blocks) != 1 || blockType(blocks[0]) != "text" {
		t.Fatalf("native endpoint must drop unsigned thinking and keep the answer, got %d blocks", len(blocks))
	}
	if strings.Contains(blockText(blocks[0]), "orphaned thinking") {
		t.Error("native endpoint must not fold reasoning into visible text")
	}
}

// reasoning_replay: omit restores the old behavior — unsigned thinking is
// dropped entirely, neither as thinking nor as text.
func TestChatRequestToAnthropic_ThinkingWithoutSignatureIsOmitted(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "assistant", ReasoningContent: "orphaned thinking", Content: "The answer is 42."},
		},
	}

	result := lowerAnthropicRequest(req, "omit")

	for _, b := range result.Messages[0].Content {
		if blockType(b) == "thinking" {
			t.Fatal("a thinking block with no signature must not be emitted under reasoning_replay=omit")
		}
		if strings.Contains(blockText(b), "orphaned thinking") {
			t.Fatal("a thinking block with no signature must not be replayed as text under reasoning_replay=omit")
		}
	}
}

// reasoning_replay: text downgrades even SIGNED reasoning to a text block,
// for endpoints that reject valid thinking replay.
func TestChatRequestToAnthropic_TextStrategyDowngradesSignedThinking(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{
				Role:               "assistant",
				ReasoningContent:   "Let me think about this...",
				ReasoningSignature: "sig-abc123",
				Content:            "The answer is 42.",
			},
		},
	}

	result := lowerAnthropicRequest(req, "text")

	for _, b := range result.Messages[0].Content {
		if blockType(b) == "thinking" {
			t.Fatal("reasoning_replay=text must not emit thinking blocks")
		}
	}
	var sawReasoningAsText bool
	for _, b := range result.Messages[0].Content {
		if strings.Contains(blockText(b), "Let me think about this...") {
			sawReasoningAsText = true
		}
	}
	if !sawReasoningAsText {
		t.Error("reasoning_replay=text must keep the reasoning in context as text")
	}
}

// A signature-validating gateway behind a compatible URL rejects the
// unsigned replay shape with 400. replayFallback recognizes only that
// rejection, drops unsigned thinking, and asks the caller to retry once.
func TestAnthropicClient_ReplayFallbackConditions(t *testing.T) {
	unsigned := []types.Message{{Role: "assistant", ReasoningContent: "cot", Content: "answer"}}
	signed := []types.Message{{Role: "assistant", ReasoningContent: "cot", ReasoningSignature: "sig", Content: "answer"}}
	cases := []struct {
		name   string
		replay string
		msgs   []types.Message
		err    error
		want   bool
	}{
		{"signature 400 with unsigned thinking", "", unsigned, &APIError{StatusCode: 400, Message: "thinking blocks must be signed"}, true},
		{"thinking block 400", "", unsigned, &APIError{StatusCode: 400, Message: "thinking blocks must be passed back to the API"}, true},
		{"thinking budget 400", "", unsigned, &APIError{StatusCode: 400, Message: "thinking.budget_tokens must be less than max_tokens"}, false},
		{"thinking unsupported 400", "", unsigned, &APIError{StatusCode: 400, Message: "thinking is not supported for this model"}, false},
		{"unrelated 400", "", unsigned, &APIError{StatusCode: 400, Message: "bad request"}, false},
		{"non-400 status", "", unsigned, &APIError{StatusCode: 500, Message: "thinking broke"}, false},
		{"no unsigned thinking", "", signed, &APIError{StatusCode: 400, Message: "thinking blocks must be signed"}, false},
		{"explicit text mode", "text", unsigned, &APIError{StatusCode: 400, Message: "thinking blocks must be signed"}, false},
		{"native endpoint already omits", "auto", unsigned, &APIError{StatusCode: 400, Message: "signature invalid"}, false},
		{"non-provider error", "", unsigned, errors.New("connection reset"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseURL := "https://gateway.example/anthropic"
			if tc.name == "native endpoint already omits" {
				baseURL = "https://api.anthropic.com"
			}
			c := NewAnthropicClient(baseURL, "sk-test", 30)
			c.ReasoningReplay = tc.replay
			req := types.ChatRequest{Messages: tc.msgs}
			mode, got := c.replayFallback(req, tc.err)
			if got != tc.want {
				t.Fatalf("replayFallback() = %v, want %v", got, tc.want)
			}
			if got && mode != "omit" {
				t.Errorf("replayFallback() mode = %q, want omit", mode)
			}
			// The downgrade is only committed once the retry succeeds.
			if tc.want {
				if c.replayMode() != "auto" {
					t.Fatal("replayMode changed before the retry was validated")
				}
				c.acceptReplayFallback()
				if c.replayMode() != "omit" {
					t.Errorf("replayMode() = %q after a validated fallback, want omit", c.replayMode())
				}
			}
		})
	}
}

// The non-streaming path drops the unsigned thinking block and retries once
// when the endpoint rejects it.
func TestAnthropicSend_RetriesWithoutUnsignedThinkingWhenRejected(t *testing.T) {
	var attempts int
	var sentThinking []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, _ := io.ReadAll(r.Body)
		sentThinking = append(sentThinking, strings.Contains(string(body), `"type":"thinking"`))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"type":"thinking"`) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking blocks must be signed"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)

	client := NewAnthropicClient(srv.URL, "sk-test", 30)
	resp, err := client.Send(context.Background(), types.ChatRequest{
		Model:     "m",
		MaxTokens: 64,
		Messages: []types.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ReasoningContent: "cot", Content: "answer"},
			{Role: "user", Content: "again"},
		},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if resp == nil || resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if !sentThinking[0] || sentThinking[1] {
		t.Errorf("thinking block presence per attempt = %v, want [true false]", sentThinking)
	}
	if client.replayMode() != "omit" {
		t.Errorf("replayMode() = %q after a validated fallback, want omit", client.replayMode())
	}
}

// The streaming path applies the same fallback: the rejection arrives
// before any event, so the retry cannot duplicate output.
func TestAnthropicSendStream_RetriesWithoutUnsignedThinkingWhenRejected(t *testing.T) {
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		reqBody, _ := io.ReadAll(r.Body)
		if strings.Contains(string(reqBody), `"type":"thinking"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking blocks must be signed"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	client := NewAnthropicClient(srv.URL, "sk-test", 30)
	chunks, errs := client.SendStream(context.Background(), types.ChatRequest{
		Model:     "m",
		MaxTokens: 64,
		Messages: []types.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ReasoningContent: "cot", Content: "answer"},
			{Role: "user", Content: "again"},
		},
	})
	content, _, streamErr := drainStream(chunks, errs)
	if streamErr != nil {
		t.Fatalf("SendStream() error = %v", streamErr)
	}
	if content != "ok" {
		t.Errorf("streamed content = %q, want %q", content, "ok")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if client.replayMode() != "omit" {
		t.Errorf("replayMode() = %q after a validated fallback, want omit", client.replayMode())
	}
}

func TestChatRequestToAnthropic_TemperaturePassthrough(t *testing.T) {
	req := types.ChatRequest{
		Model:       "claude-3.5-sonnet",
		MaxTokens:   1000,
		Temperature: 0.7,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if !result.Temperature.Valid() || result.Temperature.Or(0) != 0.7 {
		t.Errorf("expected Temperature 0.7, got valid=%v value=%v", result.Temperature.Valid(), result.Temperature.Or(0))
	}
}

func TestChatRequestToAnthropic_EmptySystem(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if len(result.System) != 0 {
		t.Errorf("expected 0 system blocks, got %d", len(result.System))
	}
}

func TestAnthropicClientInterface(t *testing.T) {
	client := NewAnthropicClient("https://api.anthropic.com", "sk-test", 30)
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Error("expected error from ListModels (not supported by Anthropic API)")
	}
	if !strings.Contains(err.Error(), "not support") {
		t.Errorf("expected 'not support' in error, got %q", err.Error())
	}
}

func TestChatRequestToAnthropic_PreservesModel(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3-opus",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	if result.Model != "claude-3-opus" {
		t.Errorf("expected model 'claude-3-opus', got %q", result.Model)
	}
}

func TestChatRequestToAnthropic_ToolResultWithoutUser(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Do something"},
			{Role: "assistant", ToolCalls: []types.ToolCall{
				{ID: "call_1", Type: "function", Function: types.ToolCallFn{Name: "search", Arguments: `{"q":"test"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != "user" {
		t.Errorf("expected last message to be user (wrap orphan tool results), got %q", lastMsg.Role)
	}
	hasToolResult := false
	for _, block := range lastMsg.Content {
		if blockType(block) == "tool_result" {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result block in orphan tool result message")
	}
}

func TestChatRequestToAnthropic_UserWithToolResults(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Initial question"},
			{Role: "assistant", ToolCalls: []types.ToolCall{
				{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: `{}`}},
			}},
			{Role: "tool", ToolCallID: "c1", Content: "file content"},
			{Role: "user", Content: "Now analyze it"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	secondUser := result.Messages[2]
	if secondUser.Role != "user" {
		t.Fatalf("expected third message role 'user', got %q", secondUser.Role)
	}
	hasToolResult := false
	hasText := false
	for _, block := range secondUser.Content {
		if blockType(block) == "tool_result" {
			hasToolResult = true
		}
		if blockType(block) == "text" && blockText(block) == "Now analyze it" {
			hasText = true
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result block in user message")
	}
	if !hasText {
		t.Error("expected text block in user message")
	}
}

// TestChatRequestToAnthropic_ToolResultBeforeAssistant pins the multi-turn
// shape that produced provider 400 "tool_use ids were found without
// tool_result blocks immediately after": a completed tool turn
// (assistant tool_calls → tool results) followed by the assistant's final
// text and a new user prompt. The tool_result blocks must be flushed as a
// user message directly after the tool_use assistant message — not deferred
// until the next user turn, which would leave the final assistant message
// between the tool_use and its results.
func TestChatRequestToAnthropic_ToolResultBeforeAssistant(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "Get weather"},
			{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{
				{ID: "call_00_abc", Type: "function", Function: types.ToolCallFn{Name: "get_weather", Arguments: `{"city":"NYC"}`}},
			}},
			{Role: "tool", ToolCallID: "call_00_abc", Content: "72F sunny"},
			{Role: "assistant", Content: "It is 72F and sunny in NYC."},
			{Role: "user", Content: "And tomorrow?"},
		},
	}

	result := lowerAnthropicRequest(req, "auto")

	// system is hoisted out: user, assistant(tool_use), user(tool_result),
	// assistant(text), user(text)
	if len(result.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result.Messages))
	}
	wantRoles := []string{"user", "assistant", "user", "assistant", "user"}
	for i, want := range wantRoles {
		if string(result.Messages[i].Role) != want {
			t.Errorf("message %d: expected role %q, got %q", i, want, result.Messages[i].Role)
		}
	}

	// The message immediately after the tool_use assistant message must
	// carry the matching tool_result block.
	resultMsg := result.Messages[2]
	hasToolResult := false
	for _, block := range resultMsg.Content {
		if blockType(block) == "tool_result" && blockToolResult(block).ToolUseID == "call_00_abc" {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result for call_00_abc immediately after the tool_use assistant message")
	}

	// The final user turn must not be polluted with the earlier result.
	for _, block := range result.Messages[4].Content {
		if blockType(block) == "tool_result" {
			t.Error("final user message must not contain a tool_result block")
		}
	}
}

// ── response raising ─────────────────────────────────────────────

func anthropicTestMessage(content []anthropic.ContentBlockUnion, stopReason string, usage anthropic.Usage) anthropic.Message {
	return anthropic.Message{
		ID:         "msg_test",
		Model:      "claude-3.5-sonnet",
		Content:    content,
		StopReason: anthropic.StopReason(stopReason),
		Usage:      usage,
	}
}

func TestRaiseAnthropicResponse(t *testing.T) {
	resp := anthropicTestMessage([]anthropic.ContentBlockUnion{
		{Type: "text", Text: "Hello "},
		{Type: "text", Text: "world!"},
		{Type: "tool_use", ID: "toolu_1", Name: "get_weather", Input: json.RawMessage(`{"city":"NYC"}`)},
	}, "end_turn", anthropic.Usage{
		InputTokens:          50,
		OutputTokens:         30,
		CacheReadInputTokens: 20,
	})
	resp.ID = "msg_123"

	result := raiseAnthropicResponse(resp)

	if result.ID != "msg_123" {
		t.Errorf("expected ID 'msg_123', got %q", result.ID)
	}
	if result.Model != "claude-3.5-sonnet" {
		t.Errorf("expected Model 'claude-3.5-sonnet', got %q", result.Model)
	}
	if len(result.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(result.Choices))
	}

	choice := result.Choices[0]
	if choice.Message.Content != "Hello world!" {
		t.Errorf("expected content 'Hello world!', got %q", choice.Message.Content)
	}
	if choice.FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(choice.Message.ToolCalls))
	}
	if choice.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected tool call name 'get_weather', got %q", choice.Message.ToolCalls[0].Function.Name)
	}

	if result.Usage.PromptTokens != 50 {
		t.Errorf("expected PromptTokens 50, got %d", result.Usage.PromptTokens)
	}
	if result.Usage.PromptTokensDetails.CachedTokens != 20 {
		t.Errorf("expected CachedTokens 20, got %d", result.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestAnthropicResponseToChat_ToolUseStopReason(t *testing.T) {
	resp := anthropicTestMessage([]anthropic.ContentBlockUnion{
		{Type: "tool_use", ID: "toolu_2", Name: "search", Input: json.RawMessage(`{"query":"test"}`)},
	}, "tool_use", anthropic.Usage{InputTokens: 10, OutputTokens: 5})
	resp.ID = "msg_456"

	result := raiseAnthropicResponse(resp)

	if result.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls' for tool_use stop, got %q", result.Choices[0].FinishReason)
	}
}

func TestAnthropicResponseToChat_Thinking(t *testing.T) {
	resp := anthropicTestMessage([]anthropic.ContentBlockUnion{
		{Type: "thinking", Thinking: "Hmm, let me consider..."},
		{Type: "text", Text: "The answer is 42."},
	}, "end_turn", anthropic.Usage{InputTokens: 20, OutputTokens: 15})
	resp.ID = "msg_789"

	result := raiseAnthropicResponse(resp)

	if result.Choices[0].Message.ReasoningContent != "Hmm, let me consider..." {
		t.Errorf("expected reasoning content, got %q", result.Choices[0].Message.ReasoningContent)
	}
	if result.Choices[0].Message.Content != "The answer is 42." {
		t.Errorf("expected text content, got %q", result.Choices[0].Message.Content)
	}
}

func TestAnthropicResponseToChat_MaxTokensStop(t *testing.T) {
	resp := anthropicTestMessage([]anthropic.ContentBlockUnion{
		{Type: "text", Text: "truncated..."},
	}, "max_tokens", anthropic.Usage{InputTokens: 10, OutputTokens: 4096})
	resp.ID = "msg_999"

	result := raiseAnthropicResponse(resp)

	if result.Choices[0].FinishReason != "length" {
		t.Errorf("expected finish_reason 'length' for max_tokens stop, got %q", result.Choices[0].FinishReason)
	}
}

func TestAnthropicResponseToChat_Usage(t *testing.T) {
	resp := anthropicTestMessage([]anthropic.ContentBlockUnion{
		{Type: "text", Text: "ok"},
	}, "end_turn", anthropic.Usage{
		InputTokens:          100,
		OutputTokens:         50,
		CacheReadInputTokens: 75,
	})
	resp.ID = "msg_xyz"

	result := raiseAnthropicResponse(resp)

	if result.Usage.PromptTokens != 100 {
		t.Errorf("expected PromptTokens 100, got %d", result.Usage.PromptTokens)
	}
	if result.Usage.CompletionTokens != 50 {
		t.Errorf("expected CompletionTokens 50, got %d", result.Usage.CompletionTokens)
	}
	if result.Usage.TotalTokens != 150 {
		t.Errorf("expected TotalTokens 150, got %d", result.Usage.TotalTokens)
	}
	if result.Usage.PromptTokensDetails.CachedTokens != 75 {
		t.Errorf("expected CachedTokens 75, got %d", result.Usage.PromptTokensDetails.CachedTokens)
	}
}

// ── stream event translation ─────────────────────────────────────

func TestTranslateAnthropicEvent_MessageStart(t *testing.T) {
	data := `{"type":"message_start","message":{"id":"msg_001","model":"claude-3.5-sonnet"}}`

	chunk, err := translateSSE(t, data, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if chunk == nil {
		t.Fatal("expected non-nil chunk")
	}
	if chunk.ID != "msg_001" {
		t.Errorf("expected ID 'msg_001', got %q", chunk.ID)
	}
	if chunk.Model != "claude-3.5-sonnet" {
		t.Errorf("expected Model 'claude-3.5-sonnet', got %q", chunk.Model)
	}
}

func TestTranslateAnthropicEvent_TextStream(t *testing.T) {
	startData := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	deltaData := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`

	startChunk, err := translateSSE(t, startData, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if startChunk == nil {
		t.Fatal("expected non-nil start chunk")
	}
	if startChunk.Choices[0].Delta.Role != "assistant" {
		t.Errorf("expected role 'assistant' on text block start, got %q", startChunk.Choices[0].Delta.Role)
	}

	deltaChunk, err := translateSSE(t, deltaData, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deltaChunk == nil {
		t.Fatal("expected non-nil delta chunk")
	}
	if deltaChunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", deltaChunk.Choices[0].Delta.Content)
	}
}

func TestTranslateAnthropicEvent_ToolUseStream(t *testing.T) {
	startData := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"get_weather"}}`
	deltaData := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"NYC\"}"}}`

	st := &anthropicStreamState{}

	startChunk, err := translateSSE(t, startData, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if startChunk == nil {
		t.Fatal("expected non-nil tool_use start chunk")
	}
	tcs := startChunk.Choices[0].Delta.ToolCalls
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tcs))
	}
	if tcs[0].ID != "toolu_abc" {
		t.Errorf("expected tool call ID 'toolu_abc', got %q", tcs[0].ID)
	}
	if tcs[0].Function.Name != "get_weather" {
		t.Errorf("expected function name 'get_weather', got %q", tcs[0].Function.Name)
	}

	deltaChunk, err := translateSSE(t, deltaData, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deltaChunk == nil {
		t.Fatal("expected non-nil tool_use delta chunk")
	}
	args := deltaChunk.Choices[0].Delta.ToolCalls[0].Function.Arguments
	if args != `{"city":"NYC"}` {
		t.Errorf("expected streamed args '{\"city\":\"NYC\"}', got %q", args)
	}
}

func TestTranslateAnthropicEvent_ToolUseMultiFragment(t *testing.T) {
	st := &anthropicStreamState{}

	startData := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_xyz","name":"search"}}`
	translateSSE(t, startData, st)

	frag1 := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"qu"}}`
	c1, err := translateSSE(t, frag1, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	frag2 := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"ery\":\"hello\"}"}}`
	c2, err := translateSSE(t, frag2, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The fragments must flow through unmodified; the consumer
	// (internal/agent/llm stream assembly) accumulates them by index.
	if got := c1.Choices[0].Delta.ToolCalls[0].Function.Arguments; got != `{"qu` {
		t.Errorf("fragment 1: expected %s, got %q", `{"qu`, got)
	}
	if got := c2.Choices[0].Delta.ToolCalls[0].Function.Arguments; got != `ery":"hello"}` {
		t.Errorf("fragment 2: expected %s, got %q", `ery":"hello"}`, got)
	}
}

func TestTranslateAnthropicEvent_MessageDelta(t *testing.T) {
	data := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`

	chunk, err := translateSSE(t, data, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if chunk == nil {
		t.Fatal("expected non-nil chunk")
	}
	if chunk.Choices[0].FinishReason == nil {
		t.Fatal("expected non-nil finish_reason")
	}
	if *chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", *chunk.Choices[0].FinishReason)
	}
	if chunk.Usage.CompletionTokens != 42 {
		t.Errorf("expected CompletionTokens 42, got %d", chunk.Usage.CompletionTokens)
	}
}

func TestTranslateAnthropicEvent_MessageDeltaToolUse(t *testing.T) {
	data := `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`

	chunk, err := translateSSE(t, data, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if chunk.Choices[0].FinishReason == nil {
		t.Fatal("expected non-nil finish_reason")
	}
	if *chunk.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls', got %q", *chunk.Choices[0].FinishReason)
	}
}

func TestTranslateAnthropicEvent_MessageDeltaMaxTokens(t *testing.T) {
	data := `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4096}}`

	chunk, err := translateSSE(t, data, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunk.Choices[0].FinishReason == nil {
		t.Fatal("expected non-nil finish_reason")
	}
	if *chunk.Choices[0].FinishReason != "length" {
		t.Errorf("expected finish_reason 'length' for max_tokens, got %q", *chunk.Choices[0].FinishReason)
	}
}

func TestTranslateAnthropicEvent_MessageStopFallback(t *testing.T) {
	// A proxy that emits message_stop without a preceding message_delta
	// must still yield a finish reason so the assembled response is not
	// discarded as a premature close.
	st := &anthropicStreamState{}
	chunk, err := translateSSE(t, `{"type":"message_stop"}`, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunk == nil {
		t.Fatal("expected synthesized finish chunk")
	}
	if chunk.Choices[0].FinishReason == nil || *chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %v", chunk.Choices[0].FinishReason)
	}
}

// TestTranslateAnthropicEvent_DeltaWithoutStopReasonThenStop pins that a
// non-conforming proxy that sends message_delta without a stop_reason and
// then message_stop yields exactly one finish chunk — the delta's chunk,
// with no second synthesized chunk from the fallback.
func TestTranslateAnthropicEvent_DeltaWithoutStopReasonThenStop(t *testing.T) {
	st := &anthropicStreamState{}
	deltaChunk, err := translateSSE(t, `{"type":"message_delta","delta":{},"usage":{"output_tokens":1}}`, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deltaChunk == nil || deltaChunk.Choices[0].FinishReason == nil || *deltaChunk.Choices[0].FinishReason != "stop" {
		t.Fatalf("expected finish chunk from the delta, got %+v", deltaChunk)
	}

	stopChunk, err := translateSSE(t, `{"type":"message_stop"}`, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stopChunk != nil {
		t.Errorf("expected no second finish chunk after any message_delta, got %+v", stopChunk)
	}
}

func TestTranslateAnthropicEvent_MessageStopAfterDelta(t *testing.T) {
	// Once a message_delta with stop_reason was seen, message_stop must
	// not emit a second finish chunk.
	st := &anthropicStreamState{}
	translateSSE(t, `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, st)
	chunk, err := translateSSE(t, `{"type":"message_stop"}`, st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunk != nil {
		t.Errorf("expected no finish chunk after message_delta, got %+v", chunk)
	}
}

func TestTranslateAnthropicEvent_ThinkingStream(t *testing.T) {
	deltaData := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`

	chunk, err := translateSSE(t, deltaData, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if chunk == nil {
		t.Fatal("expected non-nil chunk")
	}
	if chunk.Choices[0].Delta.ReasoningContent != "Let me think" {
		t.Errorf("expected reasoning content 'Let me think', got %q", chunk.Choices[0].Delta.ReasoningContent)
	}
}

func TestTranslateAnthropicEvent_CachedTokensInUsage(t *testing.T) {
	data := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":80}}`

	chunk, err := translateSSE(t, data, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if chunk.Usage.PromptTokensDetails == nil {
		t.Fatal("expected non-nil PromptTokensDetails")
	}
	if chunk.Usage.PromptTokensDetails.CachedTokens != 80 {
		t.Errorf("expected CachedTokens 80, got %d", chunk.Usage.PromptTokensDetails.CachedTokens)
	}
}

// ── error conversion ─────────────────────────────────────────────

func TestAnthropicErrorStatus(t *testing.T) {
	cases := map[string]int{
		"invalid_request_error":  400,
		"authentication_error":   401,
		"permission_error":       403,
		"not_found_error":        404,
		"request_too_large":      413,
		"rate_limit_error":       429,
		"overloaded_error":       529,
		"api_error":              500,
		"something_unrecognized": 500,
	}
	for errType, want := range cases {
		if got := anthropicErrorStatus(errType); got != want {
			t.Errorf("anthropicErrorStatus(%q) = %d, want %d", errType, got, want)
		}
	}
}

func TestAnthropicAPIError_MidStreamErrorEvent(t *testing.T) {
	// A mid-stream SSE `error` event surfaces through the SDK carrying the
	// (200) streaming status; the provider error type must map to the HTTP
	// status it would have carried as a non-2xx response, with the
	// provider's message preserved.
	var e anthropic.Error
	if err := e.UnmarshalJSON([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	e.StatusCode = http.StatusOK

	apiErr, ok := anthropicAPIError(&e).(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", anthropicAPIError(&e))
	}
	if apiErr.StatusCode != 529 {
		t.Errorf("expected status 529 for overloaded_error, got %d", apiErr.StatusCode)
	}
	if apiErr.Code != "overloaded_error" {
		t.Errorf("expected code 'overloaded_error', got %q", apiErr.Code)
	}
	if apiErr.Message != "Overloaded" {
		t.Errorf("expected message 'Overloaded', got %q", apiErr.Message)
	}
}

func TestAnthropicAPIError_Non2xxResponse(t *testing.T) {
	// A non-2xx response keeps the real HTTP status; the provider's error
	// type and message map into the structured APIError fields.
	var e anthropic.Error
	if err := e.UnmarshalJSON([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`)); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	e.StatusCode = http.StatusTooManyRequests

	apiErr, ok := anthropicAPIError(&e).(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", anthropicAPIError(&e))
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", apiErr.StatusCode)
	}
	if apiErr.Code != "rate_limit_error" {
		t.Errorf("expected code 'rate_limit_error', got %q", apiErr.Code)
	}
	if apiErr.Message != "Rate limited" {
		t.Errorf("expected message 'Rate limited', got %q", apiErr.Message)
	}
}

func TestAnthropicAPIError_PassesThroughUnknownErrors(t *testing.T) {
	sentinel := errors.New("connection reset")
	if got := anthropicAPIError(sentinel); got != sentinel {
		t.Errorf("expected non-SDK errors to pass through unchanged, got %v", got)
	}
}

// ── end-to-end streaming (httptest) ───────────────────────────────

// sseServer serves a canned SSE body for one POST and returns the client.
func sseServer(t *testing.T, body string) (*httptest.Server, *AnthropicClient) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, NewAnthropicClient(srv.URL, "sk-test", 30)
}

func drainStream(chunks <-chan StreamChunk, errs <-chan error) (string, *string, error) {
	var content string
	var finishReason *string
	var streamErr error
	for chunks != nil || errs != nil {
		select {
		case ch, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			for _, c := range ch.Choices {
				content += c.Delta.Content
				if c.FinishReason != nil {
					fr := *c.FinishReason
					finishReason = &fr
				}
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				streamErr = err
			}
		}
	}
	return content, finishReason, streamErr
}

// TestAnthropicSendStream_MidStreamErrorSurfaces pins that a provider
// `error` event ends the stream with the real structured APIError instead
// of a bare channel close that callers would misread as a premature close.
func TestAnthropicSendStream_MidStreamErrorSurfaces(t *testing.T) {
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"GLM-5.3"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Thinking"}}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"

	_, client := sseServer(t, body)
	chunks, errs := client.SendStream(context.Background(), types.ChatRequest{
		Model:     "GLM-5.3",
		MaxTokens: 64,
		Messages:  []types.Message{{Role: "user", Content: "hi"}},
	})

	content, finishReason, streamErr := drainStream(chunks, errs)
	if streamErr == nil {
		t.Fatal("expected a mid-stream error, got nil")
	}
	var apiErr *APIError
	if !errors.As(streamErr, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", streamErr, streamErr)
	}
	if apiErr.StatusCode != 529 || apiErr.Code != "overloaded_error" {
		t.Errorf("got status %d code %q, want 529 overloaded_error", apiErr.StatusCode, apiErr.Code)
	}
	if finishReason != nil {
		t.Errorf("expected no finish reason on error, got %q", *finishReason)
	}
	if !strings.Contains(content, "Thinking") {
		t.Errorf("expected partial content before the error, got %q", content)
	}
}

// TestAnthropicSendStream_ProxyOmitsMessageDelta pins the message_stop
// fallback: a proxy that never sends message_delta still yields a finish
// reason so the assembled response is not discarded as a premature close.
func TestAnthropicSendStream_ProxyOmitsMessageDelta(t *testing.T) {
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"GLM-5.3"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"all done"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	_, client := sseServer(t, body)
	chunks, errs := client.SendStream(context.Background(), types.ChatRequest{
		Model:     "GLM-5.3",
		MaxTokens: 64,
		Messages:  []types.Message{{Role: "user", Content: "hi"}},
	})

	content, finishReason, streamErr := drainStream(chunks, errs)
	if streamErr != nil {
		t.Fatalf("unexpected error: %v", streamErr)
	}
	if content != "all done" {
		t.Errorf("content = %q, want %q", content, "all done")
	}
	if finishReason == nil || *finishReason != "stop" {
		t.Fatalf("expected synthesized finish reason 'stop', got %v", finishReason)
	}
}

// ── review regression tests ──────────────────────────────────────

// messageServer serves a canned Messages API response for one POST and
// records the request headers.
func messageServer(t *testing.T) (*httptest.Server, *struct {
	Authorization string
	APIKey        string
	Body          map[string]any
}) {
	t.Helper()
	capture := &struct {
		Authorization string
		APIKey        string
		Body          map[string]any
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.Authorization = r.Header.Get("Authorization")
		capture.APIKey = r.Header.Get("x-api-key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capture.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m1","type":"message","role":"assistant","model":"claude-3.5-sonnet","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

// TestAnthropicSend_IgnoresEnvCredentials pins that a stray
// ANTHROPIC_AUTH_TOKEN (or other SDK default credential source) never
// reaches the endpoint: yaah's configured API key is the only credential
// sent. Without WithoutEnvironmentDefaults the SDK's default chain would
// attach the env bearer token as an Authorization header alongside yaah's
// x-api-key.
func TestAnthropicSend_IgnoresEnvCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-secret-token")
	t.Setenv("ANTHROPIC_API_KEY", "env-secret-api-key")

	srv, capture := messageServer(t)
	client := NewAnthropicClient(srv.URL, "sk-test", 30)

	resp, err := client.Send(context.Background(), types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 64,
		Messages:  []types.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("expected content 'ok', got %q", resp.Choices[0].Message.Content)
	}
	if capture.Authorization != "" {
		t.Errorf("Authorization header must not be sent (env credentials must be ignored), got %q", capture.Authorization)
	}
	if capture.APIKey != "sk-test" {
		t.Errorf("expected x-api-key 'sk-test', got %q", capture.APIKey)
	}
}

// TestAnthropicSend_NoSDKNonStreamingTimeoutPolicy pins that the SDK's
// non-streaming policy never overrides yaah's timeout configuration.
// Messages.New otherwise caps calls at ten minutes and rejects requests
// whose max_tokens implies a longer call ("streaming is required") —
// breaking timeout=0 (unlimited) and large non-streaming token budgets
// that the previous transport allowed.
func TestAnthropicSend_NoSDKNonStreamingTimeoutPolicy(t *testing.T) {
	srv, _ := messageServer(t)

	// timeout=0 means unlimited; MaxTokens 32768 would make the SDK's
	// calculated policy reject the request client-side before the fix.
	client := NewAnthropicClient(srv.URL, "sk-test", 0)

	resp, err := client.Send(context.Background(), types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 32768,
		Messages:  []types.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("large non-streaming budget with unlimited timeout must not be rejected: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("expected content 'ok', got %q", resp.Choices[0].Message.Content)
	}
}

// TestAnthropicToolDescription_OmittedWhenEmpty pins the wire shape for
// descriptionless tools: the description field must be omitted entirely,
// matching the pre-SDK wire — never serialized as an empty string, which
// Anthropic-compatible endpoints may reject.
func TestAnthropicToolDescription_OmittedWhenEmpty(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Tools: []types.ToolDef{
			{Type: "function", Function: types.ToolFn{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
		},
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	body, err := json.Marshal(lowerAnthropicRequest(req, "auto"))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var wire struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if len(wire.Tools) != 1 {
		t.Fatalf("expected 1 tool on the wire, got %d", len(wire.Tools))
	}
	if _, present := wire.Tools[0]["description"]; present {
		t.Errorf("description must be omitted for descriptionless tools, got %v", wire.Tools[0]["description"])
	}
}

func TestAnthropicStreamUsageExcludesCacheFromPrompt(t *testing.T) {
	chunk, err := translateSSE(t, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"claude","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":0}}}`, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("translateSSE() error: %v", err)
	}
	if chunk == nil || chunk.Usage == nil {
		t.Fatal("message_start did not emit usage")
	}
	if chunk.Usage.PromptIncludesCache {
		t.Fatal("Anthropic usage must not be marked PromptIncludesCache")
	}
	if chunk.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want uncached 10", chunk.Usage.PromptTokens)
	}
	if got := chunk.Usage.InclusivePromptTokens(); got != 100 {
		t.Errorf("InclusivePromptTokens() = %d, want 100", got)
	}
}

func TestAnthropicUsageIncludesCacheCreation(t *testing.T) {
	chunk, err := translateSSE(t, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"claude","usage":{"input_tokens":10,"cache_creation_input_tokens":5,"cache_read_input_tokens":90,"output_tokens":0}}}`, &anthropicStreamState{})
	if err != nil {
		t.Fatalf("translateSSE() error: %v", err)
	}
	if chunk == nil || chunk.Usage == nil {
		t.Fatal("message_start did not emit usage")
	}
	if chunk.Usage.PromptTokens != 15 {
		t.Errorf("PromptTokens = %d, want input+creation 15", chunk.Usage.PromptTokens)
	}
	if got := chunk.Usage.InclusivePromptTokens(); got != 105 {
		t.Errorf("InclusivePromptTokens() = %d, want 105", got)
	}
}
