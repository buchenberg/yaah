package acp

import (
	"fmt"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/toolfmt"
)

// View translates agent events into ACP session/update payloads. It
// assigns tool-call IDs monotonically so tool_call and tool_result
// updates can be correlated.
type View struct {
	toolIDGen atomic.Int64
	curToolID atomic.Int64
}

// NewView creates a View ready to translate agent events.
func NewView() *View {
	return &View{}
}

// SendTo converts evt to an update and passes it to send, tagging it with
// the given session ID. Events with no ACP representation are dropped.
func (v *View) SendTo(sessionID string, send func(string, Update), evt agent.Event) {
	var update Update
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		update = Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &Content{Type: "text", Text: e.Text},
		}
	case *agent.ThinkingEvent:
		update = Update{
			SessionUpdate: "agent_thought_chunk",
			Content:       &Content{Type: "text", Text: e.Text},
		}
	case *agent.FlushEvent:
		update = Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &Content{Type: "text", Text: "\n"},
		}
	case *agent.ToolStartEvent:
		id := v.toolIDGen.Add(1)
		v.curToolID.Store(id)
		update = Update{
			SessionUpdate: "tool_call",
			ToolCall: &ToolCall{
				ID:     id,
				Name:   e.Name,
				Args:   e.Args,
				Status: "started",
			},
		}
	case *agent.ToolEndEvent:
		update = Update{
			SessionUpdate: "tool_result",
			ToolResult: &ToolResult{
				ID:      v.curToolID.Load(),
				Name:    e.Name,
				Result:  e.Result,
				Error:   e.Error,
				Ms:      e.Duration.Milliseconds(),
				Summary: toolfmt.Summary(e.Name, e.Args, e.Result),
			},
		}
	case *agent.SubAgentStartEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		update = Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &Content{Type: "text", Text: fmt.Sprintf("\n[sub-agent: %s] %s\n", label, e.Prompt)},
		}
	case *agent.SubAgentEndEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		status := "completed"
		if e.Error != "" {
			status = e.Error
		}
		modelStr := ""
		if e.Model != "" {
			modelStr = " [" + e.Model + "]"
		}
		update = Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &Content{Type: "text", Text: fmt.Sprintf("[sub-agent: %s%s] %s\n", label, modelStr, status)},
		}
	case *agent.EscalationEvent:
		update = Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &Content{Type: "text", Text: fmt.Sprintf("ESCALATION [%s] %s: %s", e.Severity, e.SubAgentRole, e.Summary)},
		}
	case *agent.CompactionStartedEvent:
		update = Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &Content{Type: "text", Text: fmt.Sprintf("[compacting %d→%d tokens]", e.BeforeTokens, e.TargetTokens)},
		}
	case *agent.CompactionDoneEvent:
		pct := e.SavingsPct * 100
		update = Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &Content{Type: "text", Text: fmt.Sprintf("[compacted %.0f%% (%d→%d)]", pct, e.BeforeTokens, e.AfterTokens)},
		}
	default:
		return
	}

	send(sessionID, update)
}

// ViewWithWrite wraps View and sends session/update notifications
// for each event, tagging them with the active session ID.
type ViewWithWrite struct {
	av        *View
	send      func(sessionID string, update Update)
	sessionID string
}

// NewViewWithWrite creates an agent.View that forwards translated events
// to send as session/update payloads for sessionID.
func NewViewWithWrite(send func(sessionID string, update Update), sessionID string) *ViewWithWrite {
	return &ViewWithWrite{
		av:        NewView(),
		send:      send,
		sessionID: sessionID,
	}
}

// HandleEvent implements agent.View.
func (v *ViewWithWrite) HandleEvent(evt agent.Event) {
	v.av.SendTo(v.sessionID, v.send, evt)
}

// compile-time check
var _ agent.View = (*ViewWithWrite)(nil)
