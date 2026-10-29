package tui

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tui/components/activity"
	"github.com/buchenberg/yaah/internal/tui/components/backgroundjobs"
	"github.com/buchenberg/yaah/internal/tui/components/infopane"
	"github.com/buchenberg/yaah/internal/tui/components/question"
	subagent "github.com/buchenberg/yaah/internal/tui/components/subagent"
	todoview "github.com/buchenberg/yaah/internal/tui/components/todo"
)

func (t *App) handleControlMsg(msg control.Msg) {
	switch m := msg.(type) {
	case *control.Question:
		opts := make([]struct {
			Label       string
			Description string
		}, len(m.Options))
		for i, o := range m.Options {
			opts[i].Label = o.Label
			opts[i].Description = o.Description
		}
		t.setActivity(activity.Asking, "")
		t.ShowQuestion(m.Header, m.Question, opts, m.Multiple,
			func(answer question.Answer) {
				t.restoreActivity()
				m.AnswerCh <- strings.Join(answer.Selected, ", ")
			})

	case *control.Approval:
		t.setActivity(activity.Approving, "")
		t.ShowApproval(m.Name, m.Args, func(approved bool) {
			t.restoreActivity()
			m.ApproveCh <- approved
		})

	case *control.Continue:
		t.setActivity(activity.Approving, "")
		t.ShowApproval("Max iterations",
			fmt.Sprintf("The agent reached the iteration limit (%d). Continue?", m.MaxIter),
			func(approved bool) {
				t.restoreActivity()
				m.AnswerCh <- approved
			})

	case *control.ModelList:
		t.availableModels = m.Models
		t.providerNames = m.ProviderNames

	case *control.Todos:
		t.todoItems = m.Items
		t.renderTodoPane()

	case *control.ContextInfo:
		ct := m.Tokens
		if m.LastPromptTokensInclusive > 0 {
			ct = m.LastPromptTokensInclusive
		} else if m.LastPromptTokens > 0 {
			ct = m.LastPromptTokens
		}
		t.contextTokens = ct
		t.contextWindow = m.Window
		t.renderInfoPane()

	case *control.Fallback:
		t.lastProvider = m.Provider
		t.lastModel = m.Model
		t.renderInfoPane()

	case *control.Status:
		t.SetEphemeral(m.Text)

	case *control.CompactionSummary:
		// A durable conversation entry, not ephemeral status: the user
		// should be able to read what just replaced their context
		// (yaah-v378.5). Render-only — the summary is never persisted as
		// a chat message.
		t.flushPendingTokens()
		header := fmt.Sprintf("[%s]%s[-]", t.Theme.Dim, m.Header())
		if m.Summary == "" {
			header += " — no summary produced"
		}
		t.conversationLog = append(t.conversationLog, convItem{text: header})
		if m.Summary != "" {
			// The markdown renderer emits its own color tags; wrapping
			// this item in tview tags would render them literally.
			t.conversationLog = append(t.conversationLog, convItem{
				text:       m.Summary,
				isMarkdown: true,
			})
		}
		t.markDirty()

	case *control.Error:
		errText := ""
		if m.Err != nil {
			errText = m.Err.Error()
		}
		t.flushPendingTokens()
		t.pendingTool = ""
		t.setActivity(activity.Idle, "")
		t.conversationLog = append(t.conversationLog, convItem{
			text: fmt.Sprintf("[%s]error: %s[-]", t.Theme.Error, errText),
		})
		t.markDirty()
		t.showErrorDialog("Error", errText)
	}
}

// setPaneText replaces a side pane's content while keeping the vertical
// scroll position the reader was on. The horizontal offset is reset to
// zero so a pane cannot stay scrolled sideways across a refresh; the row
// is clamped by TextView.Draw when the new content is shorter.
func setPaneText(tv *tview.TextView, text string) {
	row, _ := tv.GetScrollOffset()
	tv.SetText(text)
	tv.ScrollTo(row, 0)
}

// renderInfoPane rebuilds the info pane from live state.
func (t *App) renderInfoPane() {
	promptTokens, completionTokens := t.GetCumulativeUsage()
	costEstimate := ""
	if t.lastModel != "" && (promptTokens > 0 || completionTokens > 0) {
		costEstimate = t.calculateCost(t.lastModel) + " (at current model rates)"
	}

	setPaneText(t.InfoPane, infopane.Format(infopane.State{
		Provider:      t.lastProvider,
		Model:         t.lastModel,
		Version:       t.version,
		ContextTokens: t.contextTokens,
		ContextWindow: t.contextWindow,
		McpServers:    t.McpServers,
		EphemeralMsg:  t.ephemeralMsg,
		SubAgents: infopane.SubAgentInfo{
			Enabled:     t.subAgentsEnabled,
			Provider:    t.subAgentsProvider,
			Concurrency: t.subAgentsConcurrency,
			Model:       t.subAgentsModel,
		},
		Embedding: infopane.EmbeddingInfo{
			Enabled: t.embeddingEnabled,
			Model:   t.embeddingModel,
		},
		World:            t.worldStatus,
		Pipeline:         t.middlewarePipeline,
		AgentActive:      t.agentActive,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostEstimate:     costEstimate,
	}, t.Theme))
}

// renderTodoPane rebuilds the dedicated task pane from live todo items.
// The pane is hidden when the list is empty and sized proportionally to
// the item count when active.
func (t *App) renderTodoPane() {
	text := todoview.FormatList(t.todoItems)
	setPaneText(t.TodoPane, text)
	if len(t.todoItems) == 0 {
		t.rightPane.ResizeItem(t.TodoPane, 0, 0)
	} else {
		t.rightPane.ResizeItem(t.TodoPane, len(t.todoItems)+2, 0)
	}
}

// collectSubagentBlocks returns the sub-agents shown in the dedicated pane.
func (t *App) collectSubagentBlocks() []*subagent.Block {
	return t.subAgents
}

func (t *App) renderBackgroundJobsPane() {
	subagentBlocks := t.collectSubagentBlocks()
	text := backgroundjobs.Format(subagentBlocks, t.Theme)
	setPaneText(t.BackgroundJobsPane, text)
	if text == "" {
		t.rightPane.ResizeItem(t.BackgroundJobsPane, 0, 0)
	} else {
		height := 0
		for _, b := range subagentBlocks {
			if b.S() == subagent.Active {
				height++
			}
		}
		rows := height*2 + 2 // header + 2 rows per agent (name line + task line) + border
		if rows > 8 {
			rows = 8
		}
		t.rightPane.ResizeItem(t.BackgroundJobsPane, rows, 0)
	}
}
