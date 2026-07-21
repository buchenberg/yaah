package subagent

import (
	"testing"
	"time"
)

func TestRoleProfileFor(t *testing.T) {
	t.Run("worker", func(t *testing.T) {
		p := RoleProfileFor(RoleWorker)
		if !contains(p.Tools, "bash") {
			t.Error("worker profile must include bash")
		}
		if !contains(p.Tools, "webfetch") {
			t.Error("worker profile must include webfetch")
		}
		if contains(p.Tools, "task") {
			t.Error("worker profile must NOT include task (workers cannot spawn)")
		}
		if p.IsSpawnCapable() {
			t.Error("worker should not be spawn-capable")
		}
		if p.MaxIterations != 25 {
			t.Errorf("worker MaxIterations = %d, want 25", p.MaxIterations)
		}
		if p.Timeout != 120*time.Second {
			t.Errorf("worker Timeout = %v, want 120s", p.Timeout)
		}
	})

	t.Run("reviewer is read-only", func(t *testing.T) {
		p := RoleProfileFor(RoleReviewer)
		for _, dangerous := range []string{"write", "edit", "delete", "bash", "powershell", "task"} {
			if contains(p.Tools, dangerous) {
				t.Errorf("reviewer profile must NOT include %q", dangerous)
			}
		}
		if !contains(p.Tools, "read") || !contains(p.Tools, "grep") {
			t.Error("reviewer profile must include read and grep")
		}
		if p.IsSpawnCapable() {
			t.Error("reviewer should not be spawn-capable")
		}
		if p.Timeout != 0 {
			t.Errorf("reviewer Timeout = %v, want 0 (unlimited)", p.Timeout)
		}
	})

	t.Run("planner can spawn", func(t *testing.T) {
		p := RoleProfileFor(RolePlanner)
		if !contains(p.Tools, "task") {
			t.Error("planner profile must include task")
		}
		if !p.IsSpawnCapable() {
			t.Error("planner should be spawn-capable")
		}
		if p.MaxIterations != 50 {
			t.Errorf("planner MaxIterations = %d, want 50", p.MaxIterations)
		}
		if p.Timeout != 300*time.Second {
			t.Errorf("planner Timeout = %v, want 300s", p.Timeout)
		}
		if p.MaxDepth != 3 {
			t.Errorf("planner MaxDepth = %d, want 3", p.MaxDepth)
		}
	})

	t.Run("default is zero-value (legacy)", func(t *testing.T) {
		p := RoleProfileFor(RoleDefault)
		if len(p.Tools) != 0 {
			t.Errorf("RoleDefault should have no tools, got %v", p.Tools)
		}
		if p.IsSpawnCapable() {
			t.Error("RoleDefault should not be spawn-capable")
		}
	})

	t.Run("unknown role falls back to default", func(t *testing.T) {
		p := RoleProfileFor(SubAgentRole("bogus"))
		if len(p.Tools) != 0 {
			t.Errorf("unknown role should fall back to zero-value profile")
		}
	})
}

func TestRoleGuidance(t *testing.T) {
	for _, role := range []SubAgentRole{RoleWorker, RoleReviewer, RolePlanner} {
		if g := RoleGuidance(role); g == "" {
			t.Errorf("RoleGuidance(%q) returned empty", role)
		}
	}
	if g := RoleGuidance(RoleDefault); g != "" {
		t.Errorf("RoleGuidance(RoleDefault) should be empty, got %q", g)
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
