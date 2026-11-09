package subagent

import (
	"slices"
	"testing"
)

// delegationRegistryFixtures builds role bytes covering the full mode
// vocabulary: "primary" (an orchestrator persona), "subagent", "all",
// and a role with no mode key (the pre-yaah-21uk default, normalized to
// subagent). The ids are all the same length so the map literal needs
// no gofmt alignment padding.
func delegationRegistryFixtures() map[string][]byte {
	return map[string][]byte{
		"boss.md": []byte("---\nname: Boss\nmode: primary\ntools: [read]\ndescription: runs the session\n---\nbody"),
		"work.md": []byte("---\nname: Worker\nmode: subagent\ntools: [read]\ndescription: does work\n---\nbody"),
		"flex.md": []byte("---\nname: Flex\nmode: all\ntools: [read]\ndescription: does either\n---\nbody"),
		"base.md": []byte("---\nname: Legacy\ntools: [read]\ndescription: old default\n---\nbody"),
	}
}

// TestDelegatableRoles pins yaah-21uk.4: primary-only roles disappear
// from every dispatch-facing enumeration; "subagent" (explicit or the
// missing-key default) and "all" stay. The registry itself keeps every
// role — only the delegate surface filters.
func TestDelegatableRoles(t *testing.T) {
	reg := NewRoleRegistry()
	if err := reg.LoadBytes(delegationRegistryFixtures()); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	SetDefaultRoleRegistry(reg)
	defer SetDefaultRoleRegistry(nil)

	all := []string{"boss", "work", "flex", "base"}

	t.Run("primary excluded, subagent all and default kept", func(t *testing.T) {
		got := DelegatableRoles(all)
		want := []string{"work", "flex", "base"}
		if !slices.Equal(got, want) {
			t.Errorf("DelegatableRoles(%v) = %v, want %v", all, got, want)
		}
	})

	t.Run("per-name matrix", func(t *testing.T) {
		for _, tt := range []struct {
			role string
			want bool
		}{
			{"boss", false}, // mode: primary — orchestrator persona
			{"work", true},  // mode: subagent
			{"flex", true},  // mode: all
			{"base", true},  // no mode key — defaults to subagent
			{"ghost", true}, // unknown: dispatch validation rejects it elsewhere
		} {
			if got := DelegatableRole(tt.role); got != tt.want {
				t.Errorf("DelegatableRole(%q) = %v, want %v", tt.role, got, tt.want)
			}
		}
	})

	t.Run("no registry keeps every name", func(t *testing.T) {
		SetDefaultRoleRegistry(nil)
		got := DelegatableRoles(all)
		if !slices.Equal(got, all) {
			t.Errorf("nil registry: DelegatableRoles = %v, want %v unchanged", got, all)
		}
	})
}
