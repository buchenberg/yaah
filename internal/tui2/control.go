package tui2

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/components/backgroundjobs"
	"github.com/buchenberg/yaah/internal/tui2/components/infopane"
	"github.com/buchenberg/yaah/internal/tui2/components/question"
	subagent "github.com/buchenberg/yaah/internal/tui2/components/subagent"
	todoview "github.com/buchenberg/yaah/internal/tui2/components/todo"
	"github.com/buchenberg/yaah/internal/types"
)

func (t *TUI2) handleControlMsg(msg types.CtrlMsg) {
	switch m := msg.(type) {
	case *types.CtrlQuestion:
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
				m.AnswerCh <- answer.Selected[0]
			})

	case *types.CtrlApproval:
		t.ShowApproval(m.Name, m.Args, func(approved bool) {
			m.ApproveCh <- approved
		})

	case *types.CtrlContinue:
		t.ShowApproval("Max iterations",
			fmt.Sprintf("The agent reached the iteration limit (%d). Continue?", m.MaxIter),
			func(approved bool) {
				m.AnswerCh <- approved
			})

	case *types.CtrlModelList:
		t.availableModels = m.Models
		t.providerNames = m.ProviderNames

	case *types.CtrlTodos:
		t.todoItems = m.Items
		t.renderTodoPane()

	case *types.CtrlContextInfo:
		ct := m.Tokens
		if m.LastPromptTokens > 0 {
			ct = m.LastPromptTokens
		}
		t.contextTokens = ct
		t.contextWindow = m.Window
		t.renderInfoPane()

	case *types.CtrlFallback:
		t.lastProvider = m.Provider
		t.lastModel = m.Model
		t.renderInfoPane()
	}
}

// renderInfoPane rebuilds the info pane from live state.
func (t *TUI2) renderInfoPane() {
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
		Pipeline:    t.middlewarePipeline,
		AgentActive: t.agentActive,
	}, t.Theme))
}

// renderTodoPane rebuilds the dedicated task pane from live todo items.
// The pane is hidden when the list is empty and sized proportionally to
// the item count when active.
func (t *TUI2) renderTodoPane() {
	text := todoview.FormatList(t.todoItems)
	t.TodoPane.SetText(text)
	if len(t.todoItems) == 0 {
		t.rightPane.ResizeItem(t.TodoPane, 0, 0)
	} else {
		t.rightPane.ResizeItem(t.TodoPane, len(t.todoItems)+2, 0)
	}
}

func (t *TUI2) renderBackgroundJobsPane() {
	text := backgroundjobs.Format(t.subagentBlocks, t.Theme)
	t.BackgroundJobsPane.SetText(text)
	if text == "" {
		t.rightPane.ResizeItem(t.BackgroundJobsPane, 0, 0)
	} else {
		height := 0
		for _, b := range t.subagentBlocks {
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
