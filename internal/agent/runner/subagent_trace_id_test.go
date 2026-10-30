package runner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// staticReplyProvider answers every request with one final message, so a
// dispatch finishes without touching a tool.
type staticReplyProvider struct{ content string }

func (p staticReplyProvider) Send(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: p.content},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 3},
	}, nil
}

// TestSubAgentTraceIDUniquePerDispatch pins the uniqueness invariant the
// kernel depends on: it registers a sub-agent scope under
// "scope:" + this ID, so two dispatches minting the same string lose one
// dispatch to a create-race (95ri.1).
func TestSubAgentTraceIDUniquePerDispatch(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	first := subAgentTraceID("analyst", "sess-1", at)
	second := subAgentTraceID("analyst", "sess-1", at)
	if first == second {
		t.Fatalf("two dispatches in the same clock tick minted the same scope key %q", first)
	}
	if !strings.HasPrefix(first, "sub-analyst-sess-1-") {
		t.Errorf("ID %q lost the sub-<role>-<session>-<stamp>-<suffix> shape", first)
	}

	// The suffix is 128 bits of crypto/rand: a batch of IDs minted from
	// one instant must all differ, so uniqueness never rests on the
	// clock.
	seen := make(map[string]bool, 2000)
	for range 2000 {
		id := subAgentTraceID("analyst", "sess-1", at)
		if seen[id] {
			t.Fatalf("duplicate dispatch key %q", id)
		}
		seen[id] = true
	}
}

// TestTaskRunner_ParallelDispatchesGetDistinctScopes reproduces the
// reported failure end to end: two delegate calls in one turn, stamped
// by the same clock reading. With a clock-only key the second dispatch
// failed with "sub-agent scope: scope ... already exists" and produced no
// work.
func TestTaskRunner_ParallelDispatchesGetDistinctScopes(t *testing.T) {
	initTestRoles(t)

	mgr, store := newScopeManagerForTest(t)
	root, err := CreateRootScope(mgr, "sess-parallel", "", true)
	if err != nil {
		t.Fatalf("CreateRootScope: %v", err)
	}

	frozen := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	runner := makeTaskRunner(taskRunnerOpts{
		provider:      staticReplyProvider{content: "surveyed"},
		systemPrompt:  "BASE",
		modelName:     "test-model",
		subCfg:        config.SubAgentConfig{},
		defaults:      config.Defaults{},
		rootScopeID:   root.ID(),
		parentSession: "sess-parallel",
		scopeManager:  mgr,
		traceStore:    store,
		now:           func() time.Time { return frozen },
	}, 0)

	const dispatches = 2
	errs := make([]error, dispatches)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = runner(context.Background(), "survey the tree", tools.SubAgentParams{Role: "analyst"})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("dispatch %d: %v (a sibling's scope key must not collide)", i, err)
		}
	}
	if scopes := mgr.AllScopes(); len(scopes) != dispatches+1 {
		t.Errorf("scope tree holds %d entries; want the root plus %d dispatch scopes", len(scopes), dispatches)
	}
}
