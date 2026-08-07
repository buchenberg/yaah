package tui2

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/components/question"
	"github.com/buchenberg/yaah/internal/tui2/components/sessioninfo"
	"github.com/buchenberg/yaah/internal/tui2/components/statusbar"
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
		statusbar.Update(t.StatusBar, t.lastProvider, t.lastModel, ct, m.Window)
		t.renderInfoPane()

	case *types.CtrlFallback:
		t.lastProvider = m.Provider
		t.lastModel = m.Model
		statusbar.Update(t.StatusBar, m.Provider, m.Model, t.contextTokens, t.contextWindow)
		t.renderInfoPane()
	}
}

// renderInfoPane rebuilds the info pane from live state: session info
// (provider/model/version), context token usage, and MCP server status.
func (t *TUI2) renderInfoPane() {
	var b strings.Builder

	// Session section
	b.WriteString(sessioninfo.Format(sessioninfo.Info{
		Provider: t.lastProvider,
		Model:    t.lastModel,
		Version:  t.version,
	}))
	b.WriteString("\n\n")

	// Context section
	b.WriteString(t.Theme.TagBold(t.Theme.Accent, "Context\n"))
	if t.contextWindow > 0 {
		pct := float64(t.contextTokens) * 100 / float64(t.contextWindow)
		if pct > 100 {
			pct = 100
		}
		b.WriteString(t.Theme.Tag(t.Theme.Dim,
			fmt.Sprintf("  %d / %d (%.1f%%)\n", t.contextTokens, t.contextWindow, pct)))
	} else {
		b.WriteString(t.Theme.Tag(t.Theme.Dim, "  \u2500\n"))
	}
	b.WriteString("\n")

	// MCP section (placeholder for now)
	b.WriteString(t.Theme.TagBold(t.Theme.Accent, "MCP\n"))
	b.WriteString(t.Theme.Tag(t.Theme.Dim, "  \u2500\n"))

	t.InfoPane.SetText(b.String())
}

// renderTodoPane rebuilds the dedicated task pane from live todo items.
func (t *TUI2) renderTodoPane() {
	t.TodoPane.SetText(todoview.FormatList(t.todoItems))
}
