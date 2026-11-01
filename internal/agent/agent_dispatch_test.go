package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// withRoleRegistry installs a registry loaded from the given role files
// and restores the previous default on cleanup.
func withRoleRegistry(t *testing.T, files map[string][]byte) {
	t.Helper()
	prev := subagent.DefaultRegistry()
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(files); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(prev) })
}

func TestParseContractVerdict(t *testing.T) {
	t.Run("bullet without kind", func(t *testing.T) {
		out := "## Result\n\n```\nValidation Report\n- **verdict**: PASS\n- **detail**: all good\n```"
		v, ok := parseContractVerdict(out)
		if !ok || !strings.EqualFold(v, "PASS") {
			t.Errorf("verdict = %q ok=%v, want PASS", v, ok)
		}
	})
	t.Run("bullet with kind", func(t *testing.T) {
		out := "```\nGate\n- **verdict** (evidence): FAIL\n- **detail**: 3 tests broken\n```"
		v, ok := parseContractVerdict(out)
		if !ok || !strings.EqualFold(v, "FAIL") {
			t.Errorf("verdict = %q ok=%v, want FAIL", v, ok)
		}
	})
	t.Run("no block", func(t *testing.T) {
		if _, ok := parseContractVerdict("tests ran, things look fine"); ok {
			t.Error("expected no verdict")
		}
	})
}

func TestGateVerdictFailed_Structured(t *testing.T) {
	withRoleRegistry(t, map[string][]byte{
		"validator.md": []byte("---\nname: validator\ndescription: v\ncontract:\n  heading: Validation Report\n  fields:\n    - name: verdict\n    - name: detail\n---\nbody"),
	})

	t.Run("structured FAIL wins over PASS mentions", func(t *testing.T) {
		out := "## Validation Report\n\n```\nValidation Report\n- **verdict**: FAIL\n- **detail**: PASS was printed by the test suite earlier\n```"
		if !gateVerdictFailed("validator", out) {
			t.Error("expected FAIL from structured verdict")
		}
	})
	t.Run("structured PASS", func(t *testing.T) {
		out := "```\nValidation Report\n- **verdict**: PASS\n- **detail**: none\n```"
		if gateVerdictFailed("validator", out) {
			t.Error("expected PASS from structured verdict")
		}
	})
	t.Run("no structured block falls back to heuristic", func(t *testing.T) {
		if !gateVerdictFailed("validator", "ran tests\nFAIL TestX\n") {
			t.Error("expected heuristic FAIL")
		}
	})
}

func TestGateVerdictFailed_HeuristicWithoutContract(t *testing.T) {
	// No registry installed (or role unknown): heuristic decides.
	if gateVerdictFailed("no-such-role", "all good, tests PASS") {
		t.Error("expected heuristic PASS")
	}
	if !gateVerdictFailed("no-such-role", "FAIL TestX\n") {
		t.Error("expected heuristic FAIL")
	}
	if gateVerdictFailed("no-such-role", "FAIL earlier, but PASS at the end") {
		t.Error("last-occurrence heuristic should pass when PASS follows FAIL")
	}
}

// A tool that dispatches sub-agent work of its own must take a sub-agent
// sub-agent work is exactly a delegate call: it takes the concurrency
// slot and the task-specific branches. A composite tool that dispatched sub-agents
// of its own once existed (oversee) and is gone, so the flag is one concept again
// — this test pins that a non-spawn tool neither takes a slot nor is shaped like
// a spawn.
func TestNewDispatch_OnlySpawnSubAgentTakesASubAgentSlot(t *testing.T) {
	l := &Loop{}
	call := func(name string) types.ToolCall {
		return types.ToolCall{ID: name, Type: "function", Function: types.ToolCallFn{Name: name, Arguments: "{}"}}
	}

	for _, tc := range []struct {
		name string
		task bool
	}{
		{"delegate", true},
		{"read", false},
		{"proposal", false},
	} {
		d := l.newDispatch(0, call(tc.name))
		if d.isDelegate != tc.task {
			t.Errorf("%s: isDelegate = %v, want %v", tc.name, d.isDelegate, tc.task)
		}
	}
}

func TestDispatch_PlanModeRefusesUnadvertisedToolAtExecution(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	tool := &countingTool{name: "write"}
	reg.Register(tool)
	l := NewLoop(nil, reg, WithToolsLevel(ReadOnlyTools))
	l.Hooks = &HookEmitter{}
	d := l.newDispatch(0, types.ToolCall{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "write", Arguments: `{}`}})
	results := make(chan toolExecResult, 1)
	d.run(context.Background(), results)
	result := <-results
	if tool.calls != 0 {
		t.Fatalf("plan mode executed write %d times", tool.calls)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "not available at the current tool level") {
		t.Fatalf("result err = %v, want tool-level refusal", result.err)
	}
}

type countingTool struct {
	name  string
	calls int
}

func (c *countingTool) Name() string                                    { return c.name }
func (c *countingTool) Description() string                             { return "counting test tool" }
func (c *countingTool) Schema() json.RawMessage                         { return json.RawMessage(`{"type":"object"}`) }
func (c *countingTool) Execute(context.Context, string) (string, error) { c.calls++; return "ran", nil }

// The speculation guidance promises that `max_concurrency` bounds a fan-out of
// parallel delegate calls. That bound is the sub-agent semaphore: each
// dispatch takes a slot and holds it until the dispatch ends, so N concurrent
// calls cannot exceed the cap.
func TestDispatch_SubAgentSemaphoreBoundsFanOut(t *testing.T) {
	l := &Loop{subAgentSem: make(chan struct{}, 1)}
	spawn := func() types.ToolCall {
		return types.ToolCall{Type: "function", Function: types.ToolCallFn{Name: "delegate"}}
	}

	first := l.newDispatch(0, spawn())
	if err := first.acquire(context.Background()); err != nil {
		t.Fatalf("first dispatch must take the only slot: %v", err)
	}
	defer first.release()

	second := l.newDispatch(1, spawn())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := second.acquire(ctx); err == nil {
		second.release()
		t.Fatal("a second dispatch took a slot while the cap was 1 — the fan-out bound does not hold")
	}

	// The slot frees when the first dispatch ends, not before.
	first.release()
	if err := second.acquire(context.Background()); err != nil {
		t.Fatalf("the slot must be reusable once the first dispatch ends: %v", err)
	}
	second.release()
}
