package providers

import "github.com/buchenberg/yaah/internal/types"

// wireToolCall is the provider-facing tool call shape. It omits the Index
// field which is a stream-assembly artifact that must never be serialized
// on outbound requests (providers reject unknown fields or misinterpret it).
type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function types.ToolCallFn `json:"function"`
}

// wireMessage is the provider-facing JSON shape for a single message. It
// differs from types.Message in three ways:
//
//  1. Content is *string: assistant messages with only tool_calls serialize
//     as "content": null (OpenAI spec requires null, not "").
//  2. ReasoningContent is *string: for thinking-mode providers it is always
//     present on assistant messages (even when empty); for non-thinking
//     providers it is omitted entirely.
//  3. ToolCalls use wireToolCall which strips the stream-only Index field.
//
// This is the "lowering" step — the single place that knows how yaah's
// internal conversation representation maps onto the OpenAI Chat wire format.
// Provider quirks belong here, not on types.Message.
type wireMessage struct {
	Role             string              `json:"role"`
	Content          *string             `json:"content"`
	Refusal          string              `json:"refusal,omitempty"`
	ReasoningContent *string             `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	Name             string              `json:"name,omitempty"`
	CacheControl     *types.CacheControl `json:"cache_control,omitempty"`
}

// wireRequest is the provider-facing request body. It mirrors
// types.ChatRequest but uses wireMessage for the messages array.
type wireRequest struct {
	Model           string                `json:"model"`
	Messages        []wireMessage         `json:"messages"`
	Tools           []types.ToolDef       `json:"tools,omitempty"`
	ToolChoice      any                   `json:"tool_choice,omitempty"`
	Temperature     float64               `json:"temperature,omitempty"`
	MaxTokens       int                   `json:"max_tokens,omitempty"`
	Stream          bool                  `json:"stream"`
	StreamOptions   *types.StreamOptions  `json:"stream_options,omitempty"`
	ResponseFormat  *types.ResponseFormat `json:"response_format,omitempty"`
	ReasoningEffort string                `json:"reasoning_effort,omitempty"`
}

// lowerOptions carries the switches the OpenAI-compatible lowering applies to
// one request. It is a struct rather than a row of positional booleans so that
// call sites state intent and cannot silently transpose two flags.
type lowerOptions struct {
	// ThinkingMode always emits reasoning_content on assistant messages.
	ThinkingMode bool
	// CopilotMode merges system messages into the conversation.
	CopilotMode bool
	// AllowPromotion lets conversation history promote ThinkingMode.
	AllowPromotion bool
	// CacheControl permits Anthropic-style cache_control breakpoints on the
	// outbound messages. cache_control is an Anthropic wire concept, not an
	// OpenAI one: strict compatible endpoints (Mistral, ZAI/GLM) answer 422
	// extra_forbidden when it appears. It is therefore opt-in per provider
	// (config `supports_cache_control`) and never implied by the
	// agents.default.prompt_caching knob, which only decides whether the
	// middleware stamps breakpoints — not whether the wire may carry them.
	CacheControl bool
}

// lowerMessages converts internal messages to wire format.
//
// Thinking mode is resolved from the model registry or config override.
// Additionally, if ANY assistant message in the conversation carries
// reasoning content, thinking mode is forced on for the entire request —
// this catches models not yet in the registry (e.g. new DeepSeek variants)
// and ensures reasoning_content is always present on every assistant message.
// opts.AllowPromotion gates the history-based promotion below. Config is the
// source of truth: when a per-model `thinking:` is declared, an explicit false
// must NOT be overridden just because history happens to carry reasoning.
func lowerMessages(msgs []types.Message, opts lowerOptions) []wireMessage {
	if opts.CopilotMode {
		msgs = mergeSystemMessages(msgs)
	}
	if !opts.ThinkingMode && !opts.AllowPromotion {
		// Explicit `thinking: false`. The else-branch in lowerMessage would
		// still emit any non-empty reasoning from history, so clear it on a
		// copy — config is authoritative, and sending the field to a model
		// that does not declare thinking mode is what draws "not supported".
		out := make([]types.Message, len(msgs))
		copy(out, msgs)
		for i := range out {
			out[i].ReasoningContent = ""
			out[i].ReasoningSignature = ""
		}
		msgs = out
	}
	if !opts.ThinkingMode && opts.AllowPromotion {
		for _, m := range msgs {
			if m.Role == "assistant" && m.ReasoningContent != "" {
				opts.ThinkingMode = true
				break
			}
		}
	}
	out := make([]wireMessage, len(msgs))
	for i, m := range msgs {
		out[i] = lowerMessage(m, opts)
	}
	return out
}

func mergeSystemMessages(msgs []types.Message) []types.Message {
	var out []types.Message
	sysBuf := ""
	for _, m := range msgs {
		if m.Role == "system" {
			sysBuf += m.Content + "\n\n"
			continue
		}
		if sysBuf != "" {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				out = append(out, types.UserMsg(sysBuf))
			} else {
				m.Content = sysBuf + m.Content
			}
			sysBuf = ""
		}
		out = append(out, m)
	}
	if sysBuf != "" {
		out = append(out, types.UserMsg(sysBuf))
	}
	return out
}

func lowerMessage(m types.Message, opts lowerOptions) wireMessage {
	w := wireMessage{
		Role:       m.Role,
		Refusal:    m.Refusal,
		ToolCalls:  lowerToolCalls(m.ToolCalls),
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	}
	// cache_control only reaches the wire for providers that opted in;
	// every other OpenAI-compatible endpoint would reject the unknown
	// message field outright.
	if opts.CacheControl {
		w.CacheControl = m.CacheControl
	}

	switch m.Role {
	case "assistant":
		// OpenAI spec: content must be null (not "") when the message
		// carries only tool_calls. Providers reject "" in this position.
		if m.Content != "" || len(m.ToolCalls) == 0 {
			c := m.Content
			w.Content = &c
		}

		// Thinking-mode providers require reasoning_content on every
		// assistant message. Always set it (even empty) when active.
		if opts.ThinkingMode {
			rc := m.ReasoningContent
			w.ReasoningContent = &rc
		} else if m.ReasoningContent != "" {
			rc := m.ReasoningContent
			w.ReasoningContent = &rc
		}

	default:
		c := m.Content
		w.Content = &c
	}

	return w
}

// lowerToolCalls strips the stream-assembly Index field from tool calls.
func lowerToolCalls(calls []types.ToolCall) []wireToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]wireToolCall, len(calls))
	for i, tc := range calls {
		out[i] = wireToolCall{
			ID:       tc.ID,
			Type:     tc.Type,
			Function: tc.Function,
		}
	}
	return out
}

// lowerRequest converts a types.ChatRequest into the wire-format request body,
// applying message lowering with the client's resolved options.
func lowerRequest(req types.ChatRequest, opts lowerOptions) wireRequest {
	if req.SuppressReasoning {
		// The provider rejected reasoning_content for this model. Dropping
		// thinkingMode clears the history fields, and AllowPromotion stays
		// off so lowerMessage does not re-add them as "" — an empty value is
		// still a present field, so resending it would repeat the rejection.
		opts.ThinkingMode = false
		opts.AllowPromotion = false
	}
	return wireRequest{
		Model:           req.Model,
		Messages:        lowerMessages(req.Messages, opts),
		Tools:           req.Tools,
		ToolChoice:      req.ToolChoice,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		Stream:          req.Stream,
		StreamOptions:   req.StreamOptions,
		ResponseFormat:  req.ResponseFormat,
		ReasoningEffort: req.ReasoningEffort,
	}
}
