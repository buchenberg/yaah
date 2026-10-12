package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	shepherdgit "github.com/buchenberg/shepherd-kernel-go/sandbox/git"
	"github.com/buchenberg/yaah/internal/agent/recovery"
)

// TestReviewSession_AfterRestart pins the R2 resume story at the
// review-session level: review sessions are process state (the
// registry is memory), so a crash takes the open one — but the
// recovered manager drives a NEW session through its whole cycle
// (start, continue, rollback), and the crashed session's id refuses
// with the start-a-new-one explanation instead of a mystery.
func TestReviewSession_AfterRestart(t *testing.T) {
	repo := newTestGitRepo(t)
	tracePath := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// The crashed run: a rooted session with a forked sub-agent (the
	// root must have forked to be recoverable).
	crashed := shepherd.NewScopeManager(store)
	root, err := crashed.Create("sess-crash", shepherdgit.NewLocalGitSandbox(repo))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if _, err := crashed.Fork(root.ID(), "sub-crashed", nil); err != nil {
		t.Fatalf("fork: %v", err)
	}
	store.Close(context.Background())

	// The restart: recovery rebuilds the registry.
	store2, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { store2.Close(context.Background()) })
	recovered, _, err := recovery.Recover(context.Background(), store2, repo, true, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	prevMgr, prevStore := SharedScopeManager, SharedTraceStore
	SharedScopeManager = recovered
	SharedTraceStore = store2
	t.Cleanup(func() {
		SharedScopeManager = prevMgr
		SharedTraceStore = prevStore
	})
	resetReviewSessions()
	t.Cleanup(resetReviewSessions)

	// The crashed session's verdict verbs refuse with the honest
	// explanation: the session was process state, the trace survived.
	_, err = (&SupervisorTool{RootScopeID: root.ID()}).Execute(context.Background(),
		`{"action":"continue","session_id":"supervised:worker:crashed","guidance":"go on"}`)
	if err == nil || !strings.Contains(err.Error(), "start one with supervised_task review:true") {
		t.Errorf("the crashed session's verb must explain how to start a new one, got: %v", err)
	}

	// A NEW review session on the recovered manager runs its whole
	// cycle: the fork roots on the recovered root, the checkpoints
	// ride its re-adopted sandbox, and rollback rewinds.
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one"},
			{result: "unit two"},
			{result: "corrected"},
		},
	}
	runner.sideEffect = func(call int, repoPath string) {
		if call == 0 {
			writeTestFile(t, repoPath, "after-restart.txt", "kept\n")
		}
	}
	taskTool := &SupervisedTaskTool{
		RoleNames:   []string{"worker"},
		RepoPath:    repo,
		RootScopeID: root.ID(),
		MaxRetries:  1,
		Runner:      runner.run(),
	}
	sup := &SupervisorTool{RootScopeID: root.ID()}

	sid := startTestReviewSession(t, taskTool, "work after the crash")

	cont := mustSupervisorExecute(t, sup, `{"action":"continue","session_id":`+jsonString(sid)+`,"guidance":"next unit"}`)
	if cont.Status != "review" || cont.Unit != 2 {
		t.Fatalf("continue on the recovered manager: %+v", cont)
	}

	rb := mustSupervisorExecute(t, sup, `{"action":"rollback","session_id":`+jsonString(sid)+`,"guidance":"redo it"}`)
	if rb.Status != "review" {
		t.Fatalf("rollback on the recovered manager: %+v", rb)
	}
	// The rollback rewound unit two's work; unit one's survives.
	if _, err := os.Stat(filepath.Join(repo, "after-restart.txt")); err != nil {
		t.Errorf("the first unit's work must survive the rollback: %v", err)
	}
}
