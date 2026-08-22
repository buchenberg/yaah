// Package backgroundjobs renders running background sub-agents in a
// compact side-pane format.
package backgroundjobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/buchenberg/yaah/internal/tui/components/subagent"
)

// Format returns a compact listing of running background sub-agents.
// Returns an empty string when there are no active jobs.
func Format(blocks []*subagent.Block, th *colors.Theme) string {
	var running []*subagent.Block
	for _, b := range blocks {
		if b.S() == subagent.Active {
			running = append(running, b)
		}
	}
	if len(running) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, b := range running {
		hex := th.RoleHex(b.Role())
		sb.WriteString(fmt.Sprintf("  %s🤖 %s%s %s· %s%s",
			th.ColorTag(hex), b.DisplayName(), th.ResetTag(),
			th.SecondaryTag(), b.Elapsed().Truncate(time.Second).String(), th.ResetTag()))
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("    %s· %s%s",
			th.SecondaryTag(), b.Task(), th.ResetTag()))
		sb.WriteString("\n")
	}
	return sb.String()
}
