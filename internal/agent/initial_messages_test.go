package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// TestLoop_InitialMessages_SeedsConversation verifies that a loop with
// InitialMessages continues from the seeded history: the provider's
// first request must carry the seed messages followed by the new user
// input, with no fresh system message injected.
func TestLoop_InitialMessages_SeedsConversation(t *testing.T) {
	seed := []types.Message{
		types.SystemMsg("seed system"),
		types.UserMsg("first task"),
		{Role: "assistant", Content: "first result"},
	}
	fp := &fakeProvider{responses: []*types.ChatResponse{
		turnCkTextResponse("second result"),
	}}

	loop := &Loop{
		Provider: fp,
		Config: LoopConfig{
			SystemPrompt:    "sp",
			MaxLoopCycles:   10,
			InitialMessages: seed,
		},
	}

	resp, err := loop.Run(t.Context(), "second task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp != "second result" {
		t.Fatalf("response = %q, want second result", resp)
	}

	if len(fp.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fp.requests))
	}
	got := fp.requests[0].Messages
	wantRoles := []string{"system", "user", "assistant", "user"}
	if len(got) != len(wantRoles) {
		t.Fatalf("message count = %d, want %d", len(got), len(wantRoles))
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Errorf("message[%d].Role = %q, want %q", i, got[i].Role, role)
		}
	}
	if got[0].Content != "seed system" {
		t.Errorf("seeded system message = %q, want %q", got[0].Content, "seed system")
	}
	if got[3].Content != "second task" {
		t.Errorf("appended user message = %q, want %q", got[3].Content, "second task")
	}

	// Final state must keep the seed plus this run's exchange.
	final := loop.State.Messages
	if len(final) != 5 { // seed(3) + user + assistant
		t.Errorf("final message count = %d, want 5", len(final))
	}
}

// TestLoop_InitialMessages_IgnoredWhenStateExists verifies seeding is a
// first-Run mechanism only: an explicit State.Messages wins and the
// seed is never merged in.
func TestLoop_InitialMessages_IgnoredWhenStateExists(t *testing.T) {
	fp := &fakeProvider{responses: []*types.ChatResponse{
		turnCkTextResponse("done"),
	}}

	loop := &Loop{
		Provider: fp,
		State: LoopState{
			Messages: []types.Message{
				types.SystemMsg("live system"),
				types.UserMsg("live task"),
				{Role: "assistant", Content: "live result"},
			},
		},
		Config: LoopConfig{
			SystemPrompt:    "sp",
			MaxLoopCycles:   10,
			InitialMessages: []types.Message{types.SystemMsg("stale seed")},
		},
	}

	if _, err := loop.Run(t.Context(), "next"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := fp.requests[0].Messages
	if got[0].Content != "live system" {
		t.Errorf("first message = %q, want live system (seed must not override state)", got[0].Content)
	}
	if len(got) != 4 {
		t.Errorf("message count = %d, want 4 (state + new user)", len(got))
	}
}

// TestLoop_InitialMessages_NoSeedUnchanged verifies the default path is
// unchanged: no seed, no state → system + user, as before.
func TestLoop_InitialMessages_NoSeedUnchanged(t *testing.T) {
	fp := &fakeProvider{responses: []*types.ChatResponse{
		turnCkTextResponse("done"),
	}}

	loop := &Loop{
		Provider: fp,
		Config:   LoopConfig{SystemPrompt: "sp", MaxLoopCycles: 10},
	}

	if _, err := loop.Run(t.Context(), "work"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := fp.requests[0].Messages
	if len(got) != 2 {
		t.Fatalf("message count = %d, want 2 (system + user)", len(got))
	}
	if got[0].Role != "system" || got[0].Content != "sp" {
		t.Errorf("first message = %q/%q, want system/sp", got[0].Role, got[0].Content)
	}
}

// TestLoop_InitialMessages_SeedsEmptyNonNilState verifies seeding also
// applies when State.Messages is a non-nil empty slice, not just nil.
func TestLoop_InitialMessages_SeedsEmptyNonNilState(t *testing.T) {
	seed := []types.Message{
		types.SystemMsg("seed system"),
		types.UserMsg("first task"),
	}
	fp := &fakeProvider{responses: []*types.ChatResponse{
		turnCkTextResponse("done"),
	}}

	loop := &Loop{
		Provider: fp,
		State:    LoopState{Messages: []types.Message{}},
		Config: LoopConfig{
			SystemPrompt:    "sp",
			MaxLoopCycles:   10,
			InitialMessages: seed,
		},
	}

	if _, err := loop.Run(t.Context(), "next"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := fp.requests[0].Messages
	if len(got) != 3 {
		t.Fatalf("message count = %d, want 3 (seed system + seed user + new user)", len(got))
	}
	if got[0].Content != "seed system" {
		t.Errorf("first message = %q, want seeded system", got[0].Content)
	}
	if got[2].Role != "user" || got[2].Content != "next" {
		t.Errorf("last message = %q/%q, want user/next", got[2].Role, got[2].Content)
	}
}
