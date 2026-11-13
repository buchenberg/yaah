package agent

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/types"
)

// publishDone publishes the final DoneEvent to the broker view.
func (l *Loop) publishDone(response *string, runErr *error) {
	if l.brokerView == nil {
		return
	}
	var done DoneEvent
	done.Response = *response
	if *runErr != nil {
		done.Error = (*runErr).Error()
	}
	done.ContextTokens = l.EstimatedTokens()
	done.ContextWindow = l.Config.ContextWindow
	done.LastPromptTokens = l.State.LastPromptTokens
	done.LastPromptTokensInclusive = l.State.LastPromptTokensInclusive
	done.FinishReason = l.State.LastFinishReason
	done.ResponseModel = l.State.LastResponseModel
	done.Usage = l.State.TotalTokens
	if l.State.TotalReasoningTokens > 0 {
		done.Usage.CompletionTokensDetails = &types.CompletionTokensDetails{
			ReasoningTokens: l.State.TotalReasoningTokens,
		}
	}
	if l.State.TotalCachedPromptTokens > 0 {
		done.Usage.PromptTokensDetails = &types.PromptTokensDetails{
			CachedTokens: l.State.TotalCachedPromptTokens,
		}
	}
	// Delivery guarantee for the run's terminal event (yaah-8jou.6):
	// PublishMustDeliver holds the broker's read lock and waits on this
	// subscriber up to the 50ms fan-out budget, and brokerView.Close then
	// detaches the subscription and waits for the forwarder goroutine, which
	// ranges its channel to exhaustion — buffered events still queued behind
	// the close are handed to the view before it exits. So DoneEvent is lost
	// only if the forwarder's 4096-slot buffer is full AND the forwarder is
	// wedged for the whole budget; a momentary backlog cannot drop it the
	// way a lossy Publish would.
	l.broker.PublishMustDeliver(&done)
	l.brokerView.Close()
	// Mark eventing as retired: applyDefaults re-arms the broker on the
	// next Run so a reused Loop keeps delivering events (finding A4).
	l.brokerClosed = true
}

// teardown handles panic recovery, flushes the persister, emits the
// session-end event, and closes hooks. SessionEnd must be emitted
// BEFORE Close: Close releases the hook file descriptor, and emitting
// after it landed on a nil file and was silently dropped.
func (l *Loop) teardown(runErr *error) {
	if r := recover(); r != nil {
		*runErr = fmt.Errorf("panic: %v", r)
	}
	l.Persister.Flush()
	reason := "completed"
	if *runErr != nil {
		reason = "error"
	}
	l.Hooks.Emit(HookEvent{
		Event:      events.SessionEnd,
		ExitReason: reason,
		Model:      l.Config.Model,
	})
	l.Hooks.Close()
}
