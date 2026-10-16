package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"
)

// gitStatus returns the repo's porcelain status — the byte-identity
// check for a denial's rollback.
func gitStatus(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status in %s: %v\n%s", repo, err, out)
	}
	return string(out)
}

// readOwnerFacts reads every retained fact on one owner's path from
// the session's own trace store.
func readOwnerFacts(t *testing.T, store *shepherdtrace.SQLiteTraceStore, owner string) []shepherdtrace.Record {
	t.Helper()
	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace for %s: %v", owner, err)
	}
	var facts []shepherdtrace.Record
	for _, id := range slice.FactIDs() {
		if rec, ok := slice.FactsByID[id].(shepherdtrace.Record); ok {
			facts = append(facts, rec)
		}
	}
	return facts
}

// findFact returns the first retained fact with the given schema ref.
func findFact(t *testing.T, facts []shepherdtrace.Record, schemaRef string) shepherdtrace.Record {
	t.Helper()
	for _, f := range facts {
		if f.Envelope.SchemaRef == schemaRef {
			return f
		}
	}
	t.Fatalf("no %s fact retained (have %d facts)", schemaRef, len(facts))
	return shepherdtrace.Record{}
}

// TestReviewSession_AcceptMergesThroughGate pins the C1 approve path:
// accept runs the kernel's CommitMerge — the proposal is recorded on
// the session scope's path, the approved decision on the root's, and
// the session scope ends merged with the work kept in the tree.
func TestReviewSession_AcceptMergesThroughGate(t *testing.T) {
	tool, sup, repo, fx := newReviewTestEnv(t)
	runner := &sessionRunner{repoPath: repo, responses: []runnerResponse{{result: "gate-approved work"}}}
	runner.sideEffect = func(call int, repoPath string) {
		if call == 0 {
			writeTestFile(t, repoPath, "gate-kept.txt", "kept\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "do the work")

	env := mustSupervisorExecute(t, sup, `{"action":"accept","session_id":`+jsonString(sid)+`}`)
	if env.Status != "accepted" {
		t.Fatalf("status = %q, want accepted (env: %s)", env.Status, marshalReviewEnvelope(env))
	}
	if _, err := os.Stat(filepath.Join(repo, "gate-kept.txt")); err != nil {
		t.Errorf("approved work must be kept: %v", err)
	}

	// The gate recorded the review causally (C1): the proposal on the
	// session scope's owner path, the approve decision on the root's.
	proposed := findFact(t, readOwnerFacts(t, fx.store, sid), shepherd.SchemaMergeProposed)
	if got, _ := proposed.Body.Payload["child_scope"].(string); got != "scope:"+sid {
		t.Errorf("proposal child_scope = %q, want the session scope", got)
	}
	decision := findFact(t, readOwnerFacts(t, fx.store, "test-session-root"), shepherd.SchemaSupervisorDecision)
	if approved, _ := decision.Body.Payload["approved"].(bool); !approved {
		t.Errorf("decision = %+v, want approved", decision.Body.Payload)
	}
	if got, _ := decision.Body.Payload["child_scope"].(string); got != "scope:"+sid {
		t.Errorf("decision child_scope = %q, want the session scope", got)
	}

	// The session scope merged (terminal), and the session closed.
	scope, ok := sup.ScopeManager.Get("scope:" + sid)
	if !ok {
		t.Fatal("session scope vanished after accept")
	}
	if scope.State() != shepherd.ScopeMerged {
		t.Errorf("session scope state = %s, want merged", scope.State())
	}
	if _, err := sup.Execute(context.Background(), `{"action":"review_diff","session_id":`+jsonString(sid)+`}`); err == nil {
		t.Error("accept must close the session")
	}
}

// TestReviewSession_AcceptDeniedRollsBackAndRetries pins the C1 denial
// path: a merge-gate denial keeps today's UX — the tree is rolled back
// to the unit-start state (byte-identical: clean git status), the
// denial reason is injected into the re-run's prompt, and the session
// stays open. The retry runs on a fresh fork of the root whose baseline
// is the restored tree, so the next accept proposes only the retry's
// work — no reversions of the denied work.
func TestReviewSession_AcceptDeniedRollsBackAndRetries(t *testing.T) {
	tool, sup, repo, fx := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "put it in the wrong place"},
			{result: "put it in the right place"},
		},
	}
	// The denial's restore is observable only from inside the retry:
	// by the time accept returns, the re-run unit has already written
	// its work. Capture the tree state at the retry's start — it must
	// be byte-identical to the unit start (clean git status).
	var statusAtRetryStart string
	runner.sideEffect = func(call int, repoPath string) {
		switch call {
		case 0:
			writeTestFile(t, repoPath, "outside.txt", "denied\n")
		case 1:
			statusAtRetryStart = gitStatus(t, repoPath)
			writeTestFile(t, repoPath, "inside.txt", "approved\n")
		}
	}
	tool.Runner = runner.run()

	var proposals []shepherd.MergeProposal
	tool.MergePolicy = func(p shepherd.MergeProposal) error {
		proposals = append(proposals, p)
		if len(proposals) == 1 {
			return errors.New("changes must stay under drafts/")
		}
		return nil
	}

	sid := startTestReviewSession(t, tool, "do the work")

	env := mustSupervisorExecute(t, sup, `{"action":"accept","session_id":`+jsonString(sid)+`}`)
	if env.Status != "review" {
		t.Fatalf("status = %q, want review — the denial must re-run the unit (env: %s)", env.Status, marshalReviewEnvelope(env))
	}
	if env.Unit != 2 {
		t.Errorf("unit = %d, want 2 (the denied unit re-ran)", env.Unit)
	}
	if !strings.Contains(env.Error, "changes must stay under drafts/") {
		t.Errorf("envelope error = %q, want the denial reason surfaced", env.Error)
	}

	// Denial restores the tracked tree to the unit start, and the denied
	// addition is quarantined out of the workspace (review finding on
	// #326): it must neither survive into the retry's baseline (that
	// would hide it from the gate forever) nor be deleted outright (it
	// is kept under .git and named in the envelope).
	if strings.TrimSpace(statusAtRetryStart) != "" {
		t.Errorf("tree at the retry's start must be byte-identical to unit start, git reports:\n%s", statusAtRetryStart)
	}
	if _, err := os.Stat(filepath.Join(repo, "outside.txt")); !os.IsNotExist(err) {
		t.Errorf("denied outside.txt must be quarantined out of the tree, stat err = %v", err)
	}
	if !strings.Contains(env.Error, "quarantined") || !strings.Contains(env.Error, "outside.txt") {
		t.Errorf("envelope must report the quarantine, error = %q", env.Error)
	}
	if content := findQuarantined(t, repo, "outside.txt"); content != "denied\n" {
		t.Errorf("quarantined content = %q, want %q (kept, not deleted)", content, "denied\n")
	}

	// The reason is injected into the re-run; the re-run keeps the
	// conversation as context (the rollback is the tree, not the talk).
	if p := runner.promptFor(t, 1); !strings.Contains(p, "changes must stay under drafts/") {
		t.Errorf("retry prompt = %q, want the denial reason injected", p)
	}
	seed1 := runner.seedFor(t, 1)
	if len(seed1) == 0 {
		t.Fatal("retry must keep the prior conversation as context")
	}
	if last := seed1[len(seed1)-1]; last.Role != "assistant" || last.Content != "put it in the wrong place" {
		t.Errorf("retry seed ends with %q/%q, want the last unit's report", last.Role, last.Content)
	}

	// The denial was recorded on the root's path.
	decision := findFact(t, readOwnerFacts(t, fx.store, "test-session-root"), shepherd.SchemaSupervisorDecision)
	if approved, _ := decision.Body.Payload["approved"].(bool); approved {
		t.Error("first decision must be recorded as denied")
	}
	if reason, _ := decision.Body.Payload["reason"].(string); !strings.Contains(reason, "drafts/") {
		t.Errorf("decision reason = %q, want the gate's reason", reason)
	}

	// The session is still open: the retried unit awaits a verdict.
	env2 := mustSupervisorExecute(t, sup, `{"action":"accept","session_id":`+jsonString(sid)+`}`)
	if env2.Status != "accepted" {
		t.Fatalf("second accept status = %q, want accepted (env: %s)", env2.Status, marshalReviewEnvelope(env2))
	}
	if _, err := os.Stat(filepath.Join(repo, "inside.txt")); err != nil {
		t.Errorf("the retry's work must be kept: %v", err)
	}

	// Two proposals: the first carried the denied work; the second
	// carries only the retry's work — the re-fork's baseline is the
	// restored tree, not the denied one.
	if len(proposals) != 2 {
		t.Fatalf("proposals = %d, want 2", len(proposals))
	}
	if len(proposals[0].Changes) != 1 || proposals[0].Changes[0].Path != "outside.txt" {
		t.Errorf("first proposal changes = %+v, want outside.txt", proposals[0].Changes)
	}
	if len(proposals[1].Changes) != 1 || proposals[1].Changes[0].Path != "inside.txt" {
		t.Errorf("second proposal changes = %+v, want only inside.txt — the re-fork must baseline on the restored tree", proposals[1].Changes)
	}
}

