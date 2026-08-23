package agent

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// TestLoop_secondRunDeliversEvents pins the contract that every Run on a
// Loop delivers events to its View — including a second Run after the
// first completed. Today the broker is closed by publishDone at the end
// of the first Run and never recreated (finding A4), so events on later
// Runs are silently dropped.
func TestLoop_secondRunDeliversEvents(t *testing.T) {
	t.Skip("A4: broker not recreated across Runs — fixed in Phase 1 Task 1.3")

	fp := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "first answer"},
			FinishReason: "stop",
		}}},
		{Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "second answer"},
			FinishReason: "stop",
		}}},
	}}

	view := &recordingView{}
	loop := &Loop{
		Config: LoopConfig{
			SystemPrompt:  "You are a test bot.",
			MaxLoopCycles: 5,
		},
		Provider: fp,
		Registry: tools.NewRegistry(),
		View:     view,
	}

	if _, err := loop.Run(context.Background(), "one"); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	first := countDoneEvents(view.eventsOfType())

	if _, err := loop.Run(context.Background(), "two"); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	second := countDoneEvents(view.eventsOfType())

	if second != first+1 {
		t.Fatalf("second Run delivered %d DoneEvents after the first Run's %d — broker closed after first Run (A4)",
			second, first)
	}
}

func countDoneEvents(evs []Event) int {
	n := 0
	for _, e := range evs {
		if _, ok := e.(*DoneEvent); ok {
			n++
		}
	}
	return n
}
