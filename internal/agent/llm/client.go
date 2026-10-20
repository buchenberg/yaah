package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/agent/errorclassify"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Client wraps a provider with streaming, retry, and fallback logic.
type Client struct {
	Provider         Provider
	FallbackProvider Provider
	// ProviderName / FallbackProviderName label the two providers in
	// error envelopes. They are swapped together with Provider and
	// FallbackProvider so a failed attempt is attributed to the provider
	// that actually served it.
	ProviderName         string
	FallbackProviderName string
	Model                string
	FallbackModel        string
	MaxRetries           int
	RetryBackoff         time.Duration
	ContextWindow        int
	SessionID            string
	OnToken              TokenCallback
	OnThinking           ThinkingCallback
	Compact              CompactFunc
	Trim                 TrimFunc
	OtelEnabled          bool
	OtelVerbose          bool
	StripReasoning       func() // called to permanently strip reasoning from session history
	replayCount          int    // tracks empty-response replays within a single Call

	// HeartbeatInterval throttles the sub-agent liveness heartbeats
	// emitted while a stream is being consumed. Zero means 5s. Tests
	// shorten it to observe per-chunk heartbeats.
	HeartbeatInterval time.Duration
	prematureCount    int // tracks premature-stream-close replays within a single Call
	dialectSeq        int // monotonic ID counter for dialect-recovered tool calls
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
	Message  types.Message
	Streamed bool
	Usage    types.Usage
	// RecoveryUsage is usage billed by an attempt that was replayed after
	// output-budget exhaustion. It is kept separate from Usage because the
	// loop must add it to session totals without replacing LastPromptTokens
	// (which describes the final attempt's conversation occupancy).
	RecoveryUsage     types.Usage
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

const (
	// outputBudgetRetryCeiling bounds the doubled cap so an already
	// generous explicit max_tokens cannot overflow into a value the model
	// rejects outright.
	outputBudgetRetryCeiling = 65536
)

// OutputBudgetProvider is implemented by providers that can report the
// output-token cap they will apply to a request. After output-budget
// exhaustion the retry ladder must double that effective cap — reading
// req.MaxTokens alone is not enough because providers fill an unset value
// with their own default internally.
//
// OutputTokenCeiling reports the largest cap the provider considers safe
// for the request, or 0 when it cannot say. Providers with a known-safe
// floor (native Anthropic) return it so the retry cannot raise the cap past
// what older models accept.
type OutputBudgetProvider interface {
	EffectiveMaxTokens(req types.ChatRequest) int
	OutputTokenCeiling(req types.ChatRequest) int
}

// raisedOutputBudget returns the output cap to use for the single
// budget-raising retry. ok is false when the effective cap is unknown or
// already at its ceiling: without a known starting point, an explicit
// floor could actually ask for fewer tokens than the attempt that failed.
func raisedOutputBudget(provider Provider, req types.ChatRequest) (int, bool) {
	current := req.MaxTokens
	if current <= 0 {
		obp, ok := provider.(OutputBudgetProvider)
		if !ok {
			return 0, false
		}
		current = obp.EffectiveMaxTokens(req)
		if current <= 0 {
			return 0, false
		}
	}
	next := current * 2
	if obp, ok := provider.(OutputBudgetProvider); ok {
		if ceiling := obp.OutputTokenCeiling(req); ceiling > 0 && next > ceiling {
			next = ceiling
		}
	}
	if next > outputBudgetRetryCeiling {
		next = outputBudgetRetryCeiling
	}
	if next <= current {
		return 0, false
	}
	return next, true
}

// swapToFallback rotates the client to its fallback provider/model once,
// swapping identity labels in lockstep. The previous provider becomes the
// fallback so a later swap can return to it.
func (c *Client) swapToFallback(req *types.ChatRequest) {
	c.Provider, c.FallbackProvider = c.FallbackProvider, c.Provider
	c.ProviderName, c.FallbackProviderName = c.FallbackProviderName, c.ProviderName
	oldModel := c.Model
	if c.FallbackModel != "" {
		c.Model = c.FallbackModel
		req.Model = c.FallbackModel
	}
	c.FallbackModel = oldModel
}

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
	respFormatDropped := false
	outputBudgetRaised := false
	var outputBudgetErr error
	c.replayCount = 0
	c.prematureCount = 0

	// Overflow-recovery compaction/trim replaces req.Messages in place;
	// the final slice is attached to every CallResult (success or error)
	// so the loop adopts it at its defined compaction point (B6).
	var compactedMessages []types.Message
	var recoveryUsage types.Usage
	compacted := false
	attach := func(r CallResult) CallResult {
		r.Compacted = compacted
		r.CompactedMessages = compactedMessages
		r.RecoveryUsage = recoveryUsage
		return r
	}

	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		// Per-attempt liveness ping marks the attempt boundary; the wait
		// ticker (startWaitHeartbeat) covers the long silent windows
		// within an attempt (yaah-1ep).
		tools.SendHeartbeat(ctx)

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
			stopHB := c.startWaitHeartbeat(ctx)
			var resp *types.ChatResponse
			resp, err = c.Provider.Send(ctx, req)
			stopHB()
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

					var recoveryErr error
					if cleaned, recovered, rejected, ok := recoverDialectToolCalls(msg.Content, &c.dialectSeq, req.Tools); ok {
						if len(rejected) > 0 {
							// Part of the model's tool-call markup could
							// not be safely synthesized (cut mid-parameter
							// or missing a required argument). Executing
							// the remainder or accepting the content as
							// the answer would both be a silent partial
							// success — fail the turn explicitly.
							recoveryErr = fmt.Errorf("model emitted tool-call markup that could not be safely recovered: %s", strings.Join(rejected, "; "))
						} else {
							msg.Content = cleaned
							msg.ToolCalls = append(msg.ToolCalls, recovered...)
						}
					}

					finish := resp.Choices[0].FinishReason
					if recoveryErr != nil {
						err = recoveryErr
					} else if finish == "content_filter" && msg.Content == "" {
						err = fmt.Errorf("response blocked by content filter")
					} else if finish == "length" && len(msg.ToolCalls) > 0 {
						err = fmt.Errorf("response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
					} else if msg.Content == "" && len(msg.ToolCalls) == 0 {
						if finish == "length" {
							err = fmt.Errorf("non-streaming response produced no content (finish_reason=length): output token budget exhausted before any content — the model may have spent max_tokens on reasoning; raise max_tokens for this request (provider default or caller cap)")
						} else {
							err = fmt.Errorf("non-streaming response produced no content (finish_reason=%s)", finish)
						}
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
			Model:       req.Model,
		}
		classified := errorclassify.Classify(err, meta)

		if isDegenerateStream(err) && c.replayCount < 2 {
			c.replayCount++
			attempt--
			continue
		}

		if isDegenerateStream(err) && c.FallbackProvider != nil && !providerSwapped {
			c.swapToFallback(&req)
			providerSwapped = true
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
			c.swapToFallback(&req)
			providerSwapped = true
			attempt--
			continue
		}

		switch {
		case classified.ShouldDropResponseFormat && req.ResponseFormat != nil && !respFormatDropped:
			// Provider rejects response_format (e.g. LM Studio only accepts
			// json_schema/text, not json_object). Drop it and replay once;
			// the prompt still requests JSON and callers parse leniently.
			// The drop is loud: a json_mode role silently producing
			// freeform output would otherwise be undiagnosable in the
			// field. The raw error, not the canned classification label,
			// carries the provider's own message — that is the part
			// worth reading when a new provider dialect shows up.
			fmt.Fprintf(os.Stderr, "warning: provider rejected response_format (%v); retrying without it — structured output now relies on the prompt alone\n",
				err)
			req.ResponseFormat = nil
			respFormatDropped = true
			// The decrement avoids consuming a retry slot — the replay is a
			// recovery, not a retry — and the continue skips the backoff
			// (which is only computed after this switch falls through).
			attempt--
			continue

		case classified.ShouldStripReasoning && stripAttempts < maxStripReasoningAttempts:
			req.Messages = stripReasoningContent(req.Messages)
			// Suppress the field on the wire, not just in the history: a
			// thinking-capable model would otherwise have it re-emitted as
			// "" and draw the same rejection again.
			req.SuppressReasoning = true
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
			c.swapToFallback(&req)
			providerSwapped = true
			attempt--
			continue

		case classified.Reason == errorclassify.ReasonOutputBudget:
			// The model spent the whole output budget on reasoning before
			// emitting content. Replaying unchanged is deterministic
			// failure, but doubling the cap once gives a reasoning-heavy
			// model the room it needed. If the raised cap is exhausted
			// too, surface the actionable diagnosis instead of looping.
			if !outputBudgetRaised {
				if next, ok := raisedOutputBudget(c.Provider, req); ok {
					// The exhausted attempt was billable even though it
					// produced no content. Preserve it for session totals;
					// the loop accounts for it without touching the context
					// occupancy derived from the final attempt.
					accumulateUsage(&recoveryUsage, result.Usage)
					outputBudgetErr = err
					req.MaxTokens = next
					outputBudgetRaised = true
					// Recovery replay, not a retry: do not consume a
					// MaxRetries slot and skip the backoff.
					attempt--
					continue
				}
			}
			if classified.Message != "" {
				return attach(result), fmt.Errorf("%s: %w", classified.Message, err)
			}
			return attach(result), err

		case classified.ShouldAbort:
			// Surface the actionable diagnosis: the classification explains
			// WHAT to change, the provider error is the cause. Returning err
			// alone discards the guidance entirely.
			if outputBudgetErr != nil {
				return attach(result), recoveryFailure(classified.Message, outputBudgetErr, req.MaxTokens, err)
			}
			if classified.Message != "" {
				return attach(result), fmt.Errorf("%s: %w", classified.Message, err)
			}
			return attach(result), err
		}

		if classified.Retryable && attempt < c.MaxRetries {
			backoff := c.RetryBackoff
			if backoff == 0 {
				backoff = time.Second
			}
			backoff *= time.Duration(1 << attempt)
			stopHB := c.startWaitHeartbeat(ctx)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				stopHB()
				return attach(result), ctx.Err()
			}
			stopHB()
		} else if !classified.Retryable {
			if outputBudgetErr != nil {
				return attach(result), recoveryFailure(classified.Message, outputBudgetErr, req.MaxTokens, err)
			}
			return attach(result), err
		}
	}
	if outputBudgetErr != nil {
		return attach(lastResult), recoveryFailure("", outputBudgetErr, req.MaxTokens, lastErr)
	}
	return attach(lastResult), lastErr
}

