package dispatch

// retained_threading_test.go covers retained-result threading: a new dispatch
// seeded from another proposal's sealed work (and optionally its conversation),
// with the source proposal left open.

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// TestRetained_ThreadingSeedsWorkspaceFromProposal: dispatch B runs its own
// role but starts from A's sealed workspace in a fresh clone; A stays open.
func TestRetained_ThreadingSeedsWorkspaceFromProposal(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	var calls int
	var firstWorkdir, secondWorkdir string
	var secondSeed []types.Message

	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			calls++
			if calls == 1 {
				firstWorkdir = params.Workdir
				writeTestFile(t, params.Workdir, "one.txt", "first\n")
				jobs.CaptureSubAgentTranscript(ctx, []types.Message{types.UserMsg("first")})
				return "first", nil
			}
			secondWorkdir = params.Workdir
			secondSeed = params.SeedMessages
			writeTestFile(t, params.Workdir, "two.txt", "second\n")
			jobs.CaptureSubAgentTranscript(ctx, []types.Message{types.UserMsg("second")})
			return "second", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	resA, err := StartRetainedRun(context.Background(), rt, "A", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("dispatch A: %v", err)
	}
	envA := decodeProposalEnvelope(t, resA)
	if envA.Status != "proposal" {
		t.Fatalf("dispatch A status = %q (%s)", envA.Status, envA.Error)
	}

	resB, err := StartRetainedRun(context.Background(), rt, "B", SubAgentParams{
		Role:     "tester",
		SeedFrom: envA.ProposalID,
		SeedMode: jobs.SeedModeWorkspace,
	}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("threaded dispatch B: %v", err)
	}
	envB := decodeProposalEnvelope(t, resB)
	if envB.Status != "proposal" {
		t.Fatalf("dispatch B status = %q (%s)", envB.Status, envB.Error)
	}
	if firstWorkdir == "" || secondWorkdir == "" || firstWorkdir == secondWorkdir {
		t.Fatalf("threading must run in a fresh clone: first=%q second=%q", firstWorkdir, secondWorkdir)
	}
	if len(secondSeed) != 0 {
		t.Errorf("workspace seed carried %d messages; want none", len(secondSeed))
	}
	for _, want := range []string{"one.txt", "two.txt"} {
		if !slices.Contains(envB.Files, want) {
			t.Errorf("threaded proposal files %v missing %q", envB.Files, want)
		}
	}
	if _, err := rt.Proposals.get(envA.ProposalID); err != nil {
		t.Errorf("threading consumed the source proposal: %v", err)
	}
}

// TestRetained_ThreadingConversationIsSameRoleOnly: conversation seeding carries
// the source transcript for the same role and refuses a different role (whose
// system prompt would otherwise leak into the new loop).
func TestRetained_ThreadingConversationIsSameRoleOnly(t *testing.T) {
	fx, repo, rootID := newSessionFixtures(t)
	transcript := []types.Message{types.SystemMsg("dev sys"), types.UserMsg("do it")}
	var calls int
	var gotSeed []types.Message

	rt := RetainedRuntime{
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			calls++
			if calls == 1 {
				jobs.CaptureSubAgentTranscript(ctx, transcript)
				return "first", nil
			}
			gotSeed = params.SeedMessages
			return "second", nil
		},
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		Proposals:    NewProposalRegistry(),
	}

	resA, err := StartRetainedRun(context.Background(), rt, "A", SubAgentParams{Role: "developer"}, 10*time.Second, false)
	if err != nil {
		t.Fatalf("dispatch A: %v", err)
	}
	envA := decodeProposalEnvelope(t, resA)

	if _, err := StartRetainedRun(context.Background(), rt, "B", SubAgentParams{
		Role:     "developer",
		SeedFrom: envA.ProposalID,
		SeedMode: jobs.SeedModeConversation,
	}, 10*time.Second, false); err != nil {
		t.Fatalf("same-role conversation seed: %v", err)
	}
	if !reflect.DeepEqual(gotSeed, transcript) {
		t.Fatalf("conversation seed = %+v, want %+v", gotSeed, transcript)
	}

	_, err = StartRetainedRun(context.Background(), rt, "C", SubAgentParams{
		Role:     "tester",
		SeedFrom: envA.ProposalID,
		SeedMode: jobs.SeedModeConversation,
	}, 10*time.Second, false)
	if err == nil || !strings.Contains(err.Error(), "same-role") {
		t.Fatalf("cross-role conversation seed err = %v, want a same-role refusal", err)
	}
}
