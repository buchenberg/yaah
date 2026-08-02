// Package subagent formats sub-agent dispatch log lines.
package subagent

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Start returns a formatted line for when a sub-agent is dispatched.
func Start(agentType, description string) string {
	return fmt.Sprintf("%s🤖 [%s] %s%s\n",
		colors.Dim, agentType, description, colors.Reset,
	)
}

// End returns a formatted line for when a sub-agent completes.
func End(agentType, description, result string) string {
	return fmt.Sprintf("%s✅ [%s] done: %s %s%s\n",
		colors.Dim, agentType, description, colors.Reset,
		colors.Tag(colors.Accent, result),
	)
}

// Summary returns a one-line summary of a completed sub-agent call.
func Summary(agentType, description, result string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s🤖 %s %s%s → %s\n",
		colors.Dim, agentType, description, colors.Reset,
		colors.Tag(colors.Accent, result),
	))
	return b.String()
}
