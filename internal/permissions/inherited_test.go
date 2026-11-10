package permissions

import (
	"context"
	"testing"
)

// TestInheritedCeiling pins the W5.7 filter: only deny rules on mutating
// file tools survive. Shell denies stay the parent's gate (Kilo #11523 —
// broad shell denies break sub-agents one level removed from the rule
// that caused them), ask rules cannot propagate (a sub-agent has no one
// to ask), allow rules must not (a ceiling only narrows).
func TestInheritedCeiling(t *testing.T) {
	rules := Rules{
		{Tool: "edit", Resource: "*.md", Effect: Allow},
		{Tool: "edit", Resource: "*", Effect: Deny},
		{Tool: "rmdir", Effect: Deny},
		{Tool: "bash", Resource: "git push*", Effect: Deny}, // shell: not inherited
		{Tool: "webfetch", Effect: Deny},                    // not a file tool
		{Tool: "write", Resource: "secrets/*", Effect: Ask}, // ask: not inherited
	}
	got := InheritedCeiling(rules)
	if len(got) != 2 {
		t.Fatalf("ceiling = %+v, want 2 rules", got)
	}
	if got[0] != rules[1] || got[1] != rules[2] {
		t.Errorf("ceiling kept wrong rules: %+v", got)
	}
	if len(InheritedCeiling(nil)) != 0 {
		t.Error("nil ruleset yields no ceiling")
	}
}

// TestInheritedContext round-trips the ceiling through a context: the
// dispatch carries it, a foreign context reads nil, and a nil set is
// distinguishable from an empty one by length alone (both inherit
// nothing).
func TestInheritedContext(t *testing.T) {
	ctx := context.Background()
	if got := Inherited(ctx); got != nil {
		t.Errorf("bare context carried %+v", got)
	}
	rules := Rules{{Tool: "edit", Effect: Deny}}
	if got := Inherited(WithInherited(ctx, rules)); len(got) != 1 || got[0] != rules[0] {
		t.Errorf("round trip lost the ceiling: %+v", got)
	}
	if got := Inherited(WithInherited(ctx, nil)); len(got) != 0 {
		t.Errorf("nil ceiling should read empty: %+v", got)
	}
}
