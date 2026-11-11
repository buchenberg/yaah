package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
)

// capability_parity_test.go keeps the policy table honest about the two
// name-keyed maps this package owns. They answer different questions (the
// inspection surface vs platform-independent gating), so the maps stay; the
// table is the cross-check (memory tool-gating-consistency).

func TestReadOnlySetMatchesToolPolicies(t *testing.T) {
	declared := make(map[string]bool)
	for _, name := range tools.InspectionPolicyNames() {
		declared[name] = true
	}
	for name := range readOnlyToolNames {
		if !declared[name] {
			t.Errorf("%s is in readOnlyToolNames but not Inspect in tools.capability", name)
		}
	}
	for name := range declared {
		if !readOnlyToolNames[name] {
			t.Errorf("%s is Inspect in tools.capability but not in readOnlyToolNames", name)
		}
	}
}

func TestAlwaysDangerousMatchesToolPolicies(t *testing.T) {
	for name := range alwaysDangerous {
		p, ok := tools.PolicyFor(name)
		if !ok || p.Danger != tools.DangerPolicyAlways {
			t.Errorf("%s is alwaysDangerous but its policy is %v (known=%v)", name, p.Danger, ok)
		}
	}
	for name, p := range tools.Policies() {
		if p.Danger == tools.DangerPolicyAlways && !alwaysDangerous[name] {
			t.Errorf("%s declares DangerPolicyAlways but is not in alwaysDangerous", name)
		}
	}
}
