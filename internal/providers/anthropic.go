package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/buchenberg/yaah/internal/types"
)

// AnthropicClient talks to the Anthropic Messages API (or an
// Anthropic-compatible endpoint such as DeepSeek or ZAI) through the
// official anthropic-sdk-go. The SDK owns wire serialization and SSE
// decoding; this client owns yaah's mapping between
// types.ChatRequest/ChatResponse and the SDK's params, the StreamChunk
// contract consumed by internal/agent/llm, and APIError conversion so
// errorclassify keeps a single error currency across providers.
type AnthropicClient struct {
	client   anthropic.Client
	baseOpts []option.RequestOption
	baseURL  string

	// DefaultMaxTokens caps output tokens when a request does not set
	// its own cap. Zero means use the endpoint-aware fallback chosen by
	// lower: 4096 for api.anthropic.com (several Claude 3 models cap
	// there) and 8192 for Anthropic-compatible endpoints (DeepSeek, ZAI,
	// ...), which bill thinking tokens against max_tokens.
	DefaultMaxTokens int
}

const (
	// nativeAnthropicMaxTokens is the safe unconfigured floor for the
	// native Anthropic API: claude-3-haiku, claude-3-sonnet, and the
	// Claude 2.x line reject anything larger.
	nativeAnthropicMaxTokens = 4096
	// compatibleAnthropicMaxTokens leaves room for a reasoning preamble
	// plus an answer on Anthropic-compatible reasoning endpoints.
	compatibleAnthropicMaxTokens = 8192
)

// NewAnthropicClient creates a new client targeting the Anthropic Messages
// API at baseURL. Transport retries are disabled: yaah's classified retry
// ladder (internal/agent/llm) is the single retry owner, so retries never
// compound. timeoutSeconds has the same semantics as the hand-rolled
// client this replaces — it spans the whole request including body read;
// 0 means no timeout (slow local models), negative falls back to 120s.
func NewAnthropicClient(baseURL, apiKey string, timeoutSeconds int) *AnthropicClient {
	to := time.Duration(timeoutSeconds) * time.Second
	if timeoutSeconds < 0 {
		to = 120 * time.Second
	}
	opts := []option.RequestOption{
		// yaah is the sole credential resolver. Without this marker the
		// SDK's default chain reads ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN
		// from the environment first; a stray ANTHROPIC_AUTH_TOKEN would
		// then be sent as an Authorization header alongside yaah's x-api-key
		// to whatever endpoint yaah is configured for (DeepSeek, ZAI, ...).
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(0),
		option.WithHTTPClient(&http.Client{Timeout: to}),
		// An explicit request timeout also bypasses the SDK's
		// non-streaming policy, which otherwise caps Messages.New at ten
		// minutes and rejects requests whose max_tokens implies a longer
		// call ("streaming is required"). yaah's configured timeout —
		// including unlimited — must be the only deadline.
		option.WithRequestTimeout(anthropicRequestTimeout(to)),
	}
	return &AnthropicClient{
		client:   anthropic.NewClient(opts...),
		baseOpts: opts,
		baseURL:  baseURL,
	}
}

// anthropicRequestTimeout maps yaah's HTTP timeout to an explicit SDK
// request timeout. A non-positive configured timeout means "no timeout";
// the SDK treats an unset (zero) RequestTimeout as "apply the default
// policy", so unlimited is expressed as the largest representable
// duration (~292 years).
func anthropicRequestTimeout(to time.Duration) time.Duration {
	if to > 0 {
		return to
	}
	return time.Duration(math.MaxInt64)
}

// requestOpts returns the base options plus session-affinity headers when
// a session ID is present in ctx. The copy keeps per-request appends from
// ever aliasing baseOpts.
func (c *AnthropicClient) requestOpts(ctx context.Context) []option.RequestOption {
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return c.baseOpts
	}
	hexID := sessionAffinityID(sessionID)
	opts := make([]option.RequestOption, 0, len(c.baseOpts)+2)
	opts = append(opts, c.baseOpts...)
	opts = append(opts,
		option.WithHeader("x-session-id", hexID),
		option.WithHeader("x-session-affinity", hexID),
	)
	return opts
}

// Send translates a ChatRequest to the Anthropic Messages API and returns
// the response.
func (c *AnthropicClient) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	msg, err := c.client.Messages.New(ctx, c.lower(req), c.requestOpts(ctx)...)
	if err != nil {
		return nil, anthropicAPIError(err)
	}
	return raiseAnthropicResponse(*msg), nil
}

