package llm

import (
	"context"
	"errors"
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
	prematureCount   int    // tracks premature-stream-close replays within a single Call
	dsmlSeq          int    // monotonic ID counter for DSML-recovered tool calls
}

// CallResult holds the outcome of a single LLM call. Response metadata
// (FinishReason, ResponseModel) is kept separate from the message so it
// never pollutes the conversation history or gets persisted to the DB.
//
// Compacted/CompactedMessages carry overflow-recovery compaction (or
// trimming) performed by the CompactFunc/TrimFunc during Call back to
// the loop, so it can adopt the compacted conversation at the iteration
// boundary instead of the compaction mutating loop state from inside
// Call (review finding B6).
type CallResult struct {
	Message           types.Message
	Streamed          bool
	Usage             types.Usage
	FinishReason      string
	ResponseModel     string
	Compacted         bool
	CompactedMessages []types.Message
}

// maxStripReasoningAttempts bounds how many times Call strips reasoning
// content and replays the request. A provider that keeps rejecting the
// stripped payload falls through to the classified retry path (bounded by
// MaxRetries) instead of spinning forever (finding A2).
const maxStripReasoningAttempts = 3

// Call sends a chat request and returns the result. It handles retries,
// provider rotation on credential errors, and context compaction on overflow.
func (c *Client) Call(ctx context.Context, req types.ChatRequest) (CallResult, error) {
	// Inject session ID into context so providers can set affinity headers.
	ctx = providers.WithSessionID(ctx, c.SessionID)

	var lastResult CallResult
	var lastErr error
	compactAttempts := 0
	stripAttempts := 0
	providerSwapped := false
	c.replayCount = 0
	c.prematureCount = 0

	// Overflow-recovery compaction/trim replaces req.Messages in place;
	// the final slice is attached to every CallResult (success or error)
	// so the loop adopts it at its defined compaction point (B6).
	var compactedMessages []types.Message
	compacted := false
	attach := func(r CallResult) CallResult {
		r.Compacted = compacted
		r.CompactedMessages = compactedMessages
		return r
	}

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
			return attach(result), nil
		}

		lastResult = result
		lastErr = err

		// StatusCode is extracted inside Classify from typed provider
		// errors (providers.APIError); string parsing is gone.
		meta := errorclassify.ErrorMeta{
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

		// Premature stream close: the connection was cut before the
		// finish_reason chunk, so the partial response was discarded.
		// Replay the unchanged request immediately (a fresh connection
		// usually succeeds), then rotate to the fallback provider once —
		// the same ladder as degenerate streams. Remaining failures fall
		// through to the classified retry path below.
		if errors.Is(err, ErrPrematureStreamClose) && c.prematureCount < 2 {
			c.prematureCount++
			attempt--
			continue
		}
		if errors.Is(err, ErrPrematureStreamClose) && c.FallbackProvider != nil && !providerSwapped {
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
		case classified.ShouldStripReasoning && stripAttempts < maxStripReasoningAttempts:
			req.Messages = stripReasoningContent(req.Messages)
			if c.StripReasoning != nil {
				c.StripReasoning()
			}
			c.replayCount = 0
			stripAttempts++
			attempt--
			// Continue immediately: attempt is now negative, so the
			// exponential backoff below (1 << attempt) must not run.
			continue

		case classified.ShouldCompress && isDegenerateStream(err) && c.Trim != nil && compactAttempts < 3:
			beforeCount := len(req.Messages)
			req.Messages = c.Trim(ctx, req.Messages)
			compacted = true
			compactedMessages = req.Messages
			compactAttempts++
			if len(req.Messages) < beforeCount {
				attempt--
				continue
			}

		case classified.ShouldCompress && c.Compact != nil && compactAttempts < 3:
			beforeCount := len(req.Messages)
			req.Messages = c.Compact(ctx, req.Messages, 0.4)
			compacted = true
			compactedMessages = req.Messages
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
			return attach(CallResult{Usage: result.Usage}), err
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
				return attach(CallResult{Usage: result.Usage}), ctx.Err()
			}
		} else if !classified.Retryable {
			return attach(CallResult{Usage: result.Usage}), err
		}
	}
	return attach(lastResult), lastErr
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
