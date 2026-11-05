package yaah

import (
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/wake"
)

func TestDeliverWakeQueuesFollowUpWithoutRequester(t *testing.T) {
	sess := &agentSession{
		followupCh: make(chan string, 1),
		closeCh:    make(chan struct{}),
	}
	sess.deliverWake("[WATCH] ci is green")
	select {
	case got := <-sess.followupCh:
		if got != "[WATCH] ci is green" {
			t.Fatalf("follow-up = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("wake was not queued on the follow-up channel")
	}
}

func TestDeliverWakeOffersToRequester(t *testing.T) {
	sess := &agentSession{
		followupCh: make(chan string, 1),
		closeCh:    make(chan struct{}),
	}
	var got string
	sess.SetWakeTurnRequester(func(text string) bool {
		got = text
		return true
	})
	sess.deliverWake("wake")
	if got != "wake" {
		t.Fatalf("requester got %q, want the digest", got)
	}
	select {
	case extra := <-sess.followupCh:
		t.Fatalf("accepted wake was also queued as a follow-up: %q", extra)
	default:
	}
}

func TestDeliverWakeFallsBackWhenRequesterDeclines(t *testing.T) {
	sess := &agentSession{
		followupCh: make(chan string, 1),
		closeCh:    make(chan struct{}),
	}
	sess.SetWakeTurnRequester(func(string) bool { return false })
	sess.deliverWake("busy wake")
	select {
	case got := <-sess.followupCh:
		if got != "busy wake" {
			t.Fatalf("follow-up = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("declined wake was not queued as a follow-up")
	}
}

func TestDeliverWakeUnblocksOnClose(t *testing.T) {
	sess := &agentSession{
		followupCh: make(chan string, 1),
		closeCh:    make(chan struct{}),
	}
	// Fill the channel so the next delivery blocks.
	sess.deliverWake("first")
	done := make(chan struct{})
	go func() {
		sess.deliverWake("second")
		close(done)
	}()
	close(sess.closeCh)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wake delivery did not unblock when the session closed")
	}
}

func TestIdleWakeTurnsEnabledConfig(t *testing.T) {
	s := &agentSession{}
	if !s.IdleWakeTurnsEnabled() {
		t.Fatal("a session with no config must default to idle wake turns enabled")
	}
	off := false
	s.cfg = &config.Config{}
	s.cfg.Agent.Default.IdleWakeTurns = &off
	if s.IdleWakeTurnsEnabled() {
		t.Fatal("idle_wake_turns: false must disable the session policy")
	}
}

func TestStartWakeTurnRefusesBusyGate(t *testing.T) {
	sess := &agentSession{}
	sess.runMu.Lock()
	defer sess.runMu.Unlock()
	if sess.StartWakeTurn("wake") {
		t.Fatal("StartWakeTurn ran while the run gate was held")
	}
}

func TestStartIsIdempotent(t *testing.T) {
	sess := &agentSession{
		wakeMgr: wake.NewManager(wake.Options{}),
		closeCh: make(chan struct{}),
	}
	defer sess.wakeMgr.Close()
	sess.Start()
	sess.Start() // must not panic, rehydrate again, or double-deliver
}
