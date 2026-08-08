package tui2

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/tui2/components/statusbar"
)

func (t *TUI2) HandleEvent(event agent.Event) {
	switch e := event.(type) {
	case *agent.TokenDeltaEvent:
		t.App.QueueUpdateDraw(func() {
			t.isStreaming.Store(true)
			t.pendingTokens.WriteString(e.Text)
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
			t.flushPendingTokens()
		})
	case *agent.ToolStartEvent:
		t.App.QueueUpdateDraw(func() {
			t.thinkingInd.Hide()
			t.pendingTool = e.Name
			if e.Name == "spawn_subagent" {
				return
			}
			id := fmt.Sprintf("%d", e.ID)
			t.AddToolStart(id, e.Name, e.Args)
		})
	case *agent.ToolEndEvent:
		t.App.QueueUpdateDraw(func() {
			t.pendingTool = ""
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
			t.flushPendingTokens()
			msg := fmt.Sprintf("[#ff5555]\u26A0 %s[-]", e.Summary)
			t.plainMessages = append(t.plainMessages, msg)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.refreshMessages()
		})
	case *agent.CompactionStartedEvent:
		t.App.QueueUpdateDraw(func() {
			t.flushPendingTokens()
			t.compacting = true
			msg := fmt.Sprintf("[#888888]compacting (%d→%d tokens, %s)[-]", e.BeforeTokens, e.TargetTokens, e.Reason)
			t.plainMessages = append(t.plainMessages, msg)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.refreshMessages()
		})
	case *agent.CompactionDoneEvent:
		t.App.QueueUpdateDraw(func() {
			t.flushPendingTokens()
			t.compacting = false
			pct := e.SavingsPct * 100
			note := ""
			if e.IneffectiveNote != "" {
				note = " " + e.IneffectiveNote
			}
			msg := fmt.Sprintf("[#888888]compacted %.0f%% (%.1fK → %.1fK, %s) in %.1fs%s[-]",
				pct, float64(e.BeforeTokens)/1000, float64(e.AfterTokens)/1000,
				e.Method, e.ElapsedSeconds, note)
			t.plainMessages = append(t.plainMessages, msg)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.refreshMessages()
		})
	case *agent.DoneEvent:
		t.App.QueueUpdateDraw(func() {
			t.flushPendingTokens()
			t.pendingThink = ""
			t.pendingTool = ""
			t.thinkingInd.Hide()
			t.refreshMessages()

			if e.ContextWindow > 0 {
				ct := e.ContextTokens
				if e.LastPromptTokens > 0 {
					ct = e.LastPromptTokens
				}
				t.contextTokens = ct
				t.contextWindow = e.ContextWindow
				statusbar.Update(t.StatusBar, t.lastProvider, t.lastModel, ct, e.ContextWindow)
				t.renderInfoPane()
			}
		})
	}
}

func (t *TUI2) flushPendingTokens() {
	if !t.isStreaming.Load() || t.pendingTokens.Len() == 0 {
		return
	}
	t.addAssistantResponse(t.pendingTokens.String())
	t.pendingTokens.Reset()
	t.isStreaming.Store(false)
}

func (t *TUI2) HandleContextInfo(tokens, window int) {
	t.App.QueueUpdateDraw(func() {
		t.contextTokens = tokens
		t.contextWindow = window
		t.renderInfoPane()
		statusbar.Update(t.StatusBar, t.lastProvider, t.lastModel, tokens, window)
	})
}
