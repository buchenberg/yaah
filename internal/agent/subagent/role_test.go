package subagent

import (
	"testing"
	"time"
)

func TestRoleProfileFor(t *testing.T) {
	t.Run("default has full tools and limits", func(t *testing.T) {
		p := RoleProfileFor(RoleDefault)
		if len(p.Tools) == 0 {
			t.Error("RoleDefault should have tools")
		}
		if !contains(p.Tools, "read") || !contains(p.Tools, "write") {
			t.Error("RoleDefault should include read and write")
		}
		if !contains(p.Tools, "bash") || !contains(p.Tools, "powershell") {
			t.Error("RoleDefault should include shell tools")
		}
		if !contains(p.Tools, "spawn_subagent") {
			t.Error("RoleDefault should include spawn_subagent")
		}
		if !p.IsSpawnCapable() {
			t.Error("RoleDefault should be spawn-capable")
		}
		if p.MaxIterations != 25 {
			t.Errorf("RoleDefault MaxIterations = %d, want 25", p.MaxIterations)
		}
		if p.Timeout != 180*time.Second {
			t.Errorf("RoleDefault Timeout = %v, want 180s", p.Timeout)
		}
		if p.MaxDepth != 0 {
			t.Errorf("RoleDefault MaxDepth = %d, want 0", p.MaxDepth)
		}
	})

	t.Run("unknown role is zero-value (no tools)", func(t *testing.T) {
		p := RoleProfileFor(SubAgentRole("bogus"))
		if len(p.Tools) != 0 {
			t.Errorf("unknown role should fall back to zero-value profile, got %v", p.Tools)
		}
		if p.IsSpawnCapable() {
			t.Error("unknown role should not be spawn-capable")
		}
	})
}

func TestRoleGuidance(t *testing.T) {
	if g := RoleGuidance(RoleDefault); g == "" {
		t.Error("RoleGuidance(RoleDefault) should not be empty")
	}
	if g := RoleGuidance(SubAgentRole("bogus")); g != "" {
		t.Errorf("RoleGuidance(bogus) should be empty, got %q", g)
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
