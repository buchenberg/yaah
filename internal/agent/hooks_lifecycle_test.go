package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// TestTeardown_emitsSessionEnd pins the teardown ordering: session.end
// must be present in the hook log even though teardown closes the hook
// emitter in the same call. Previously Close ran before the Emit, so
// session.end was silently dropped whenever the hook file was already
// open.
func TestTeardown_emitsSessionEnd(t *testing.T) {
	dir := t.TempDir()
	l := &Loop{
		Persister: NewSessionPersister(nil, nil, "sess"),
		Hooks:     events.NewHookEmitter(dir, "sess"),
		Config:    LoopConfig{Model: "test-model"},
	}
	// Emit first so the hook file is open when teardown runs — the case
	// where the old Close-before-Emit ordering lost the event.
	l.Hooks.Emit(HookEvent{Event: events.SessionStart})

	var runErr error
	l.teardown(&runErr)

	data, err := os.ReadFile(filepath.Join(dir, "sess.jsonl"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	if !strings.Contains(string(data), `"session.end"`) {
		t.Fatalf("session.end missing from hook log: %q", string(data))
	}
	if !strings.Contains(string(data), `"exit_reason":"completed"`) {
		t.Errorf("exit_reason missing/wrong in hook log: %q", string(data))
	}
}

// TestLoop_secondRunKeepsEmittingHooks pins hook reusability: a reused
// Loop (the REPL/serve pattern) must keep emitting hook events after the
// first Run's teardown closed the emitter.
func TestLoop_secondRunKeepsEmittingHooks(t *testing.T) {
	dir := t.TempDir()
	fp := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "first"},
			FinishReason: "stop",
		}}},
		{Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "second"},
			FinishReason: "stop",
		}}},
	}}

	loop := &Loop{
		Config: LoopConfig{
			SystemPrompt:  "You are a test bot.",
			MaxLoopCycles: 5,
			SessionID:     "hook-sess",
		},
		Provider: fp,
		Registry: tools.NewRegistry(),
		Hooks:    events.NewHookEmitter(dir, "hook-sess"),
	}

	if _, err := loop.Run(context.Background(), "one"); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	if _, err := loop.Run(context.Background(), "two"); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "hook-sess.jsonl"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	content := string(data)
	if got := strings.Count(content, `"session.end"`); got != 2 {
		t.Errorf("session.end count = %d; want 2 (one per Run)\nlog: %s", got, content)
	}
	if got := strings.Count(content, `"turn.start"`); got < 2 {
		t.Errorf("turn.start count = %d; want >= 2 (second Run emitted nothing after first teardown?)\nlog: %s", got, content)
	}
}
