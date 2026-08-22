package tui

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent"
)

func (t *App) HandleEvent(event agent.Event) {
	switch e := event.(type) {
	case *agent.TokenDeltaEvent:
		t.tokensRx.Add(1)
		t.tokenMu.Lock()
		t.pendingTokens.WriteString(e.Text)
		t.isStreaming.Store(true)
		t.tokenMu.Unlock()
		t.thinkingInd.Hide()
	case *agent.ThinkingEvent:
		t.queueThinkingUpdate(e.Text)
	case *agent.FlushEvent:
		t.queueUpdate(func() {
			t.flushPendingTokens()
		})
	case *agent.ToolStartEvent:
		t.queueUpdateCritical(func() {
			t.thinkingInd.Hide()
			t.pendingTool = e.Name
			if e.Name == "spawn_subagent" {
				return
			}
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
		})
	case *agent.SubAgentStartEvent:
		t.queueUpdateDrawCritical(func() {
			t.AddSubAgentStart(e.SubAgentID, e.Role, "", e.Prompt, e.Model)
		})
	case *agent.SubAgentEndEvent:
		t.queueUpdateDrawCritical(func() {
			if e.Error != "" {
				t.AddSubAgentError(e.SubAgentID, e.Error)
			} else {
				t.AddSubAgentEnd(e.SubAgentID)
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
			msg := fmt.Sprintf("[%s]compacting (%d→%d tokens, %s)[-]", t.Theme.Dim, e.BeforeTokens, e.TargetTokens, e.Reason)
			t.conversationLog = append(t.conversationLog, convItem{text: msg})
			t.markDirty()
		})
	case *agent.CompactionDoneEvent:
		t.queueUpdateCritical(func() {
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
			t.markDirty()
		})
	case *agent.DoneEvent:
		t.queueUpdateDrawCritical(func() {
			flushed := t.flushPendingTokens()
			t.pendingThink = ""
			t.pendingTool = ""
			t.agentActive = false
			t.thinkingInd.Hide()
			if !flushed && e.Response != "" {
				t.addAssistantResponse(e.Response)
			}
			t.markDirty()

			// Accumulate usage tracking
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

func (t *App) HandleContextInfo(tokens, window int) {
	t.queueContextInfoUpdate(tokens, window)
}
