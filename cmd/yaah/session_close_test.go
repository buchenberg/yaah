package yaah

import (
	"context"
	"testing"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/jobs"
)

// TestSessionCloseKeepsFollowupsOpenThroughTheDrain pins the teardown
// ordering close() must keep: the background-job drain runs to completion
// BEFORE followupCh closes. A job that finishes during the drain delivers
// through wiring's Deliver closure, whose select retains a send case on
// followupCh — closing the channel first turns that send into a
// send-on-closed-channel panic, and CloseAndWait deliberately keeps job
// goroutines alive for the whole drain, so the ordering is load-bearing.
//
// The probe send runs while the drain is provably in flight (the cancelled
// job has signalled but not yet returned): with the current ordering the
// channel is still open and the send succeeds; with the old ordering the
// channel closed before the drain began and this send panics.
func TestSessionCloseKeepsFollowupsOpenThroughTheDrain(t *testing.T) {
	bg := jobs.NewBackgroundJobs()
	sess := &agentSession{
		backgroundJobs: bg,
		followupCh:     make(chan string, 32),
	}

	draining := make(chan struct{})
	release := make(chan struct{})
	runner := func(ctx context.Context, prompt string, p jobs.SubAgentParams) (string, error) {
		<-ctx.Done()
		close(draining)
		<-release
		return "result", ctx.Err()
	}

	if _, err := bg.Launch(context.Background(), runner, "worker", "drain-test", "x", jobs.SubAgentParams{Role: "worker"}, 0); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		sess.close()
	}()

	<-draining
	// The drain is in flight: the cancelled job is still alive, so close()
	// must not have reached the followupCh close yet.
	sess.followupCh <- "probe"

	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close() did not finish after the job was released")
	}
}

// TestSessionClosePrunesTerminalScopes pins the R2 teardown step:
// close() prunes the scope manager's terminal scopes — merged and
// discarded children accumulate over a long session, their trace
// records survive, and nothing close() does afterwards needs them.
// Pruning is idempotent at the kernel level; the pin is that close()
// calls it at all and that live scopes survive.
func TestSessionClosePrunesTerminalScopes(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr := shepherd.NewScopeManager(store)
	root, err := mgr.Create("sess-prune", nil)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	merged, err := mgr.Fork(root.ID(), "sub-merged", nil)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	live, err := mgr.Fork(root.ID(), "sub-live", nil)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := root.Merge(merged); err != nil {
		t.Fatalf("merge: %v", err)
	}

	sess := &agentSession{
		scopes: mgr,
	}
	sess.close()

	if _, ok := mgr.Get(merged.ID()); ok {
		t.Error("close() must prune the merged child from the registry")
	}
	if _, ok := mgr.Get(live.ID()); !ok {
		t.Error("close() must not prune a live child")
	}
	if _, ok := mgr.Get(root.ID()); !ok {
		t.Error("close() must not prune the root")
	}
	// Idempotent: a second close's prune finds nothing to remove and
	// must not fail or remove anything else.
	sess.close()
	if _, ok := mgr.Get(live.ID()); !ok {
		t.Error("a second close must not prune live scopes")
	}
}
