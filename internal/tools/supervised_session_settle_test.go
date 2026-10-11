package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

// settledAction reads the settlement action recorded on a variant
// scope's owner path (shepherd.run_output.settled.v1), the durable
// assertion of which kernel action chose ran.
func settledAction(t *testing.T, owner string) string {
	t.Helper()
	slice, err := SharedTraceStore.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace for %s: %v", owner, err)
	}
	for _, id := range slice.FactIDs() {
		if rec, ok := slice.FactsByID[id].(shepherdtrace.Record); ok && rec.Envelope.SchemaRef == shepherd.SchemaRunOutputSettled {
			action, _ := rec.Body.Payload["action"].(string)
			return action
		}
	}
	t.Fatalf("no %s fact retained for %s", shepherd.SchemaRunOutputSettled, owner)
	return ""
}

// forkAndDrift starts a session, forks two variants that each write
// name in their isolated worktrees, applies drift to the parent tree
// after the fork, and returns the session and paths.
func forkAndDrift(t *testing.T, variantFile string, drift func(repo string)) (string, *SupervisorTool, string, *sessionRunner) {
	t.Helper()
	tool, sup, repo := newReviewTestEnv(t)
	tool.WorktreeRoot = t.TempDir()

	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one"},
			{result: "variant A"},
			{result: "variant B"},
		},
	}
	runner.sideEffect = func(call int, _ string) {
		switch call {
		case 1, 2:
			writeTestFile(t, runner.workdirFor(t, call), variantFile, "variant\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "start")
	if env := mustSupervisorExecute(t, sup, `{"action":"fork","session_id":`+jsonString(sid)+`,"prompt_a":"A","prompt_b":"B"}`); env.Status != "awaiting_choose" {
		t.Fatalf("fork status = %q, want awaiting_choose", env.Status)
	}
	if drift != nil {
		drift(repo)
	}
	return sid, sup, repo, runner
}

// TestReviewSession_ChooseSelectFastPath pins the common case
// (adoption plan C2): the parent never moved since the fork, so choose
// settles the winner with Select — fast-forward onto the parent — and
// the settlement records it.
func TestReviewSession_ChooseSelectFastPath(t *testing.T) {
	sid, sup, repo, _ := forkAndDrift(t, "variant-a.txt", nil)

	chosen := mustSupervisorExecute(t, sup, `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`)
	if chosen.Status != "chosen" {
		t.Fatalf("choose status = %q, want chosen", chosen.Status)
	}
	if _, err := os.Stat(filepath.Join(repo, "variant-a.txt")); err != nil {
		t.Errorf("the selected variant's work must land in the parent: %v", err)
	}
	if action := settledAction(t, sid+":var-a"); action != "selected" {
		t.Errorf("winner settlement action = %q, want selected (the parent never moved)", action)
	}
	if action := settledAction(t, sid+":var-b"); action != "discarded" {
		t.Errorf("loser settlement action = %q, want discarded", action)
	}
}

// TestReviewSession_ChooseAppliesOverParentDrift pins the drift case:
// the parent tree changed (path-disjoint) between fork and choose, so
// the winner settles with Apply — a three-way merge that keeps the
// parent's concurrent changes.
func TestReviewSession_ChooseAppliesOverParentDrift(t *testing.T) {
	sid, sup, repo, _ := forkAndDrift(t, "variant-a.txt", func(repo string) {
		// Disjoint drift: a file the variants never touch.
		writeTestFile(t, repo, "parent-drift.txt", "drift\n")
	})

	chosen := mustSupervisorExecute(t, sup, `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`)
	if chosen.Status != "chosen" {
		t.Fatalf("choose status = %q, want chosen (env: %s)", chosen.Status, marshalReviewEnvelope(chosen))
	}
	if _, err := os.Stat(filepath.Join(repo, "variant-a.txt")); err != nil {
		t.Errorf("the applied variant's work must land in the parent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "parent-drift.txt")); err != nil {
		t.Errorf("the parent's disjoint drift must survive the apply: %v", err)
	}
	if action := settledAction(t, sid+":var-a"); action != "applied" {
		t.Errorf("winner settlement action = %q, want applied (the parent drifted)", action)
	}
}

// TestReviewSession_ChooseOverlapConflictRefusesUnconsumed pins the
// typed refusal: the parent drifted onto a path the sealed output also
// changes, so the settle refuses with ErrApplyConflict — the output
// stays unconsumed (its worktree survives), the session stays
// awaiting-choose, and choosing again after the drift resolves works.
func TestReviewSession_ChooseOverlapConflictRefusesUnconsumed(t *testing.T) {
	sid, sup, repo, runner := forkAndDrift(t, "shared.txt", func(repo string) {
		// Overlapping drift: the same path the variants change.
		writeTestFile(t, repo, "shared.txt", "drifted\n")
	})

	dirA := runner.workdirFor(t, 1)
	_, err := sup.Execute(context.Background(), `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`)
	if err == nil || !strings.Contains(err.Error(), "apply conflict") {
		t.Fatalf("choose over an overlapping drift must refuse with the typed conflict, got: %v", err)
	}

	// The refusal consumed nothing: the session is still awaiting
	// choose and the winner's worktree still stands.
	if _, serr := os.Stat(dirA); serr != nil {
		t.Errorf("an unconsumed output must keep its worktree: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(repo, "shared.txt")); serr != nil {
		t.Fatalf("the parent's drift must be untouched by the refused apply: %v", serr)
	}

	// Resolve the drift and choose again: the same output settles.
	if rerr := os.Remove(filepath.Join(repo, "shared.txt")); rerr != nil {
		t.Fatalf("resolve drift: %v", rerr)
	}
	chosen := mustSupervisorExecute(t, sup, `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`)
	if chosen.Status != "chosen" {
		t.Fatalf("choose after resolving the drift: status = %q, want chosen (env: %s)", chosen.Status, marshalReviewEnvelope(chosen))
	}
	data, rerr := os.ReadFile(filepath.Join(repo, "shared.txt"))
	if rerr != nil || string(data) != "variant\n" {
		t.Errorf("the settled variant's content must win after resolution, got %q (%v)", data, rerr)
	}
}

// TestReviewSession_DoubleChooseRefused pins consume-once: a settled
// fork cannot be chosen again — the session state guard refuses a
// second choose, and the kernel's ErrOutputConsumed maps to the
// already-chose error when the state guard did not fire first.
func TestReviewSession_DoubleChooseRefused(t *testing.T) {
	sid, sup, _, _ := forkAndDrift(t, "variant-a.txt", nil)

	if chosen := mustSupervisorExecute(t, sup, `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`); chosen.Status != "chosen" {
		t.Fatalf("first choose status = %q, want chosen", chosen.Status)
	}
	if _, err := sup.Execute(context.Background(), `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`); err == nil {
		t.Error("a second choose after the session left awaiting_choose must fail")
	}

	// The consume-once mapping: an output settled out-of-band (a
	// partial-choose recovery) is refused as already chosen — not as a
	// silent re-settle of a physical effect that already happened.
	sid2, sup2, repo2, runner2 := forkAndDrift(t, "variant-a.txt", nil)
	_ = repo2
	sess, err := getReviewSession(sid2)
	if err != nil {
		t.Fatalf("getReviewSession: %v", err)
	}
	sess.mu.Lock()
	discardErr := sess.varA.Output.Settle(context.Background(), shepherd.SettleDiscard)
	sess.mu.Unlock()
	if discardErr != nil {
		t.Fatalf("out-of-band discard of the winner: %v", discardErr)
	}
	// The discard tore the winner's worktree; the tree never moved
	// (variants are isolated), so the refusal is clean.
	if _, serr := os.Stat(runner2.workdirFor(t, 1)); !os.IsNotExist(serr) {
		t.Errorf("the out-of-band discard must tear down the worktree, stat err = %v", serr)
	}
	_, err = sup2.Execute(context.Background(), `{"action":"choose","session_id":`+jsonString(sid2)+`,"winner":"a"}`)
	if err == nil || !strings.Contains(err.Error(), "already chosen") {
		t.Errorf("choosing an already-settled output must map to the already-chose error, got: %v", err)
	}
	// The failed fork is still teardown-able: abort cleans the loser.
	abortEnv := mustSupervisorExecute(t, sup2, `{"action":"abort","session_id":`+jsonString(sid2)+`}`)
	if abortEnv.Status != "aborted" {
		t.Errorf("abort after a refused choose: status = %q, want aborted", abortEnv.Status)
	}
	if _, serr := os.Stat(runner2.workdirFor(t, 2)); !os.IsNotExist(serr) {
		t.Errorf("abort must tear down the remaining variant's worktree, stat err = %v", serr)
	}
}

// TestReviewSession_AbortDuringForkTearsDownWorktrees pins the abort
// path: the branches' worktrees are session resources — abort discards
// both through the settlement, and the parent tree sits at the fork
// point needing no rewind.
func TestReviewSession_AbortDuringForkTearsDownWorktrees(t *testing.T) {
	sid, sup, repo, runner := forkAndDrift(t, "variant-a.txt", func(repo string) {
		writeTestFile(t, repo, "unit-work.txt", "kept at fork point\n")
	})

	env := mustSupervisorExecute(t, sup, `{"action":"abort","session_id":`+jsonString(sid)+`}`)
	if env.Status != "aborted" {
		t.Fatalf("abort status = %q, want aborted", env.Status)
	}
	for _, call := range []int{1, 2} {
		if _, err := os.Stat(runner.workdirFor(t, call)); !os.IsNotExist(err) {
			t.Errorf("abort must tear down variant %d's worktree, stat err = %v", call, err)
		}
	}
	// The parent tree sits at the fork point: the unit-start state,
	// untouched by the isolated variants.
	if _, err := os.Stat(filepath.Join(repo, "unit-work.txt")); err != nil {
		t.Errorf("the fork-point tree must be untouched by abort: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "variant-a.txt")); !os.IsNotExist(err) {
		t.Error("the variants' work must never reach the parent tree")
	}
}

// TestReviewSession_ForkSealsOutputs pins the seal itself: each variant
// scope's owner path carries a sealed record before any settlement,
// and the envelope's variants are backed by the frozen outputs.
func TestReviewSession_ForkSealsOutputs(t *testing.T) {
	sid, sup, _, _ := forkAndDrift(t, "variant-a.txt", nil)
	_ = sup

	env := mustSupervisorExecute(t, sup, `{"action":"review_diff","session_id":`+jsonString(sid)+`}`)
	if env.Status != "review_diff" {
		t.Fatalf("review_diff status = %q, want review_diff", env.Status)
	}
	for _, label := range []string{"a", "b"} {
		variant, ok := env.Variants[label]
		if !ok {
			t.Fatalf("review_diff must carry variant %q", label)
		}
		if variant.Diff == "" || len(variant.Files) != 1 || variant.Files[0] != "variant-a.txt" {
			t.Errorf("variant %q must be backed by the sealed output's frozen diff (files=%v)", label, variant.Files)
		}
	}
	for _, label := range []string{"a", "b"} {
		owner := fmt.Sprintf("%s:var-%s", sid, label)
		slice, err := SharedTraceStore.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 999, shepherdtrace.ModeBoth)
		if err != nil {
			t.Fatalf("read trace for %s: %v", owner, err)
		}
		found := false
		for _, id := range slice.FactIDs() {
			if rec, ok := slice.FactsByID[id].(shepherdtrace.Record); ok && rec.Envelope.SchemaRef == shepherd.SchemaRunOutputSealed {
				found = true
			}
		}
		if !found {
			t.Errorf("variant %q's scope must carry a %s fact", label, shepherd.SchemaRunOutputSealed)
		}
	}
}

// TestReviewSession_ChooseWithDirtyForkPoint pins the digest-based
// action picker's reason for existing (review finding): a fork point
// with uncommitted content mints a fresh state revision per capture,
// so the parent can never digest-match the baseline — a content diff
// would pick Select and the kernel would refuse. The session must
// still choose: the picker sends it down Apply, which merges cleanly.
func TestReviewSession_ChooseWithDirtyForkPoint(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	tool.WorktreeRoot = t.TempDir()

	// Uncommitted work exists before the session starts, so the fork
	// point is a dirty tree.
	writeTestFile(t, repo, "dirty.txt", "uncommitted fork point\n")

	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one"},
			{result: "variant A"},
			{result: "variant B"},
		},
	}
	runner.sideEffect = func(call int, _ string) {
		if call == 1 || call == 2 {
			writeTestFile(t, runner.workdirFor(t, call), "variant.txt", "variant\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "start")
	if env := mustSupervisorExecute(t, sup, `{"action":"fork","session_id":`+jsonString(sid)+`,"prompt_a":"A","prompt_b":"B"}`); env.Status != "awaiting_choose" {
		t.Fatalf("fork status = %q, want awaiting_choose", env.Status)
	}

	chosen := mustSupervisorExecute(t, sup, `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`)
	if chosen.Status != "chosen" {
		t.Fatalf("choose over a dirty fork point: status = %q, want chosen (env: %s)", chosen.Status, marshalReviewEnvelope(chosen))
	}
	if _, err := os.Stat(filepath.Join(repo, "variant.txt")); err != nil {
		t.Errorf("the winner's work must land on the dirty parent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "dirty.txt")); err != nil {
		t.Errorf("the dirty fork point's content must survive the settlement: %v", err)
	}
}
