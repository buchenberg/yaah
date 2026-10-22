package yaah

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// waitFor gives a detached dispatch a bounded window to complete before a test
// declares it never ran.
const consolidateTestWait = 5 * time.Second

func consolidateTestTranscript() []types.Message {
	return []types.Message{
		{Role: "user", Content: "we decided to store memory in SQLite"},
		{Role: "tool", Content: strings.Repeat("noise ", 400)},
		{Role: "assistant", Content: "noted the decision"},
	}
}

// TestDispatchConsolidation verifies the deep-sleep dispatch: it runs the
// consolidator role through the injected sub-agent runner, detaches from the
// caller's cancellation, ignores other kinds, and swallows panics.
func TestDispatchConsolidation(t *testing.T) {
	t.Run("dispatches the consolidator role with a tagged directive", func(t *testing.T) {
		params := make(chan tools.SubAgentParams, 1)
		prompts := make(chan string, 1)
		var gotErr atomic.Bool
		runner := func() tools.TaskRunner {
			return func(ctx context.Context, prompt string, p tools.SubAgentParams) (string, error) {
				if ctx.Err() != nil {
					gotErr.Store(true)
				}
				prompts <- prompt
				params <- p
				return "1 memory", nil
			}
		}

		// The run that fired the nudge is already over: its context is
		// cancelled, and the dispatch must survive it.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		dispatchConsolidation(ctx, consolidateKindMemory, runner, consolidateTestTranscript)

		select {
		case p := <-params:
			if p.Role != consolidatorRole {
				t.Fatalf("role = %q, want %q", p.Role, consolidatorRole)
			}
		case <-time.After(consolidateTestWait):
			t.Fatal("dispatch never reached the sub-agent runner")
		}
		if gotErr.Load() {
			t.Error("dispatch inherited the caller's cancellation; want a detached context")
		}

		prompt := <-prompts
		for _, want := range []string{"source:compaction", `"session"`, "memory_add", "user: we decided"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q", want)
			}
		}
		if len(prompt) > consolidateTranscriptBytes+len(consolidateDirective)+1024 {
			t.Errorf("prompt not bounded: %d bytes", len(prompt))
		}
		if strings.Contains(prompt, strings.Repeat("noise ", 200)) {
			t.Error("oversized tool result was not collapsed")
		}
	})

	t.Run("ignores other kinds", func(t *testing.T) {
		var called atomic.Bool
		runner := func() tools.TaskRunner {
			return func(ctx context.Context, prompt string, p tools.SubAgentParams) (string, error) {
				called.Store(true)
				return "", nil
			}
		}
		dispatchConsolidation(context.Background(), "skills", runner, consolidateTestTranscript)
		time.Sleep(50 * time.Millisecond)
		if called.Load() {
			t.Fatal("dispatch ran for a kind it does not handle")
		}
	})

	t.Run("no runner leaves the nudge inert", func(t *testing.T) {
		dispatchConsolidation(context.Background(), consolidateKindMemory, nil, consolidateTestTranscript)
		dispatchConsolidation(context.Background(), consolidateKindMemory, func() tools.TaskRunner { return nil }, consolidateTestTranscript)
	})

	t.Run("a panicking consolidator does not escape", func(t *testing.T) {
		ran := make(chan struct{}, 1)
		runner := func() tools.TaskRunner {
			return func(ctx context.Context, prompt string, p tools.SubAgentParams) (string, error) {
				ran <- struct{}{}
				panic("consolidator exploded")
			}
		}
		dispatchConsolidation(context.Background(), consolidateKindMemory, runner, consolidateTestTranscript)
		select {
		case <-ran:
		case <-time.After(consolidateTestWait):
			t.Fatal("dispatch never ran")
		}
	})

	t.Run("a failing consolidator is swallowed", func(t *testing.T) {
		ran := make(chan struct{}, 1)
		runner := func() tools.TaskRunner {
			return func(ctx context.Context, prompt string, p tools.SubAgentParams) (string, error) {
				ran <- struct{}{}
				return "", context.DeadlineExceeded
			}
		}
		dispatchConsolidation(context.Background(), consolidateKindMemory, runner, consolidateTestTranscript)
		select {
		case <-ran:
		case <-time.After(consolidateTestWait):
			t.Fatal("dispatch never ran")
		}
	})
}

// TestConsolidateFnNilWithoutRegistry verifies the guard that keeps the nudge
// inert when the session has no tool registry to dispatch through.
func TestConsolidateFnNilWithoutRegistry(t *testing.T) {
	if fn := (*agentSession)(nil).consolidateFn(); fn != nil {
		t.Fatal("nil session produced a ConsolidateFn")
	}
	if fn := (&agentSession{}).consolidateFn(); fn != nil {
		t.Fatal("session without a tool registry produced a ConsolidateFn")
	}
}

func TestDeepSleepGate(t *testing.T) {
	t.Run("default always deep", func(t *testing.T) {
		g := newDeepSleepGate(0)
		for i := 0; i < 5; i++ {
			if !g.deep() {
				t.Fatalf("nudge %d should be deep (deepEvery=0 -> always)", i+1)
			}
		}
	})

	t.Run("deep every third nudge", func(t *testing.T) {
		g := newDeepSleepGate(3)
		want := map[int]bool{1: true, 4: true, 7: true}
		for i := 1; i <= 7; i++ {
			if got := g.deep(); got != want[i] {
				t.Errorf("nudge %d deep() = %v, want %v", i, got, want[i])
			}
		}
	})

	t.Run("one means every nudge", func(t *testing.T) {
		g := newDeepSleepGate(1)
		for i := 0; i < 3; i++ {
			if !g.deep() {
				t.Fatal("deepEvery=1 must fork every nudge")
			}
		}
	})
}
