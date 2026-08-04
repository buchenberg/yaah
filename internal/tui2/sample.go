package tui2

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// populateSampleData inserts example messages, tool calls, and sub-agent
// activity into the conversation view.
func (t *TUI2) populateSampleData() {
	t.addAssistantResponse("Hello! I'm yaah, your vendor-free agent harness. How can I help you today? 🐐")
	t.addUserMessage("Can you help me refactor the TUI to use tview instead of Bubbletea?")
	t.addAssistantResponse("I'll start by analyzing the current TUI structure.")

	t.AddToolStart("1", "read", `{"file": "internal/tui/tui.go"}`)
	t.AddToolEnd("1", "200 lines", "200 lines read successfully")

	t.AddToolStart("2", "grep", `{"pattern": "banner"}`)
	t.AddToolEnd("2", "5 matches", "5 matches found in 3 files")

	t.AddSubAgentStart("1", "analyst", "Finds and gathers information", "Audit TUI component structure", "claude-sonnet-4-20250514")
	t.AddSubAgentEnd("1")

	t.addAssistantResponse(
		fmt.Sprintf("After %sanalysis%s, the TUI has 13 components. "+
			"I recommend extracting 4 of them into dedicated files: banner, messages, input, and statusbar.",
			colors.TagBold(colors.Accent, ""), colors.Reset,
		),
	)
	t.addUserMessage("Sounds good! Let's start with the banner component.")
	t.addAssistantResponse("I'll create `internal/tui/banner.go` with the figlet/lolcat banner logic.")
}
