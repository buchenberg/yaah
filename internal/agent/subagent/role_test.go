package subagent

import (
	"testing"
)

func TestRoleProfileFor(t *testing.T) {
	t.Run("unknown role is zero-value (no tools)", func(t *testing.T) {
		p := RoleProfileFor(SubAgentRole("bogus"))
		if len(p.Tools) != 0 {
			t.Errorf("unknown role should fall back to zero-value profile, got %v", p.Tools)
		}
		if contains(p.Tools, "spawn_subagent") {
			t.Error("unknown role should not be spawn-capable")
		}
	})

	t.Run("no registry is zero-value for every role", func(t *testing.T) {
		SetDefaultRoleRegistry(nil)
		if p := RoleProfileFor(SubAgentRole("analyst")); len(p.Tools) != 0 {
			t.Errorf("no registry: expected zero-value profile, got %v", p.Tools)
		}
	})
}

func TestRoleGuidance(t *testing.T) {
	if g := RoleGuidance(SubAgentRole("bogus")); g != "" {
		t.Errorf("RoleGuidance(bogus) should be empty, got %q", g)
	}
	SetDefaultRoleRegistry(nil)
	if g := RoleGuidance(SubAgentRole("analyst")); g != "" {
		t.Errorf("no registry: RoleGuidance should be empty, got %q", g)
	}
}

func TestRoleDisplayNameFallback(t *testing.T) {
	SetDefaultRoleRegistry(nil)
	if got := RoleDisplayName(SubAgentRole("custom_role")); got != "custom_role" {
		t.Errorf("display name should fall back to the role identifier, got %q", got)
	}
}

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}
