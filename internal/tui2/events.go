package tui2

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent"
)

func (t *TUI2) HandleEvent(event agent.Event) {
	switch e := event.(type) {
	case *agent.TokenDeltaEvent:
		t.tokensRx.Add(1)
		t.App.QueueUpdate(func() {
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
			t.AddSubAgentStart(e.SubAgentID, e.Role, "", e.Prompt, e.Model)
		})
	case *agent.SubAgentEndEvent:
		t.App.QueueUpdateDraw(func() {
			if e.Error != "" {
				t.AddSubAgentError(e.SubAgentID, e.Error)
			} else {
				t.AddSubAgentEnd(e.SubAgentID)
			}
		})
	case *agent.EscalationEvent:
		t.App.QueueUpdateDraw(func() {
			t.flushPendingTokens()
			msg := fmt.Sprintf("[%s]\u26A0 %s[-]", t.Theme.Error, e.Summary)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.refreshMessages()
		})
	case *agent.CompactionStartedEvent:
		t.App.QueueUpdateDraw(func() {
			t.flushPendingTokens()
			t.compacting = true
			msg := fmt.Sprintf("[%s]compacting (%d→%d tokens, %s)[-]", t.Theme.Dim, e.BeforeTokens, e.TargetTokens, e.Reason)
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
			msg := fmt.Sprintf("[%s]compacted %.0f%% (%.1fK → %.1fK, %s) in %.1fs%s[-]", t.Theme.Dim,
				pct, float64(e.BeforeTokens)/1000, float64(e.AfterTokens)/1000,
				e.Method, e.ElapsedSeconds, note)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.refreshMessages()
		})
	case *agent.DoneEvent:
		t.App.QueueUpdateDraw(func() {
			flushed := t.flushPendingTokens()
			t.pendingThink = ""
			t.pendingTool = ""
			t.agentActive = false
			t.thinkingInd.Hide()
			if !flushed && e.Response != "" {
				t.addAssistantResponse(e.Response)
			}
			t.refreshMessages()

			if e.ContextWindow > 0 {
				ct := e.ContextTokens
				if e.LastPromptTokens > 0 {
					ct = e.LastPromptTokens
				}
				t.contextTokens = ct
				t.contextWindow = e.ContextWindow
			}
			t.renderInfoPane()
		})
	}
}

func (t *TUI2) flushPendingTokens() bool {
	if !t.isStreaming.Load() || t.pendingTokens.Len() == 0 {
		return false
	}
	raw := t.pendingTokens.String()

	_, span := otel.Tracer("yaah").Start(context.Background(), "tui2.flush",
		trace.WithAttributes(
			attribute.Int64("tokens_rx", t.tokensRx.Load()),
			attribute.Int64("chars_written", t.charsWritten.Load()),
			attribute.Int64("chars_rendered", t.charsRendered.Load()),
			attribute.Int("pending_len", len(raw)),
		))
	span.End()

	t.addAssistantResponse(raw)
	t.pendingTokens.Reset()
	t.isStreaming.Store(false)
	return true
}

func (t *TUI2) HandleContextInfo(tokens, window int) {
	t.App.QueueUpdateDraw(func() {
		t.contextTokens = tokens
		t.contextWindow = window
		t.renderInfoPane()
	})
}
