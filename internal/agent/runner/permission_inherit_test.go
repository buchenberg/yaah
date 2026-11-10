package runner

import (
	"testing"

	"github.com/buchenberg/yaah/internal/permissions"
)

// TestWithInheritedCeiling pins the merge precedence (parity W5.7): the
// role's own rules come first, the inherited ceiling appended after — so
// under last-match-wins an inherited deny wins over a role-file rule for
// the same tool and resource. A role file can narrow further, never
// widen what the parent denied.
func TestWithInheritedCeiling(t *testing.T) {
	role := permissions.Rules{
		{Tool: "edit", Resource: "*.md", Effect: permissions.Allow},
		{Tool: "write", Effect: permissions.Deny},
	}
	inherited := permissions.Rules{
		{Tool: "edit", Effect: permissions.Deny},
	}

	merged := withInheritedCeiling(role, inherited)
	if len(merged) != 3 {
		t.Fatalf("merged %d rules, want 3", len(merged))
	}
	// The inherited deny is last and therefore wins for every edit.
	if effect, ok := merged.Match("edit", "docs/notes.md"); !ok || effect != permissions.Deny {
		t.Errorf("inherited deny must beat the role's allow: got (%v, %v)", effect, ok)
	}
	// The role's own narrowing that does not conflict survives.
	if effect, ok := merged.Match("write", "x"); !ok || effect != permissions.Deny {
		t.Errorf("role's own deny must survive: got (%v, %v)", effect, ok)
	}

	// One-sided cases pass through.
	if got := withInheritedCeiling(nil, inherited); len(got) != 1 {
		t.Errorf("ceiling-only = %+v", got)
	}
	if got := withInheritedCeiling(role, nil); len(got) != 2 || got[0] != role[0] {
		t.Errorf("role-only = %+v", got)
	}
	if got := withInheritedCeiling(nil, nil); got != nil {
		t.Errorf("both empty = %+v, want nil", got)
	}
}
