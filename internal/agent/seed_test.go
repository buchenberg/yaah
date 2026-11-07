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
		types.SystemMsg("old env: cwd=/old"),
		types.UserMsg("do the work"),
	}
	loop := NewSubAgentLoop(nil, tools.NewEmptyRegistry(), "m", "new env: cwd=/new", SubAgentConfig{
		MaxLoopCycles: 2,
		SeedMessages:  seed,
	})

	if len(loop.State.Messages) != len(seed) {
		t.Fatalf("seeded %d messages, want %d", len(loop.State.Messages), len(seed))
	}
	// The seeded system message carries the prior run's workspace root; the
	// current prompt must win, or the model is told the wrong tree.
	if loop.State.Messages[0].Content != "new env: cwd=/new" {
		t.Errorf("seeded system message = %q, want the current prompt", loop.State.Messages[0].Content)
	}
	if loop.State.Messages[1].Content != "do the work" {
		t.Fatalf("seed was not preserved: %+v", loop.State.Messages)
	}

	seed[1].Content = "mutated"
	if loop.State.Messages[1].Content != "do the work" {
		t.Error("loop aliased the caller's seed slice; want an independent copy")
	}
}

// TestNewSubAgentLoop_EmptyPromptKeepsSeedSystem: an empty current prompt must
// not blank the role/environment context the seed already carries.
func TestNewSubAgentLoop_EmptyPromptKeepsSeedSystem(t *testing.T) {
	seed := []types.Message{types.SystemMsg("seeded sys"), types.UserMsg("do")}
	loop := NewSubAgentLoop(nil, tools.NewEmptyRegistry(), "m", "", SubAgentConfig{
		MaxLoopCycles: 2,
		SeedMessages:  seed,
	})
	if len(loop.State.Messages) != 2 {
		t.Fatalf("seeded %d messages, want 2", len(loop.State.Messages))
	}
	if loop.State.Messages[0].Content != "seeded sys" {
		t.Errorf("first message = %q, want the seeded system message", loop.State.Messages[0].Content)
	}
}

// TestNewSubAgentLoop_SeedWithoutSystemGetsOne: a seed that lacks a system
// message still gets the current prompt prepended.
func TestNewSubAgentLoop_SeedWithoutSystemGetsOne(t *testing.T) {
	loop := NewSubAgentLoop(nil, tools.NewEmptyRegistry(), "m", "current sys", SubAgentConfig{
		MaxLoopCycles: 2,
		SeedMessages:  []types.Message{types.UserMsg("do")},
	})
	if len(loop.State.Messages) != 2 {
		t.Fatalf("seeded %d messages, want 2", len(loop.State.Messages))
	}
	if loop.State.Messages[0].Role != "system" || loop.State.Messages[0].Content != "current sys" {
		t.Errorf("first message = %+v, want the current system prompt", loop.State.Messages[0])
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
