package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// TestNewSubAgentLoop_SeedsConversation: a continuation pre-populates the
// loop's conversation so Run appends its prompt to the prior turns instead of
// starting from a fresh system+user pair. The loop must own its copy of the
// seed so a caller mutating its slice cannot rewind the conversation.
func TestNewSubAgentLoop_SeedsConversation(t *testing.T) {
	seed := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("do the work"),
	}
	loop := NewSubAgentLoop(nil, tools.NewEmptyRegistry(), "m", "sys", SubAgentConfig{
		MaxLoopCycles: 2,
		SeedMessages:  seed,
	})

	if len(loop.State.Messages) != len(seed) {
		t.Fatalf("seeded %d messages, want %d", len(loop.State.Messages), len(seed))
	}
	if loop.State.Messages[1].Content != "do the work" {
		t.Fatalf("seed was not preserved: %+v", loop.State.Messages)
	}

	seed[1].Content = "mutated"
	if loop.State.Messages[1].Content != "do the work" {
		t.Error("loop aliased the caller's seed slice; want an independent copy")
	}
}

// TestNewSubAgentLoop_NoSeedLeavesStateEmpty pins the unchanged fresh-dispatch
// behaviour: without SeedMessages the loop starts with no conversation and
// Run builds one from its prompt.
func TestNewSubAgentLoop_NoSeedLeavesStateEmpty(t *testing.T) {
	loop := NewSubAgentLoop(nil, tools.NewEmptyRegistry(), "m", "sys", SubAgentConfig{MaxLoopCycles: 2})
	if loop.State.Messages != nil {
		t.Fatalf("unseeded loop State.Messages = %+v, want nil", loop.State.Messages)
	}
}