// heartbeatInterval returns the throttle interval for liveness heartbeats
// (default 5s when unset).
func (c *Client) heartbeatInterval() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return 5 * time.Second
}

// startWaitHeartbeat beats the sub-agent liveness heartbeat every
// heartbeatInterval until the returned stop function is called or ctx
// ends. It covers the silent waits where no stream chunk — the normal
// heartbeat source — can arrive: time-to-first-token, blocking
// non-streaming calls, and retry backoff. Without it those waits starve
// the stuck-child watchdog and healthy sub-agents are cancelled mid-call
// (yaah-1ep). stop is idempotent.
func (c *Client) startWaitHeartbeat(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(c.heartbeatInterval())
		defer t.Stop()
		for {
			select {
			case <-t.C:
				tools.SendHeartbeat(ctx)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// isDegenerateStream returns true if the error is from a model that produced
// a stream or non-streaming response with no content or tool calls.
func isDegenerateStream(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// finish_reason=length is deterministic output-budget exhaustion:
	// replaying the identical request cannot succeed. Let classification
	// surface an actionable max_tokens error instead of burning two
	// replays and a fallback swap.
	if strings.Contains(msg, "finish_reason=length") {
		return false
	}
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

// accumulateUsage folds one exhausted attempt's usage into the recovery
// total, preserving reasoning details and normalizing cache reads through
// CacheTokens so aggregate shapes are not double-counted.
func accumulateUsage(total *types.Usage, u types.Usage) {
	total.PromptTokens += u.PromptTokens
	total.CompletionTokens += u.CompletionTokens
	total.TotalTokens += u.TotalTokens
	if u.PromptIncludesCache {
		total.PromptIncludesCache = true
	}
	if cached := u.CacheTokens(); cached > 0 {
		total.CachedPromptTokens += cached
	}
	if d := u.CompletionTokensDetails; d != nil {
		if total.CompletionTokensDetails == nil {
			total.CompletionTokensDetails = &types.CompletionTokensDetails{}
		}
		total.CompletionTokensDetails.ReasoningTokens += d.ReasoningTokens
		total.CompletionTokensDetails.AudioTokens += d.AudioTokens
		total.CompletionTokensDetails.AcceptedPredictionTokens += d.AcceptedPredictionTokens
		total.CompletionTokensDetails.RejectedPredictionTokens += d.RejectedPredictionTokens
	}
}

// recoveryFailure preserves the original output-budget diagnosis when the
// raised-cap retry fails for a different reason (for example a provider
// rejecting the larger max_tokens). Without this, the actionable budget error
// is replaced by a generic "bad request format" abort.
func recoveryFailure(classifiedMsg string, budgetErr error, maxTokens int, retryErr error) error {
	if classifiedMsg == "" {
		return fmt.Errorf("%w (raised max_tokens to %d; retry failed: %w)", budgetErr, maxTokens, retryErr)
	}
	return fmt.Errorf("%s: %w (raised max_tokens to %d; retry failed: %w)",
		classifiedMsg, budgetErr, maxTokens, retryErr)
}