// TestReviewSession_StartWithoutRootScopeRefused pins the C1
// precondition: without a root scope there is no fork parent and no
// baseline, so no merge gate — review sessions refuse to start rather
// than run ungated.
func TestReviewSession_StartWithoutRootScopeRefused(t *testing.T) {
	tool, _, _, _ := newReviewTestEnv(t)
	tool.RootScopeID = ""
	tool.Runner = (&sessionRunner{}).run()

	_, err := tool.Execute(context.Background(), `{"prompt":"do the work","role":"worker","review":true}`)
	if err == nil || !strings.Contains(err.Error(), "root scope") {
		t.Errorf("review start without a root scope must refuse naming the root scope, got: %v", err)
	}
}

// TestReviewSession_AcceptDeniedRollsBackCumulativeWork pins the
// cumulative semantics of a merge-gate denial (review finding): the
// gate's proposal diffs from the session scope's fork baseline — all
// continued units' work, not just the last unit's — so the denial's
// rollback is the session start. Earlier units' denied work must not
// survive into the retry's baseline.
func TestReviewSession_AcceptDeniedRollsBackCumulativeWork(t *testing.T) {
	tool, sup, repo, _ := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one: added the forbidden file"},
			{result: "unit two: added the good file"},
			{result: "redone without the forbidden file"},
		},
	}
	// Unit 1 adds the forbidden file, unit 2 a good one. The retry
	// captures the tree state it starts from — it must be the session
	// start (both files gone), and it redoes the work with only the
	// good file.
	var statusAtRetryStart string
	runner.sideEffect = func(call int, repoPath string) {
		switch call {
		case 0:
			writeTestFile(t, repoPath, "forbidden.txt", "denied\n")
		case 1:
			writeTestFile(t, repoPath, "good.txt", "fine\n")
		case 2:
			statusAtRetryStart = gitStatus(t, repoPath)
			writeTestFile(t, repoPath, "good.txt", "fine\n")
		}
	}
	tool.Runner = runner.run()

	tool.MergePolicy = func(p shepherd.MergeProposal) error {
		for _, c := range p.Changes {
			if c.Path == "forbidden.txt" {
				return errors.New("forbidden.txt may not be touched")
			}
		}
		return nil
	}

	sid := startTestReviewSession(t, tool, "do the work")

	// Unit 1 completes; the orchestrator accepts it per-unit via
	// continue — its forbidden file now stands in the tree.
	if env := mustSupervisorExecute(t, sup, `{"action":"continue","session_id":`+jsonString(sid)+`,"guidance":"now add the good file"}`); env.Status != "review" {
		t.Fatalf("continue status = %q, want review", env.Status)
	}

	// The final accept denies the cumulative proposal: the forbidden
	// file from unit 1 is in it.
	env := mustSupervisorExecute(t, sup, `{"action":"accept","session_id":`+jsonString(sid)+`}`)
	if env.Status != "review" {
		t.Fatalf("status = %q, want review — the denial must re-run the unit (env: %s)", env.Status, marshalReviewEnvelope(env))
	}
	if !strings.Contains(env.Error, "forbidden.txt") {
		t.Errorf("envelope error = %q, want the denial reason surfaced", env.Error)
	}

	// The rollback is the session start: both denied additions are
	// quarantined out of the workspace — kept under .git, gone from the
	// tree, and reported in the envelope. A retry baseline that
	// swallowed them would hide them from the gate forever (#326
	// review finding).
	if strings.TrimSpace(statusAtRetryStart) != "" {
		t.Errorf("tree at the retry's start must be the session start, git reports:\n%s", statusAtRetryStart)
	}
	if _, err := os.Stat(filepath.Join(repo, "forbidden.txt")); err == nil {
		t.Error("unit 1's denied work must be quarantined out of the tree (forbidden.txt still present)")
	}
	if content := findQuarantined(t, repo, "forbidden.txt"); content != "denied\n" {
		t.Errorf("forbidden.txt not kept in quarantine: %q", content)
	}
	if !strings.Contains(env.Error, "quarantined") {
		t.Errorf("envelope must report the quarantine, error = %q", env.Error)
	}

	// The retry's accept approves: the re-done work passes the gate.
	env2 := mustSupervisorExecute(t, sup, `{"action":"accept","session_id":`+jsonString(sid)+`}`)
	if env2.Status != "accepted" {
		t.Fatalf("second accept status = %q, want accepted (env: %s)", env2.Status, marshalReviewEnvelope(env2))
	}
	if _, err := os.Stat(filepath.Join(repo, "good.txt")); err != nil {
		t.Errorf("the re-done work must be kept: %v", err)
	}
}

