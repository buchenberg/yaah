package acp

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// forwardCtrl translates control-channel messages into session/update
// notifications. Question and continue policies are governed by the
// Server's AutoAnswerQuestions and AutoContinue fields.
func (s *Server) forwardCtrl(ctx context.Context, ch <-chan types.CtrlMsg, sessionID string, send func(string, Update)) {
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *types.CtrlStatus:
				send(sessionID, Update{
					SessionUpdate: "agent_message_chunk",
					Content:       &Content{Type: "text", Text: m.Text},
				})
			case *types.CtrlError:
				send(sessionID, Update{
					SessionUpdate: "agent_message_chunk",
					Content:       &Content{Type: "text", Text: fmt.Sprintf("error: %v", m.Err)},
				})
			case *types.CtrlContinue:
				// Inform the client and auto-continue.
				msg := fmt.Sprintf("Max iterations (%d) reached — continuing.", m.MaxIter)
				send(sessionID, Update{
					SessionUpdate: "agent_message_chunk",
					Content:       &Content{Type: "text", Text: msg},
				})
				if s.AutoContinue && m.AnswerCh != nil {
					select {
					case m.AnswerCh <- true:
					default:
					}
				}
			case *types.CtrlDone:
				for {
					select {
					case <-ch:
					case <-ctx.Done():
						return
					}
				}
			case *types.CtrlContextInfo:
				send(sessionID, Update{
					SessionUpdate: "agent_message_chunk",
					Content:       &Content{Type: "text", Text: fmt.Sprintf("[context: %d/%d tokens]", m.Tokens, m.Window)},
				})
			case *types.CtrlQuestion:
				// Format and send the question text.
				msg := fmt.Sprintf("❓ %s\n\n%s\n\n", m.Header, m.Question)
				for i, o := range m.Options {
					msg += fmt.Sprintf("  [%d] %s — %s\n", i+1, o.Label, o.Description)
				}
				send(sessionID, Update{
					SessionUpdate: "agent_message_chunk",
					Content:       &Content{Type: "text", Text: msg},
				})
				// Auto-answer with the first option.
				if s.AutoAnswerQuestions && m.AnswerCh != nil {
					if len(m.Options) > 0 {
						select {
						case m.AnswerCh <- m.Options[0].Label:
						default:
						}
					} else {
						select {
						case m.AnswerCh <- "":
						default:
						}
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
