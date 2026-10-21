package acp

import (
	"context"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/todo"
)

// forwardCtrl translates control-channel messages into session/update
// notifications. It runs for the lifetime of one prompt: the ctrl
// channel drains concurrently while the run is active (todo writes use
// blocking sends), then a close of the flush channel delivers one
// final deterministic batch of queued messages before the
// session/prompt response.
//
// ctrlDone is closed when goroutine work is fully finished — either
// after the post-run flush batch was delivered, or on cancellation —
// so the run goroutine can join before answering the prompt.
//
// Continue and question policies are governed by the Server's
// AutoContinue and AutoAnswerQuestions fields. The question tool is
// routed through the QuestionTool.Handler override in server.go, so
// there is deliberately no control.Question path here.
func (s *Server) forwardCtrl(
	ctx context.Context,
	ch <-chan control.Msg,
	sessionID string,
	send updateSender,
	flush <-chan struct{},
	ctrlDone chan<- struct{},
) {
	defer close(ctrlDone)
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !forwardOne(msg, sessionID, send, s, ch) {
				return
			}
		case <-flush:
			// RunPrompt has returned: no more producers. Drain whatever
			// is already queued (ignoring ctx so the prompt response is
			// the very last message the client sees) and finish.
			for {
				select {
				case msg, ok := <-ch:
					if !ok {
						return
					}
					if !forwardOne(msg, sessionID, send, s, ch) {
						return
					}
				default:
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// forwardOne translates a single control message, reporting idle-exit
// conditions as false. The channel is passed to drain the Done
// sentinel's remaining queue in place.
func forwardOne(msg control.Msg, sessionID string, send updateSender, s *Server, ch <-chan control.Msg) bool {
	switch m := msg.(type) {
	case *control.Status:
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(m.Text),
		})
	case *control.Error:
		if s.logf != nil && m.Err != nil {
			s.logf("control error: %v", m.Err)
		}
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(text("error: %v", m.Err)),
		})
	case *control.Fallback:
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(text("fallback to %s/%s", m.Provider, m.Model)),
		})
	case *control.ContextInfo:
		if m.Window > 0 {
			used := m.LastPromptTokensInclusive
			if used == 0 {
				used = m.Tokens
			}
			send(sessionID, UsageUpdate{
				SessionUpdate: UpdateUsageUpdate,
				Used:          used,
				Size:          m.Window,
			})
		}
	case *control.Todos:
		if len(m.Items) > 0 {
			send(sessionID, PlanUpdate{
				SessionUpdate: UpdatePlan,
				Entries:       planEntries(m.Items),
			})
		}
	case *control.Continue:
		send(sessionID, ChunkUpdate{
			SessionUpdate: UpdateAgentMessageChunk,
			Content:       textContent(text("Max iterations (%d) reached — continuing.", m.MaxIter)),
		})
		if s.AutoContinue && m.AnswerCh != nil {
			select {
			case m.AnswerCh <- true:
			default:
			}
		}
	case *control.Done:
		// Session closing. The queue is producerless past this point;
		// drain it and stop.
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// planEntries maps todo items onto ACP plan entries. todo statuses
// outside the ACP set (cancelled) clamp to pending so the entry stays
// visible; priority passthrough matches the ACP enum exactly.
func planEntries(items []todo.Item) []PlanEntry {
	entries := make([]PlanEntry, 0, len(items))
	for _, it := range items {
		status := it.Status
		switch status {
		case PlanPending, PlanInProgress, PlanCompleted:
		default:
			status = PlanPending
		}
		entries = append(entries, PlanEntry{
			Content:  it.Content,
			Priority: it.Priority,
			Status:   status,
		})
	}
	return entries
}
