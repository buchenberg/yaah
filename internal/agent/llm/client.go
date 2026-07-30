package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent/errorclassify"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

// Client wraps a provider with streaming, retry, and fallback logic.
type Client struct {
	Provider         Provider
	FallbackProvider Provider
	Model            string
	FallbackModel    string
	MaxRetries       int
	RetryBackoff     time.Duration
	ContextWindow    int
	SessionID        string
	OnToken          TokenCallback
	OnThinking       ThinkingCallback
	Compact          CompactFunc
	Trim             TrimFunc
	OtelEnabled      bool
	OtelVerbose      bool
	StripReasoning   func() // called to permanently strip reasoning from session history
	replayCount      int    // tracks empty-response replays within a single Call
	dsmlSeq          int    // monotonic ID counter for DSML-recovered tool calls
}

// CallResult holds the outcome of a single LLM call. Response metadata
// (FinishReason, ResponseModel) is kept separate from the message so it
// never pollutes the conversation history or gets persisted to the DB.
type CallResult struct {
	Message       types.Message
	Streamed      bool
	Usage         types.Usage
	FinishReason  string
	ResponseModel string
}

// Call sends a chat request and returns the result. It handles retries,
// provider rotation on credential errors, and context compaction on overflow.
func (c *Client) Call(ctx context.Context, req types.ChatRequest) (CallResult, error) {
	// Inject session ID into context so providers can set affinity headers.
	ctx = providers.WithSessionID(ctx, c.SessionID)

	var lastResult CallResult
	var lastErr error
	compactAttempts := 0
	providerSwapped := false
	c.replayCount = 0

	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		var result CallResult
		var err error

		if sp, ok := c.Provider.(StreamProvider); ok && c.OnToken != nil {
			var msg types.Message
			var finishReason, responseModel string
			var streamUsage types.Usage
			msg, finishReason, responseModel, streamUsage, err = c.runStream(ctx, sp, req)
			result = CallResult{
				Message:       msg,
				Streamed:      true,
				Usage:         streamUsage,
				FinishReason:  finishReason,
				ResponseModel: responseModel,
			}
		} else {
			var resp *types.ChatResponse
			resp, err = c.Provider.Send(ctx, req)
			if err == nil {
				result.Usage = captureUsage(resp)
				result.Streamed = false
				if len(resp.Choices) == 0 {
					err = fmt.Errorf("no choices in response")
				} else {
					msg := resp.Choices[0].Message
					result.FinishReason = resp.Choices[0].FinishReason
					result.ResponseModel = resp.Model

					if msg.Content == "" && msg.Refusal != "" {
						msg.Content = msg.Refusal
					}

					if msg.ReasoningContent != "" && c.OnThinking != nil {
						c.OnThinking(msg.ReasoningContent)
					}

					if cleaned, dsmlCalls, ok := parseDSMLToolCalls(msg.Content, &c.dsmlSeq); ok {
						msg.Content = cleaned
						msg.ToolCalls = append(msg.ToolCalls, dsmlCalls...)
					}

					finish := resp.Choices[0].FinishReason
					if finish == "content_filter" && msg.Content == "" {
						err = fmt.Errorf("response blocked by content filter")
					} else if finish == "length" && len(msg.ToolCalls) > 0 {
						err = fmt.Errorf("response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
					} else if msg.Content == "" && len(msg.ToolCalls) == 0 {
						err = fmt.Errorf("non-streaming response produced no content (finish_reason=%s)", finish)
					} else {
						result.Message = msg
					}
				}
			}
		}

		if err == nil {
			return result, nil
		}

		lastResult = result
		lastErr = err

		meta := errorclassify.ErrorMeta{
			StatusCode:  httpStatusCode(err),
			NumMessages: len(req.Messages),
		}
		classified := errorclassify.Classify(err, meta)

		if isDegenerateStream(err) && c.replayCount < 2 {
			c.replayCount++
			attempt--
			continue
		}

		if isDegenerateStream(err) && c.FallbackProvider != nil && !providerSwapped {
			oldProvider := c.Provider
			oldModel := c.Model
			c.Provider = c.FallbackProvider
			if c.FallbackModel != "" {
				c.Model = c.FallbackModel
				req.Model = c.FallbackModel
			}
			providerSwapped = true
			c.FallbackProvider = oldProvider
			c.FallbackModel = oldModel
			attempt--
			continue
		}

		switch {
		case classified.ShouldStripReasoning:
			req.Messages = stripReasoningContent(req.Messages)
			if c.StripReasoning != nil {
				c.StripReasoning()
			}
			c.replayCount = 0
			attempt--

		case classified.ShouldCompress && isDegenerateStream(err) && c.Trim != nil && compactAttempts < 3:
			beforeCount := len(req.Messages)
			req.Messages = c.Trim(ctx, req.Messages)
			compactAttempts++
			if len(req.Messages) < beforeCount {
				attempt--
				continue
			}

		case classified.ShouldCompress && c.Compact != nil && compactAttempts < 3:
			beforeCount := len(req.Messages)
			req.Messages = c.Compact(ctx, req.Messages, 0.4)
			compactAttempts++
			if len(req.Messages) < beforeCount {
				attempt--
				continue
			}

		case classified.ShouldRotateCred && c.FallbackProvider != nil && !providerSwapped:
			oldProvider := c.Provider
			oldModel := c.Model
			c.Provider = c.FallbackProvider
			if c.FallbackModel != "" {
				c.Model = c.FallbackModel
				req.Model = c.FallbackModel
			}
			providerSwapped = true
			c.FallbackProvider = oldProvider
			c.FallbackModel = oldModel
			attempt--
			continue

		case classified.ShouldAbort:
			return CallResult{Usage: result.Usage}, err
		}

		if classified.Retryable && attempt < c.MaxRetries {
			backoff := c.RetryBackoff
			if backoff == 0 {
				backoff = time.Second
			}
			backoff *= time.Duration(1 << attempt)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return CallResult{Usage: result.Usage}, ctx.Err()
			}
		} else if !classified.Retryable {
			return CallResult{Usage: result.Usage}, err
		}
	}
	return lastResult, lastErr
}

// httpStatusCode extracts the HTTP status code from a provider error.
func httpStatusCode(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	const prefix = "provider returned "
	for i := 0; i+len(prefix) <= len(msg); i++ {
		if msg[i:i+len(prefix)] == prefix {
			rest := msg[i+len(prefix):]
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end == 3 && rest[end] == ':' {
				var code int
				for j := 0; j < 3; j++ {
					code = code*10 + int(rest[j]-'0')
				}
				return code
			}
			break
		}
	}
	return 0
}

// isDegenerateStream returns true if the error is from a model that produced
// a stream or non-streaming response with no content or tool calls.
func isDegenerateStream(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "streamed response produced no content") ||
		strings.Contains(msg, "non-streaming response produced no content")
}

func stripReasoningContent(messages []types.Message) []types.Message {
	out := make([]types.Message, len(messages))
	for i, m := range messages {
		out[i] = m
		if m.Role == "assistant" {
			out[i].ReasoningContent = ""
		}
	}
	return out
}

// captureUsage extracts token usage from a response.
func captureUsage(resp *types.ChatResponse) types.Usage {
	return resp.Usage
}