// TestMapRestoreError_PinsKernelRefusalText pins the stale-checkpoint
// mapping (adoption plan R1/D5): the kernel does not type its
// CheckpointInvalid refusal, so the mapping matches the pinned
// message — and this test fails loudly if a kernel bump rewords it,
// instead of the recorded-reason contract silently degrading to the
// generic restore error.
func TestMapRestoreError_PinsKernelRefusalText(t *testing.T) {
	stale := mapRestoreError(fmt.Errorf("checkpoint cp:sess:1 is invalid, cannot restore"))
	if !strings.Contains(stale.Error(), "no longer matches the workspace") || !strings.Contains(stale.Error(), "invalid, cannot restore") {
		t.Errorf("stale mapping = %q, want the recorded-reason refusal carrying the kernel text", stale.Error())
	}
	other := mapRestoreError(fmt.Errorf("checkpoint cp:sess:1 is used, cannot restore"))
	if !strings.Contains(other.Error(), "supervisor: rollback: restore:") || strings.Contains(other.Error(), "no longer matches") {
		t.Errorf("generic mapping = %q, want the plain restore wrap", other.Error())
	}
}

// findQuarantined returns the content of a path inside the session's
// quarantine dir under .git, or "" when it is not there.
func findQuarantined(t *testing.T, repo, rel string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repo, ".git", "shepherd-quarantine", "*", filepath.FromSlash(rel)))
	if err != nil || len(matches) == 0 {
		return ""
	}
	b, rerr := os.ReadFile(matches[0])
	if rerr != nil {
		return ""
	}
	return string(b)
}
