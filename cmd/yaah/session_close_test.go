package yaah

import (
	"context"
	"testing"
	"time"

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
