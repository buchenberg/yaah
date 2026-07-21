package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/agent/errorclassify"
	"github.com/buchenberg/yaah/internal/types"
)

// getAssistantMessage returns the next assistant message with retry logic.
// If the response has finish_reason="length" and tool calls, it errors rather
// than executing potentially truncated tool calls.
// Uses the errorclassify package for structured classification and recovery hints.
func (l *Loop) getAssistantMessage(ctx context.Context, req types.ChatRequest) (types.Message, bool, error) {
	var lastMsg types.Message
	var wasStreamed bool
	var lastErr error
	compactAttempts := 0
	providerSwapped := false

	for attempt := 0; attempt <= l.MaxRetries; attempt++ {
		var msg types.Message
		var streamed bool
		var err error

		if sp, ok := l.Provider.(StreamProvider); ok && l.OnToken != nil {
			msg, err = l.runStream(ctx, sp, req)
			streamed = true
		} else {
			var resp *types.ChatResponse
			resp, err = l.Provider.Send(ctx, req)
			if err == nil {
				l.captureUsage(resp)
				if len(resp.Choices) == 0 {
					err = fmt.Errorf("no choices in response")
				} else {
					msg = resp.Choices[0].Message

					// Surface model refusal as content so it is visible to the user.
					if msg.Content == "" && msg.Refusal != "" {
						msg.Content = msg.Refusal
					}

					// Fire the thinking callback for non-streaming reasoning content
					// (streaming path handles this via StreamDelta in runStream).
					if msg.ReasoningContent != "" && l.OnThinking != nil {
						l.OnThinking(msg.ReasoningContent)
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
			return msg, streamed, nil
		}

		lastMsg = msg
		wasStreamed = streamed
		lastErr = err

		// ── Classify the error with structured recovery hints ──────
		meta := errorclassify.ErrorMeta{
			StatusCode:  httpStatusCode(err),
			NumMessages: len(l.Messages),
		}
		classified := errorclassify.Classify(err, meta)

		// ── Act on recovery hints ──────────────────────────────────
		switch {
		case classified.ShouldCompress && l.ContextWindow > 0 && compactAttempts < 3:
			// Context overflow or payload too large: compact and retry.
			beforeCount := len(l.Messages)
			l.compactContext(ctx, 0.4) // aggressive 40% threshold
			compactAttempts++
			if len(l.Messages) < beforeCount {
				req.Messages = l.Messages
				attempt-- // don't count against MaxRetries
				continue
			}

		case classified.ShouldRotateCred && l.FallbackProvider != nil && !providerSwapped:
			// Auth, billing, or rate-limit: swap to fallback provider.
			oldProvider := l.Provider
			oldModel := l.Model
			l.Provider = l.FallbackProvider
			if l.FallbackModel != "" {
				l.Model = l.FallbackModel
				req.Model = l.FallbackModel
			}
			providerSwapped = true
			// Keep the old provider as fallback for the next rotation.
			l.FallbackProvider = oldProvider
			l.FallbackModel = oldModel
			attempt-- // don't count against MaxRetries
			continue

		case classified.ShouldAbort:
			// Content policy, format error: surface immediately.
			return types.Message{}, false, err
		}

		// ── Standard backoff for retryable errors ──────────────────
		if classified.Retryable && attempt < l.MaxRetries {
			backoff := l.RetryBackoff
			if backoff == 0 {
				backoff = time.Second
			}
			backoff *= time.Duration(1 << attempt)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return types.Message{}, false, ctx.Err()
			}
		} else if !classified.Retryable {
			return types.Message{}, false, err
		}
	}
	return lastMsg, wasStreamed, lastErr
}

// httpStatusCode extracts the HTTP status code from a provider error.
// Parses the common "provider returned NNN: body" format used by OpenAIClient.
func httpStatusCode(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	// Match "provider returned NNN: ..."
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
