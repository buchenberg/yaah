package agent

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
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
// concurrency slot, while staying out of the task-specific branches (which shape
// spawn_subagent's own envelope). Without this a composite tool escapes
// MaxSubAgentConcurrency entirely.
func TestNewDispatch_SubAgentWorkAccountsForCompositeTools(t *testing.T) {
	l := &Loop{}
	call := func(name string) types.ToolCall {
		return types.ToolCall{ID: name, Type: "function", Function: types.ToolCallFn{Name: name, Arguments: "{}"}}
	}

	for _, tc := range []struct {
		name     string
		task     bool
		accounts bool
	}{
		{"spawn_subagent", true, true},
		{"oversee", false, true}, // composite: dispatches, but its envelope is not a spawn's
		{"read", false, false},
		{"proposal", false, false},
	} {
		d := l.newDispatch(0, call(tc.name))
		if d.isTask != tc.task {
			t.Errorf("%s: isTask = %v, want %v", tc.name, d.isTask, tc.task)
		}
		if d.isSubAgentWork != tc.accounts {
			t.Errorf("%s: isSubAgentWork = %v, want %v", tc.name, d.isSubAgentWork, tc.accounts)
		}
	}
}