// lower applies the client's configured default output cap before
// translating the request. Per-request values always win so callers
// (for example context compaction) can keep their own explicit budget.
func (c *AnthropicClient) lower(req types.ChatRequest) anthropic.MessageNewParams {
	if req.MaxTokens <= 0 {
		req.MaxTokens = c.DefaultMaxTokens
		if req.MaxTokens <= 0 {
			req.MaxTokens = int(c.fallbackMaxTokens())
		}
	}
	return lowerAnthropicRequest(req)
}

// fallbackMaxTokens selects the unconfigured output cap by endpoint. The
// native API must stay at 4096 because older models reject more; the
// compatible endpoints this code path exists for need room for thinking.
func (c *AnthropicClient) fallbackMaxTokens() int64 {
	if strings.Contains(strings.ToLower(c.baseURL), "api.anthropic.com") {
		return nativeAnthropicMaxTokens
	}
	return compatibleAnthropicMaxTokens
}

// ListModels is not supported by the Anthropic Messages API.
func (c *AnthropicClient) ListModels(ctx context.Context) ([]string, error) {
	return nil, fmt.Errorf("anthropic Messages API does not support model listing")
}

// anthropicCacheControl returns the cache-control breakpoint param. The
// Messages API supports a single cache type ("ephemeral") and yaah's
// types.CacheControl carries only that type field, so every configured
// marker lowers to an ephemeral breakpoint.
func anthropicCacheControl() anthropic.CacheControlEphemeralParam {
	return anthropic.NewCacheControlEphemeralParam()
}

// anthropicTextBlock builds a text content block union with optional
// cache control.
func anthropicTextBlock(text string, cc *types.CacheControl) anthropic.ContentBlockParamUnion {
	block := anthropic.TextBlockParam{Text: text}
	if cc != nil {
		block.CacheControl = anthropicCacheControl()
	}
	return anthropic.ContentBlockParamUnion{OfText: &block}
}

// lowerAnthropicRequest converts a ChatRequest into the SDK's
// MessageNewParams. The message walk preserves the ordering invariants the
// Messages API requires:
//
//   - system messages are hoisted into the top-level system parameter
//   - pending tool results are flushed as their own user message before the
//     next assistant turn (every tool_use must be answered by tool_result
//     blocks in the immediately following user message)
//   - consecutive user messages merge into one multi-block message
func lowerAnthropicRequest(req types.ChatRequest) anthropic.MessageNewParams {
	var sysBlocks []anthropic.TextBlockParam
	var convMessages []types.Message

	for _, m := range req.Messages {
		if m.Role == "system" {
			block := anthropic.TextBlockParam{Text: m.Content}
			if m.CacheControl != nil {
				block.CacheControl = anthropicCacheControl()
			}
			sysBlocks = append(sysBlocks, block)
		} else {
			convMessages = append(convMessages, m)
		}
	}

	var msgs []anthropic.MessageParam
	var pendingToolResults []anthropic.ContentBlockParamUnion

	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			msgs = append(msgs, anthropic.NewUserMessage(pendingToolResults...))
			pendingToolResults = nil
		}
	}

	for _, m := range convMessages {
		switch m.Role {
		case "user":
			blocks := make([]anthropic.ContentBlockParamUnion, 0, 1+len(pendingToolResults))
			blocks = append(blocks, pendingToolResults...)
			pendingToolResults = nil

			if m.Content != "" {
				blocks = append(blocks, anthropicTextBlock(m.Content, m.CacheControl))
			}

			if len(msgs) > 0 && msgs[len(msgs)-1].Role == anthropic.MessageParamRoleUser {
				msgs[len(msgs)-1].Content = append(msgs[len(msgs)-1].Content, blocks...)
			} else if len(blocks) > 0 {
				msgs = append(msgs, anthropic.NewUserMessage(blocks...))
			}

		case "assistant":
			// Flush pending tool results as their own user message before
			// the next assistant turn. Anthropic requires every tool_use
			// to be answered by tool_result blocks in the *immediately
			// following* user message; deferring the flush until the next
			// user turn leaves an assistant message between the tool_use
			// and its results and the API rejects the request with 400
			// "tool_use ids were found without tool_result blocks
			// immediately after".
			flushToolResults()

			var blocks []anthropic.ContentBlockParamUnion
			// A thinking block must carry the provider's opaque signature
			// verbatim. Fabricating an empty one is rejected with "the
			// thinking mode must be passed back to the API", so omit the
			// block entirely when the signature was never captured.
			if m.ReasoningContent != "" && m.ReasoningSignature != "" {
				blocks = append(blocks, anthropic.NewThinkingBlock(m.ReasoningSignature, m.ReasoningContent))
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicTextBlock(m.Content, nil))
			}
			for _, tc := range m.ToolCalls {
				// Empty arguments omit the input field (the API treats a
				// missing input as {}); non-empty arguments pass through as
				// raw JSON exactly as the provider emitted them.
				var input any
				if tc.Function.Arguments != "" {
					input = json.RawMessage(tc.Function.Arguments)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Function.Name))
			}

			if len(blocks) > 0 {
				msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
			}

		case "tool":
			pendingToolResults = append(pendingToolResults,
				anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false))
		}
	}

	// Orphan tool results (a trailing tool turn with no following user or
	// assistant message) still need their own user message.
	flushToolResults()

	var tools []anthropic.ToolUnionParam
	for _, t := range req.Tools {
		var schema anthropic.ToolInputSchemaParam
		if len(t.Function.Parameters) > 0 {
			if err := json.Unmarshal(t.Function.Parameters, &schema); err != nil {
				schema = anthropic.ToolInputSchemaParam{}
			}
		}
		tool := &anthropic.ToolParam{
			InputSchema: schema,
			Name:        t.Function.Name,
		}
		if t.Function.Description != "" {
			tool.Description = param.NewOpt(t.Function.Description)
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: tool})
	}

	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		// Safe floor for direct callers. AnthropicClient.lower normally
		// fills this in with the endpoint-aware default (4096 native,
		// 8192 for Anthropic-compatible reasoning endpoints).
		maxTokens = nativeAnthropicMaxTokens
	}

	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    sysBlocks,
		Messages:  msgs,
		Tools:     tools,
	}
	if req.Temperature != 0 {
		params.Temperature = param.NewOpt(req.Temperature)
	}
	return params
}

