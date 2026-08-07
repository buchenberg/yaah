package yaah

import (
	"fmt"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/toolfmt"
)

type acpView struct {
	toolIDGen atomic.Int64
	curToolID atomic.Int64
}

func newACPView() *acpView {
	return &acpView{}
}

func (v *acpView) sendTo(sessionID string, send func(string, acp.Update), evt agent.Event) {
	var update acp.Update
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		update = acp.Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &acp.Content{Type: "text", Text: e.Text},
		}
	case *agent.ThinkingEvent:
		update = acp.Update{
			SessionUpdate: "agent_thought_chunk",
			Content:       &acp.Content{Type: "text", Text: e.Text},
		}
	case *agent.FlushEvent:
		update = acp.Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &acp.Content{Type: "text", Text: "\n"},
		}
	case *agent.ToolStartEvent:
		id := v.toolIDGen.Add(1)
		v.curToolID.Store(id)
		update = acp.Update{
			SessionUpdate: "tool_call",
			ToolCall: &acp.ToolCall{
				ID:     id,
				Name:   e.Name,
				Args:   e.Args,
				Status: "started",
			},
		}
	case *agent.ToolEndEvent:
		update = acp.Update{
			SessionUpdate: "tool_result",
			ToolResult: &acp.ToolResult{
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
		update = acp.Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &acp.Content{Type: "text", Text: fmt.Sprintf("\n[sub-agent: %s] %s\n", label, e.Prompt)},
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
		update = acp.Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &acp.Content{Type: "text", Text: fmt.Sprintf("[sub-agent: %s%s] %s\n", label, modelStr, status)},
		}
	case *agent.EscalationEvent:
		update = acp.Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &acp.Content{Type: "text", Text: fmt.Sprintf("ESCALATION [%s] %s: %s", e.Severity, e.SubAgentRole, e.Summary)},
		}
	case *agent.CompactionStartedEvent:
		update = acp.Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &acp.Content{Type: "text", Text: fmt.Sprintf("[compacting %d→%d tokens]", e.BeforeTokens, e.TargetTokens)},
		}
	case *agent.CompactionDoneEvent:
		pct := e.SavingsPct * 100
		update = acp.Update{
			SessionUpdate: "agent_message_chunk",
			Content:       &acp.Content{Type: "text", Text: fmt.Sprintf("[compacted %.0f%% (%d→%d)]", pct, e.BeforeTokens, e.AfterTokens)},
		}
	default:
		return
	}

	send(sessionID, update)
}
