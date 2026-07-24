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
	replayCount      int // tracks empty-response replays within a single Call
	dsmlSeq          int // monotonic ID counter for DSML-recovered tool calls
}

// Call sends a chat request and returns the assistant message, whether it
// was streamed, the provider usage, and any error. It handles retries,
// provider rotation on credential errors, and context compaction on overflow.
func (c *Client) Call(ctx context.Context, req types.ChatRequest) (types.Message, bool, types.Usage, error) {
	// Inject session ID into context so providers can set affinity headers.
	ctx = providers.WithSessionID(ctx, c.SessionID)

	var lastMsg types.Message
	var wasStreamed bool
	var lastErr error
	var totalUsage types.Usage
	compactAttempts := 0
	providerSwapped := false
	c.replayCount = 0

	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		var msg types.Message
		var streamed bool
		var err error

		if sp, ok := c.Provider.(StreamProvider); ok && c.OnToken != nil {
			var streamUsage types.Usage
			msg, streamUsage, err = c.runStream(ctx, sp, req)
			if err == nil {
				totalUsage = streamUsage
			}
			streamed = true
		} else {
			var resp *types.ChatResponse
			resp, err = c.Provider.Send(ctx, req)
			if err == nil {
				totalUsage = captureUsage(resp)
				if len(resp.Choices) == 0 {
					err = fmt.Errorf("no choices in response")
				} else {
					msg = resp.Choices[0].Message

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
						msg = types.Message{}
					} else if finish == "length" && len(msg.ToolCalls) > 0 {
						err = fmt.Errorf("response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
						msg = types.Message{}
					} else if msg.Content == "" && len(msg.ToolCalls) == 0 {
						err = fmt.Errorf("non-streaming response produced no content (finish_reason=%s)", finish)
						msg = types.Message{}
					}
				}
			}
		}

		if err == nil {
			return msg, streamed, totalUsage, nil
		}

		lastMsg = msg
		wasStreamed = streamed
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
			return types.Message{}, false, totalUsage, err
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
				return types.Message{}, false, totalUsage, ctx.Err()
			}
		} else if !classified.Retryable {
			return types.Message{}, false, totalUsage, err
		}
	}
	return lastMsg, wasStreamed, totalUsage, lastErr
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

// captureUsage extracts token usage from a response.
func captureUsage(resp *types.ChatResponse) types.Usage {
	return resp.Usage
}