// raiseAnthropicResponse converts an SDK Message into a ChatResponse.
func raiseAnthropicResponse(resp anthropic.Message) *types.ChatResponse {
	var content strings.Builder
	var reasoning strings.Builder
	var signature string
	var toolCalls []types.ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
			signature = block.Signature
		case "tool_use":
			// Validate that the tool input is valid JSON before storing.
			// Anthropic will reject requests with invalid JSON in
			// tool_use.input.
			if len(block.Input) == 0 || !json.Valid(block.Input) {
				continue
			}
			toolCalls = append(toolCalls, types.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: types.ToolCallFn{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}

	finishReason := string(resp.StopReason)
	switch finishReason {
	case "end_turn":
		finishReason = "stop"
	case "tool_use":
		finishReason = "tool_calls"
	case "max_tokens":
		finishReason = "length"
	case "stop_sequence":
		finishReason = "stop"
	}

	return &types.ChatResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Choices: []types.Choice{{
			Index: 0,
			Message: types.Message{
				Role:               "assistant",
				Content:            content.String(),
				ReasoningContent:   reasoning.String(),
				ReasoningSignature: signature,
				ToolCalls:          toolCalls,
			},
			FinishReason: finishReason,
		}},
		Usage: types.Usage{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
			TotalTokens:      int(resp.Usage.InputTokens + resp.Usage.OutputTokens),
			PromptTokensDetails: &types.PromptTokensDetails{
				CachedTokens: int(resp.Usage.CacheReadInputTokens),
			},
		},
	}
}

// anthropicAPIError converts an SDK error into the structured APIError
// consumed by errorclassify. Non-2xx responses carry the real HTTP status.
// Mid-stream SSE `error` events surface through the SDK carrying the (200)
// streaming status, so the provider error type is mapped to the status it
// would have carried as a non-2xx response — errorclassify then routes
// both delivery forms identically.
func anthropicAPIError(err error) error {
	var aerr *anthropic.Error
	if !errors.As(err, &aerr) {
		return err
	}
	status := aerr.StatusCode
	if status < 400 {
		status = anthropicErrorStatus(string(aerr.Type()))
	}
	return &APIError{
		StatusCode: status,
		Code:       string(aerr.Type()),
		Message:    anthropicErrorMessage(aerr),
	}
}

// anthropicErrorMessage extracts the human-readable message from an SDK
// error's response body, falling back to the rendered error.
func anthropicErrorMessage(aerr *anthropic.Error) string {
	if raw := aerr.RawJSON(); raw != "" {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(raw), &body) == nil {
			if body.Error.Message != "" {
				return body.Error.Message
			}
			if body.Message != "" {
				return body.Message
			}
		}
	}
	if aerr.Request != nil && aerr.Response != nil {
		return truncateRunes(aerr.Error(), 512)
	}
	msg := strings.TrimSpace(string(aerr.Type()))
	if msg == "" {
		msg = "api error"
	}
	return msg
}

// anthropicErrorStatus maps an Anthropic error type from a mid-stream
// `error` event to the HTTP status it would carry as a non-2xx response,
// so errorclassify routes both delivery forms identically.
func anthropicErrorStatus(errType string) int {
	switch errType {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return 529
	default: // api_error and unknown types
		return http.StatusInternalServerError
	}
}
