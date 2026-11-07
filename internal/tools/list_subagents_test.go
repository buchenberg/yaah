package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
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

// TestListSubAgentsExcludesPrimaryOnlyRoles pins yaah-21uk.4: the
// orchestrator must not see primary-only personas in list_subagents —
// it cannot dispatch them, so advertising them would only invite a
// doomed or self-referential dispatch. The lister still yields every
// registry entry; filtering is the view's job.
func TestListSubAgentsExcludesPrimaryOnlyRoles(t *testing.T) {
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(delegationModeFixtures()); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	defer subagent.SetDefaultRoleRegistry(nil)

	tool := &ListSubAgentsTool{
		Lister: func() []SubAgentInfo {
			return []SubAgentInfo{
				{Role: "boss", DisplayName: "Boss", Tools: []string{"read"}},
				{Role: "work", DisplayName: "Worker", Tools: []string{"read"}},
				{Role: "flex", DisplayName: "Flex", Tools: []string{"read"}},
				{Role: "base", DisplayName: "Legacy", Tools: []string{"read"}},
			}
		},
	}

	out, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "boss") {
		t.Errorf("primary-only role leaked into list_subagents output:\n%s", out)
	}
	if !strings.HasPrefix(out, "3 sub-agent role(s) available") {
		t.Errorf("count should reflect the filtered list:\n%s", out)
	}
	for _, want := range []string{"Role ID: work", "Role ID: flex", "Role ID: base"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
