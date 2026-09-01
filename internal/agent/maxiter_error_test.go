package agent

import (
	"strings"
	"testing"
)

// TestMaxIterationsErrorCarriesBudgetSource pins the §4.7 enrichment of
// subagent-turn-budget-floors: the exhaustion error reports the budget
// provenance so retry guidance can say "who set this budget".
func TestMaxIterationsErrorCarriesBudgetSource(t *testing.T) {
	t.Run("with provenance", func(t *testing.T) {
		err := MaxIterationsError{MaxIter: 5, MaxTurns: 4, IterationsSource: "call", TurnsSource: "call"}
		msg := err.Error()
		if !strings.Contains(msg, "max iterations (5) reached") {
			t.Errorf("message = %q, want the bare form preserved", msg)
		}
		if !strings.Contains(msg, "(budget source: call)") {
			t.Errorf("message = %q, want budget source included", msg)
		}
	})

	t.Run("without provenance keeps the legacy form", func(t *testing.T) {
		err := MaxIterationsError{MaxIter: 3}
		if got, want := err.Error(), "max iterations (3) reached"; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})
}
