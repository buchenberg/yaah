package yaah

import (
	"fmt"
	"os"
	"sync"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/spinner"
)

// terminalView implements agent.View for REPL terminal output.
// It owns the spinner lifecycle and records whether streaming occurred.
type terminalView struct {
	spin     *spinner.Spinner
	stopOnce sync.Once
	streamed bool
}

func newTerminalView() *terminalView {
	return &terminalView{spin: spinner.New(nil, "Thinking...")}
}

// start begins the thinking indicator. Must be called before RunPrompt.
func (v *terminalView) start() {
	fmt.Fprintln(os.Stderr)
	v.spin.Start()
}

func (v *terminalView) HandleEvent(evt agent.Event) {
	switch e := evt.(type) {
	case *agent.TokenDeltaEvent:
		v.stopOnce.Do(func() {
			v.spin.Stop()
			fmt.Fprintln(os.Stderr)
			v.streamed = true
		})
		fmt.Fprint(os.Stderr, e.Text)
	case *agent.ToolStartEvent:
		// handled on ToolEndEvent
	case *agent.ToolEndEvent:
		if e.Name == "spawn_subagent" {
			return
		}
		fmt.Fprintf(os.Stderr, "\n  tool: %s", Bold(e.Name))
		if e.Args != "" {
			args := e.Args
			if len(args) > 60 {
				args = args[:57] + "..."
			}
			fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
		}
		fmt.Fprintf(os.Stderr, " (%s)\n", Dim(formatDuration(e.Duration)))
		if e.Error != "" {
			fmt.Fprintf(os.Stderr, "    %s\n", replYellow("error: "+e.Error))
		}

	case *agent.CompactionStartedEvent:
		// No-op in terminal mode — spinner already implies activity.

	case *agent.CompactionDoneEvent:
		beforeK := float64(e.BeforeTokens) / 1000.0
		afterK := float64(e.AfterTokens) / 1000.0
		pct := e.SavingsPct * 100
		fmt.Fprintf(os.Stderr, "\n  %s %.0f%% (%.1fK → %.1fK, %s)\n",
			Dim("compacted"), pct, beforeK, afterK, Dim(e.Method))
	case *agent.SubAgentStartEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		fmt.Fprintf(os.Stderr, "\n╭─ sub-agent: %s · %s\n", Bold(label), e.Prompt)
	case *agent.SubAgentEndEvent:
		displayName := subagent.RoleDisplayName(subagent.SubAgentRole(e.Role))
		specialty := subagent.RoleSpecialty(subagent.SubAgentRole(e.Role))
		label := displayName
		if specialty != "" {
			label += " — " + specialty
		}
		status := "completed"
		if e.Error != "" {
			status = replYellow(e.Error)
		}
		modelStr := ""
		if e.Model != "" {
			modelStr = " [" + Dim(e.Model) + "]"
		}
		fmt.Fprintf(os.Stderr, "╰─ sub-agent: %s%s · %s (%s)\n", Bold(label), modelStr, status, Dim(formatDuration(e.Duration)))
	case *agent.EscalationEvent:
		severity := e.Severity
		color := replYellow
		switch severity {
		case "blocker", "critical":
			color = replRed
		case "info":
			color = Dim
		}
		fmt.Fprintf(os.Stderr, "\n  %s ESCALATION [%s] %s: %s\n", color("⚠"), severity, Bold(e.SubAgentRole), e.Summary)
		if e.Detail != "" {
			fmt.Fprintf(os.Stderr, "  %s\n", Dim(e.Detail))
		}
		if e.Suggestion != "" {
			fmt.Fprintf(os.Stderr, "  %s %s\n", Dim("→"), e.Suggestion)
		}
		fmt.Fprintln(os.Stderr)
	case *agent.DoneEvent:
		v.stopOnce.Do(v.spin.Stop)
		if v.streamed {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr)
		}
	}
}
