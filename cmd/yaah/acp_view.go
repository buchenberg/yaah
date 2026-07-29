package yaah

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
)

type acpUpdate struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Content       *acpContent    `json:"content,omitempty"`
	ToolCall      *acpToolCall   `json:"tool_call,omitempty"`
	ToolResult    *acpToolResult `json:"tool_result,omitempty"`
}

type acpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type acpToolCall struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args"`
	Status string `json:"status"`
}

type acpToolResult struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type acpSessionUpdate struct {
	SessionID string    `json:"sessionId"`
	Update    acpUpdate `json:"update"`
}

type acpSessionUpdateMsg struct {
	JSONRPC string           `json:"jsonrpc"`
	Method  string           `json:"method"`
	Params  acpSessionUpdate `json:"params"`
}

type acpView struct {
	mu        sync.Mutex
	toolIDGen atomic.Int64
	curToolID atomic.Int64
}

func newACPView() *acpView {
	return &acpView{}
}

func (v *acpView) sendTo(sessionID string, send func(string, acpUpdate), evt agent.Event) {
	var update acpUpdate
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		update = acpUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       &acpContent{Type: "text", Text: e.Text},
		}
	case *agent.ThinkingEvent:
		update = acpUpdate{
			SessionUpdate: "agent_thought_chunk",
			Content:       &acpContent{Type: "text", Text: e.Text},
		}
	case *agent.FlushEvent:
		update = acpUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       &acpContent{Type: "text", Text: "\n"},
		}
	case *agent.ToolStartEvent:
		id := v.toolIDGen.Add(1)
		v.curToolID.Store(id)
		update = acpUpdate{
			SessionUpdate: "tool_call",
			ToolCall: &acpToolCall{
				ID:     id,
				Name:   e.Name,
				Args:   e.Args,
				Status: "started",
			},
		}
	case *agent.ToolEndEvent:
		update = acpUpdate{
			SessionUpdate: "tool_result",
			ToolResult: &acpToolResult{
				ID:      v.curToolID.Load(),
				Name:    e.Name,
				Result:  e.Result,
				Error:   e.Error,
				Ms:      e.Duration.Milliseconds(),
				Summary: toolSummary(e.Name, e.Args, e.Result),
			},
		}
	case *agent.SubAgentStartEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		update = acpUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       &acpContent{Type: "text", Text: fmt.Sprintf("\n[sub-agent: %s] %s\n", label, e.Prompt)},
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
		update = acpUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       &acpContent{Type: "text", Text: fmt.Sprintf("[sub-agent: %s%s] %s\n", label, modelStr, status)},
		}
	case *agent.EscalationEvent:
		update = acpUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       &acpContent{Type: "text", Text: fmt.Sprintf("ESCALATION [%s] %s: %s", e.Severity, e.SubAgentRole, e.Summary)},
		}
	case *agent.CompactionStartedEvent:
		update = acpUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       &acpContent{Type: "text", Text: fmt.Sprintf("[compacting %d→%d tokens]", e.BeforeTokens, e.TargetTokens)},
		}
	case *agent.CompactionDoneEvent:
		pct := e.SavingsPct * 100
		update = acpUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       &acpContent{Type: "text", Text: fmt.Sprintf("[compacted %.0f%% (%d→%d)]", pct, e.BeforeTokens, e.AfterTokens)},
		}
	default:
		return
	}

	send(sessionID, update)
}
