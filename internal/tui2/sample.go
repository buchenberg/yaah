package tui2

import (
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// populateSampleData inserts example messages, tool calls, and sub-agent
// activity into the conversation view.
func (t *TUI2) populateSampleData() {
	t.addAssistantResponse("Hello! I'm yaah, your vendor-free agent harness. How can I help you today? 🐐")
	t.addUserMessage("Can you help me refactor the TUI to use tview instead of Bubbletea?")
	t.addAssistantResponse("I'll start by analyzing the current TUI structure.")

	t.AddToolStart("read", `{"file": "internal/tui/tui.go"}`)
	time.Sleep(600 * time.Millisecond)
	t.AddToolEnd("read", "200 lines")

	t.AddToolStart("grep", `{"pattern": "banner"}`)
	time.Sleep(400 * time.Millisecond)
	t.AddToolEnd("grep", "5 matches")

	t.AddSubAgentStart("analyst", "Audit TUI component structure")
	time.Sleep(1 * time.Second)
	t.AddSubAgentEnd("analyst", "Audit TUI component structure", "13 components; 4 candidates for extraction")

	t.addAssistantResponse(
		fmt.Sprintf("After %sanalysis%s, the TUI has 13 components. "+
			"I recommend extracting 4 of them into dedicated files: banner, messages, input, and statusbar.",
			colors.TagBold(colors.Accent, ""), colors.Reset,
		),
	)
	t.addUserMessage("Sounds good! Let's start with the banner component.")
	t.addAssistantResponse("I'll create `internal/tui/banner.go` with the figlet/lolcat banner logic.")
}
