package agent

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
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
