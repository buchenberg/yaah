package yaah

import (
	"context"
	"testing"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
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
	discarded, err := mgr.Fork(root.ID(), "sub-discarded", nil)
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
	if err := root.Discard(discarded); err != nil {
		t.Fatalf("discard: %v", err)
	}

	sess := &agentSession{
		scopes: mgr,
	}
	sess.close()

	if _, ok := mgr.Get(merged.ID()); ok {
		t.Error("close() must prune the merged child from the registry")
	}
	if _, ok := mgr.Get(discarded.ID()); ok {
		t.Error("close() must prune the discarded child from the registry")
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

// TestSessionCloseClosesShepherdInfrastructure pins plan 07 H2's happy
// exit: a session with the shepherd infrastructure tears the trace store
// and its bus down on close — store first, then the bus — instead of
// leaving both to the process exit. Post-close, the store refuses writes
// and the bus's subscriber channels are closed.
func TestSessionCloseClosesShepherdInfrastructure(t *testing.T) {
	dir := t.TempDir()
	store, bus, mgr, err := pipeline.InitShepherdInfrastructure(dir, 0)
	if err != nil {
		t.Fatalf("init infrastructure: %v", err)
	}
	sess := &agentSession{
		traceStore: store,
		traceBus:   bus,
		scopes:     mgr,
	}

	sess.close()

	if _, aerr := store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: "post-close-probe",
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: "probe",
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Declaration,
				SchemaRef: "shepherd.probe.v1",
				KindLabel: "probe",
				Payload:   map[string]any{"probe": true},
			}},
		}},
	}); aerr == nil {
		t.Error("the trace store accepted a write after close; the clean exit must close it")
	}
	if storeAppendable(store) {
		t.Error("the trace store is still usable after close")
	}
}

// TestSessionCloseLeavesInfraOpenOnDrainTimeout pins plan 07 H2's
// survivor branch: a background job that outlives the drain shares the
// trace store, and closing it under a live writer would turn the job's
// final writes into corruption-shaped failures — worse than an unclosed
// store the process exit reaps. The infrastructure stays open, and
// close() says so.
func TestSessionCloseLeavesInfraOpenOnDrainTimeout(t *testing.T) {
	dir := t.TempDir()
	store, bus, _, err := pipeline.InitShepherdInfrastructure(dir, 0)
	if err != nil {
		t.Fatalf("init infrastructure: %v", err)
	}

	// A job that ignores cancellation and outlives any shrunk drain.
	bg := jobs.NewBackgroundJobs()
	hang := make(chan struct{})
	if _, lerr := bg.Launch(context.Background(), func(ctx context.Context, prompt string, p jobs.SubAgentParams) (string, error) {
		<-hang
		return "", ctx.Err()
	}, "role", "hang", "hang", jobs.SubAgentParams{}, time.Minute); lerr != nil {
		t.Fatalf("launch hanging job: %v", lerr)
	}
	t.Cleanup(func() { close(hang); _ = bg.CloseAndWait(5 * time.Second) })
	// The store DELIBERATELY stays open past close() here — that is the
	// assertion — but on Windows an open handle blocks the TempDir
	// cleanup, so the test closes it before returning.
	t.Cleanup(func() { _ = store.Close(context.Background()) })

	oldTimeout := backgroundJobDrainTimeout
	backgroundJobDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { backgroundJobDrainTimeout = oldTimeout })

	sess := &agentSession{
		backgroundJobs: bg,
		traceStore:     store,
		traceBus:       bus,
	}
	sess.close()

	if !storeAppendable(store) {
		t.Error("the trace store was closed under a live background job; a drain-timeout survivor must leave it open")
	}
	if sess.traceStore != store || sess.traceBus != bus {
		t.Error("close() dropped the infrastructure references without closing them")
	}
}

// TestCloseShepherdInfrastructureIdempotent pins the double-close no-op:
// the store's database Close and the bus's closed guard make a second
// close benign — a close that already happened (the wiring's
// recovery-failure path) must not fabricate a failure.
func TestCloseShepherdInfrastructureIdempotent(t *testing.T) {
	dir := t.TempDir()
	store, bus, _, err := pipeline.InitShepherdInfrastructure(dir, 0)
	if err != nil {
		t.Fatalf("init infrastructure: %v", err)
	}

	pipeline.CloseShepherdInfrastructure(store, bus)
	pipeline.CloseShepherdInfrastructure(store, bus) // must not panic or error

	if storeAppendable(store) {
		t.Error("the store is still usable after being closed")
	}
}

// storeAppendable reports whether the store still accepts a write — the
// probe the close tests use to tell an open store from a closed one.
func storeAppendable(store *shepherdtrace.SQLiteTraceStore) bool {
	_, err := store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: "appendable-probe",
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: "probe",
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Declaration,
				SchemaRef: "shepherd.probe.v1",
				KindLabel: "probe",
				Payload:   map[string]any{"probe": true},
			}},
		}},
	})
	return err == nil
}

var _ = shepherd.Scope{} // keep the shepherd import for the shared fixtures
