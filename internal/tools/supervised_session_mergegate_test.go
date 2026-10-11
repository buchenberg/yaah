package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
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

// readOwnerFacts reads every retained fact on one owner's path.
func readOwnerFacts(t *testing.T, owner string) []shepherdtrace.Record {
	t.Helper()
	slice, err := SharedTraceStore.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 999, shepherdtrace.ModeBoth)
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
	tool, sup, repo := newReviewTestEnv(t)
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
	proposed := findFact(t, readOwnerFacts(t, sid), shepherd.SchemaMergeProposed)
	if got, _ := proposed.Body.Payload["child_scope"].(string); got != "scope:"+sid {
		t.Errorf("proposal child_scope = %q, want the session scope", got)
	}
	decision := findFact(t, readOwnerFacts(t, "test-session-root"), shepherd.SchemaSupervisorDecision)
	if approved, _ := decision.Body.Payload["approved"].(bool); !approved {
		t.Errorf("decision = %+v, want approved", decision.Body.Payload)
	}
	if got, _ := decision.Body.Payload["child_scope"].(string); got != "scope:"+sid {
		t.Errorf("decision child_scope = %q, want the session scope", got)
	}

	// The session scope merged (terminal), and the session closed.
	scope, ok := SharedScopeManager.Get("scope:" + sid)
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
	tool, sup, repo := newReviewTestEnv(t)
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

	// Denial leaves the parent tree byte-identical to the unit start:
	// at the retry's start the denied file is gone and git reports a
	// clean tree.
	if strings.TrimSpace(statusAtRetryStart) != "" {
		t.Errorf("tree at the retry's start must be byte-identical to unit start, git reports:\n%s", statusAtRetryStart)
	}

	// The reason is injected into the re-run; the re-run re-seeds from
	// the unit-start conversation (empty for unit 1).
	if p := runner.promptFor(t, 1); !strings.Contains(p, "changes must stay under drafts/") {
		t.Errorf("retry prompt = %q, want the denial reason injected", p)
	}
	if len(runner.seedFor(t, 1)) != 0 {
		t.Error("retry must re-seed from the unit-start conversation (empty for unit 1)")
	}

	// The denial was recorded on the root's path.
	decision := findFact(t, readOwnerFacts(t, "test-session-root"), shepherd.SchemaSupervisorDecision)
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
	tool, _, _ := newReviewTestEnv(t)
	tool.RootScopeID = ""
	tool.Runner = (&sessionRunner{}).run()

	_, err := tool.Execute(context.Background(), `{"prompt":"do the work","role":"worker","review":true}`)
	if err == nil || !strings.Contains(err.Error(), "root scope") {
		t.Errorf("review start without a root scope must refuse naming the root scope, got: %v", err)
	}
}
