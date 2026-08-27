package tui

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/tui/components/activity"
)

func (t *App) HandleEvent(event agent.Event) {
	switch e := event.(type) {
	case *agent.TokenDeltaEvent:
		t.tokensRx.Add(1)
		t.tokenMu.Lock()
		t.pendingTokens.WriteString(e.Text)
		first := !t.isStreaming.Swap(true)
		t.tokenMu.Unlock()
		if first {
			t.queueUpdateDrawCritical(func() {
				t.setActivity(activity.Responding, "")
			})
		}
	case *agent.ThinkingEvent:
		t.queueThinkingUpdate(e.Text)
	case *agent.FlushEvent:
		t.queueUpdate(func() {
			t.flushPendingTokens()
		})
	case *agent.ToolStartEvent:
		t.queueUpdateCritical(func() {
			t.pendingTool = e.Name
			if e.Name == "spawn_subagent" {
				return
			}
			t.setActivity(activity.Tool, e.Name)
			id := fmt.Sprintf("%d", e.ID)
			t.AddToolStart(id, e.Name, e.Args)
		})
	case *agent.ToolEndEvent:
		t.queueUpdateCritical(func() {
			t.pendingTool = ""
			if e.Name == "spawn_subagent" {
				return
			}
			id := fmt.Sprintf("%d", e.ID)
			if e.Error != "" {
				t.AddToolError(id, e.Name+" \u274C", e.Error)
			} else {
				t.AddToolEnd(id, e.Name, e.Result)
			}
			t.restoreActivity()
		})
	case *agent.SubAgentStartEvent:
		t.queueUpdateDrawCritical(func() {
			t.activeSubAgents++
			t.AddSubAgentStart(e.SubAgentID, e.Role, "", e.Prompt, e.Model)
			t.setActivity(activity.SubAgent, activity.FormatSubAgentDetail(e.Role, t.activeSubAgents))
		})
	case *agent.SubAgentEndEvent:
		t.queueUpdateDrawCritical(func() {
			if e.Error != "" {
				t.AddSubAgentError(e.SubAgentID, e.Error)
			} else {
				t.AddSubAgentEnd(e.SubAgentID)
			}
			if t.activeSubAgents > 0 {
				t.activeSubAgents--
			}
			if t.activeSubAgents > 0 {
				t.activityLine.SetState(activity.SubAgent, activity.FormatSubAgentDetail(e.Role, t.activeSubAgents))
			} else {
				t.restoreActivity()
			}
		})
	case *agent.EscalationEvent:
		t.queueUpdateCritical(func() {
			t.flushPendingTokens()
			msg := fmt.Sprintf("[%s]\u26A0 %s[-]", t.Theme.Error, e.Summary)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.markDirty()
		})
	case *agent.CompactionStartedEvent:
		t.queueUpdateCritical(func() {
			t.flushPendingTokens()
			t.compacting = true
			detail := fmt.Sprintf("%.1fK\u2192%.1fK", float64(e.BeforeTokens)/1000, float64(e.TargetTokens)/1000)
			t.setActivity(activity.Compacting, detail)
			msg := fmt.Sprintf("[%s]compacting (%d\u2192%d tokens, %s)[-]", t.Theme.Dim, e.BeforeTokens, e.TargetTokens, e.Reason)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.markDirty()
		})
	case *agent.CompactionDoneEvent:
		t.queueUpdateCritical(func() {
			t.flushPendingTokens()
			t.compacting = false
			t.restoreActivity()
			pct := e.SavingsPct * 100
			note := ""
			if e.IneffectiveNote != "" {
				note = " " + e.IneffectiveNote
			}
			msg := fmt.Sprintf("[%s]compacted %.0f%% (%.1fK \u2192 %.1fK, %s) in %.1fs%s[-]", t.Theme.Dim,
				pct, float64(e.BeforeTokens)/1000, float64(e.AfterTokens)/1000,
				e.Method, e.ElapsedSeconds, note)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.markDirty()
		})
	case *agent.DoneEvent:
		t.queueUpdateDrawCritical(func() {
			flushed := t.flushPendingTokens()
			t.pendingTool = ""
			t.activeSubAgents = 0
			t.setActivity(activity.Idle, "")
			if !flushed && e.Response != "" {
				t.addAssistantResponse(e.Response)
			}
			if e.Error != "" {
				t.showErrorDialog("Error", e.Error)
			}
			t.markDirty()

			if e.Usage.PromptTokens > 0 || e.Usage.CompletionTokens > 0 {
				t.accumulateUsage(e.Usage)
			}

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

func (t *App) flushPendingTokens() bool {
	if !t.isStreaming.Load() {
		return false
	}
	t.tokenMu.Lock()
	if t.pendingTokens.Len() == 0 {
		t.tokenMu.Unlock()
		return false
	}
	raw := t.pendingTokens.String()
	t.pendingTokens.Reset()
	t.tokenMu.Unlock()

	_, span := otel.Tracer("yaah").Start(context.Background(), "tui.flush",
		trace.WithAttributes(
			attribute.Int64("tokens_rx", t.tokensRx.Load()),
			attribute.Int64("chars_written", t.charsWritten.Load()),
			attribute.Int64("chars_rendered", t.charsRendered.Load()),
			attribute.Int("pending_len", len(raw)),
		))
	span.End()

	t.addAssistantResponse(raw)
	t.isStreaming.Store(false)
	return true
}
