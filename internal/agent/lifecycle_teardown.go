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
	l.broker.PublishMustDeliver(&done)
	l.brokerView.Close()
	// Mark eventing as retired: applyDefaults re-arms the broker on the
	// next Run so a reused Loop keeps delivering events (finding A4).
	l.brokerClosed = true
}

// teardown handles panic recovery, flushes the persister, closes hooks,
// and emits the session-end event.
func (l *Loop) teardown(runErr *error) {
	if r := recover(); r != nil {
		*runErr = fmt.Errorf("panic: %v", r)
	}
	l.Persister.Flush()
	l.Hooks.Close()
	reason := "completed"
	if *runErr != nil {
		reason = "error"
	}
	l.Hooks.Emit(HookEvent{
		Event:      events.SessionEnd,
		ExitReason: reason,
		Model:      l.Config.Model,
	})
}
