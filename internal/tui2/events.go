package tui2

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/agent"
)

func (t *TUI2) HandleEvent(event agent.Event) {
	switch e := event.(type) {
	case *agent.TokenDeltaEvent:
		t.App.QueueUpdateDraw(func() {
			t.isStreaming.Store(true)
			t.pendingTokens += e.Text
			t.thinkingInd.Hide()
		})
	case *agent.ThinkingEvent:
		t.App.QueueUpdateDraw(func() {
			if !t.thinkingInd.Visible() {
				t.thinkingInd.Show()
			}
			t.thinkingLabel = e.Text
		})
	case *agent.FlushEvent:
		t.App.QueueUpdateDraw(func() {
			if t.isStreaming.Load() && t.pendingTokens != "" {
				t.addAssistantResponse(t.pendingTokens)
				t.pendingTokens = ""
				t.isStreaming.Store(false)
			}
		})
	case *agent.ToolStartEvent:
		t.App.QueueUpdateDraw(func() {
			t.thinkingInd.Hide()
			t.pendingTool = e.Name
			// Skip the tool block for spawn_subagent — the SubAgentStart/End
			// events render the sub-agent block instead, so showing both
			// would duplicate the entry in the conversation.
			if e.Name == "spawn_subagent" {
				return
			}
			id := fmt.Sprintf("%d", e.ID)
			t.AddToolStart(id, e.Name, e.Args)
		})
	case *agent.ToolEndEvent:
		t.App.QueueUpdateDraw(func() {
			t.pendingTool = ""
			// Skip the tool block for spawn_subagent (see ToolStartEvent).
			if e.Name == "spawn_subagent" {
				return
			}
			id := fmt.Sprintf("%d", e.ID)
			if e.Error != "" {
				t.AddToolEnd(id, e.Name+" \u274C", e.Error)
			} else {
				t.AddToolEnd(id, e.Name, e.Result)
			}
		})
	case *agent.SubAgentStartEvent:
		t.App.QueueUpdateDraw(func() {
			t.AddSubAgentStart(e.Role, e.Role, "", e.Prompt, e.Model)
		})
	case *agent.SubAgentEndEvent:
		t.App.QueueUpdateDraw(func() {
			if e.Error != "" {
				t.AddSubAgentError(e.Role, e.Error)
			} else {
				t.AddSubAgentEnd(e.Role)
			}
		})
	case *agent.EscalationEvent:
		t.App.QueueUpdateDraw(func() {
			t.plainMessages = append(t.plainMessages,
				fmt.Sprintf("[#ff5555]\u26A0 %s[-]", e.Summary))
			t.refreshMessages()
		})
	case *agent.CompactionStartedEvent:
		t.App.QueueUpdateDraw(func() {
			t.compacting = true
		})
	case *agent.CompactionDoneEvent:
		t.App.QueueUpdateDraw(func() {
			t.compacting = false
		})
	case *agent.DoneEvent:
		t.App.QueueUpdateDraw(func() {
			if t.isStreaming.Load() && t.pendingTokens != "" {
				t.addAssistantResponse(t.pendingTokens)
			}
			t.isStreaming.Store(false)
			t.pendingTokens = ""
			t.pendingThink = ""
			t.pendingTool = ""
			t.thinkingInd.Hide()
			t.refreshMessages()
		})
	}
}

func (t *TUI2) HandleContextInfo(tokens, _ int) {
	t.App.QueueUpdateDraw(func() {
		t.contextTokens = tokens
	})
}
