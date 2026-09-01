package tools

import (
	"context"
	"strings"
	"testing"
)

// TestListSubAgentsShowsBudget pins the §4.7 rendering: the orchestrator
// sees each role's effective loop budget (and floors) instead of guessing.
func TestListSubAgentsShowsBudget(t *testing.T) {
	tool := &ListSubAgentsTool{
		Lister: func() []SubAgentInfo {
			return []SubAgentInfo{
				{
					Role:          "reviewer",
					DisplayName:   "Tim",
					Specialty:     "reviewer",
					Tools:         []string{"read", "grep"},
					Iterations:    25,
					Turns:         8,
					MinIterations: 10,
					MinTurns:      8,
				},
			}
		},
	}

	out, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"Budget: 25 iterations (min 10), 8 tool turns (min 8)",
		"Tools: read, grep",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestListSubAgentsOmitsBudgetWhenUnknown(t *testing.T) {
	tool := &ListSubAgentsTool{
		Lister: func() []SubAgentInfo {
			return []SubAgentInfo{{Role: "grump", DisplayName: "Grump", Tools: []string{}}}
		},
	}

	out, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "Budget:") {
		t.Errorf("zero-valued budget should not render a Budget line:\n%s", out)
	}
}
