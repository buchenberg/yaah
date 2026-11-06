package tools

// retained_lane_helpers_test.go duplicates the retained-lane runner fixtures
// from internal/dispatch's tests (retained_test.go): test helpers cannot cross
// package boundaries, and delegate_test.go exercises the retained lane through
// DelegateTool. Keep the copies in sync.

import (
	"encoding/json"
	"testing"

	"github.com/buchenberg/yaah/internal/dispatch"
)

// decodeProposalEnvelope parses a proposal result into its envelope.
func decodeProposalEnvelope(t *testing.T, result string) proposalEnvelope {
	t.Helper()
	var env proposalEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("proposal envelope is not JSON: %v\n%s", err, result)
	}
	return env
}

// newRetainedTestEnv builds a RetainedRuntime over a real temp repo, a real
// scope manager, and a worktree root, with the given runner.
func newRetainedTestEnv(t *testing.T, runner *sessionRunner) RetainedRuntime {
	t.Helper()
	fx, repo, rootID := newSessionFixtures(t)
	return RetainedRuntime{
		Runner:       runner.run(),
		RepoPath:     repo,
		ScopeManager: fx.mgr,
		RootScopeID:  rootID,
		WorktreeRoot: t.TempDir(),
		// A registry per test, as the composition root injects one per
		// session (yaah-yox).
		Proposals: dispatch.NewProposalRegistry(),
	}
}

// writingRunner returns a runner whose sub-agent writes name in its worktree.
func writingRunner(t *testing.T, name string) *sessionRunner {
	t.Helper()
	r := &sessionRunner{}
	r.sideEffect = func(call int, _ string) {
		writeTestFile(t, r.workdirFor(t, call), name, "from sub-agent\n")
	}
	return r
}
