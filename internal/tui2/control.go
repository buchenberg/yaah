package tui2

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/question"
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

	case *types.CtrlModelList:
		t.availableModels = m.Models
		t.providerNames = m.ProviderNames

	case *types.CtrlTodos:
		t.todoItems = m.Items
		t.renderInfoPane()

	case *types.CtrlContextInfo:
		t.contextTokens = m.Tokens
		t.contextWindow = m.Window
		statusbar.Update(t.StatusBar, t.lastProvider, t.lastModel, t.contextTokens, t.contextWindow)
		t.renderInfoPane()

	case *types.CtrlFallback:
		t.lastProvider = m.Provider
		t.lastModel = m.Model
		statusbar.Update(t.StatusBar, m.Provider, m.Model, t.contextTokens, t.contextWindow)
	}
}

// renderInfoPane rebuilds the right-side info pane from live state: the
// context-window usage driven by CtrlContextInfo and the todo list driven
// by CtrlTodos. It is invoked on Run() (empty initial state) and whenever
// either control message arrives, so the pane always reflects real data.
func (t *TUI2) renderInfoPane() {
	var b strings.Builder

	b.WriteString(colors.TagBold(colors.Accent, "Context\n"))
	if t.contextWindow > 0 {
		pct := float64(t.contextTokens) * 100 / float64(t.contextWindow)
		b.WriteString(colors.Tag(colors.Dim,
			fmt.Sprintf("  %d / %d (%.1f%%)\n", t.contextTokens, t.contextWindow, pct)))
	} else {
		b.WriteString(colors.Tag(colors.Dim, "  \u2500\n"))
	}
	b.WriteString("\n")

	b.WriteString(colors.TagBold(colors.Accent, "Tasks\n"))
	b.WriteString(todoview.FormatList(t.todoItems))

	t.InfoPane.SetText(b.String())
}
