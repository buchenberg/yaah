package tui

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/control"
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
		t.ShowQuestion(m.Header, m.Question, opts, m.Multiple,
			func(answer question.Answer) {
				// Selected is nil on Esc/cancel and on an empty
				// multi-select confirm; send an empty answer instead
				// of panicking on the index.
				m.AnswerCh <- strings.Join(answer.Selected, ", ")
			})

	case *control.Approval:
		t.ShowApproval(m.Name, m.Args, func(approved bool) {
			m.ApproveCh <- approved
		})

	case *control.Continue:
		t.ShowApproval("Max iterations",
			fmt.Sprintf("The agent reached the iteration limit (%d). Continue?", m.MaxIter),
			func(approved bool) {
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
		if m.LastPromptTokens > 0 {
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

	case *control.Error:
		errText := ""
		if m.Err != nil {
			errText = m.Err.Error()
		}
		t.flushPendingTokens()
		t.pendingThink = ""
		t.pendingTool = ""
		t.agentActive = false
		if t.thinkingInd.Hide() {
			t.needsFullRender.Store(true)
		}
		t.conversationLog = append(t.conversationLog, convItem{
			text: fmt.Sprintf("[%s]error: %s[-]", t.Theme.Error, errText),
		})
		t.markDirty()
	}
}

// renderInfoPane rebuilds the info pane from live state.
func (t *App) renderInfoPane() {
	promptTokens, completionTokens := t.GetCumulativeUsage()
	costEstimate := ""
	if t.lastModel != "" && (promptTokens > 0 || completionTokens > 0) {
		costEstimate = t.calculateCost(t.lastModel) + " (at current model rates)"
	}

	t.InfoPane.SetText(infopane.Format(infopane.State{
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
	t.TodoPane.SetText(text)
	if len(t.todoItems) == 0 {
		t.rightPane.ResizeItem(t.TodoPane, 0, 0)
	} else {
		t.rightPane.ResizeItem(t.TodoPane, len(t.todoItems)+2, 0)
	}
}

// collectSubagentBlocks returns all subagent blocks from conversationLog.
func (t *App) collectSubagentBlocks() []*subagent.Block {
	var blocks []*subagent.Block
	for _, ci := range t.conversationLog {
		if ci.subBlock != nil {
			blocks = append(blocks, ci.subBlock)
		}
	}
	return blocks
}

func (t *App) renderBackgroundJobsPane() {
	subagentBlocks := t.collectSubagentBlocks()
	text := backgroundjobs.Format(subagentBlocks, t.Theme)
	t.BackgroundJobsPane.SetText(text)
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
