package tools

import (
	"strings"
	"testing"
)

func TestResolveDispatchWorld(t *testing.T) {
	t.Run("auto uses the worktree while the container lane is unshipped", func(t *testing.T) {
		rt := RetainedRuntime{World: "auto", Containment: "auto", Network: "allow_all"}
		res, err := resolveDispatchWorld(rt, "developer", false)
		if err != nil {
			t.Fatalf("resolveDispatchWorld: %v", err)
		}
		if res.Resolved != "worktree" || res.Degraded {
			t.Fatalf("got %+v, want an undegraded worktree", res)
		}
	})

	t.Run("explicit container fails closed", func(t *testing.T) {
		rt := RetainedRuntime{World: "container"}
		if _, err := resolveDispatchWorld(rt, "developer", false); err == nil {
			t.Fatal("container requested on a build without the lane must fail")
		} else if !strings.Contains(err.Error(), "container") {
			t.Fatalf("error = %q, want it to name the world", err)
		}
	})

	t.Run("per-role override wins", func(t *testing.T) {
		rt := RetainedRuntime{
			World:        "auto",
			WorldForRole: func(role string) string { return "container" },
		}
		if _, err := resolveDispatchWorld(rt, "reviewer", true); err == nil {
			t.Fatal("the role override must be honoured (and fail closed)")
		}
	})

	t.Run("a shipped lane reports a host failure as a degradation", func(t *testing.T) {
		rt := RetainedRuntime{World: "auto", ContainerWorlds: true, Containment: "auto", Network: "allow_all"}
		res, err := resolveDispatchWorld(rt, "developer", false)
		if err != nil {
			t.Fatalf("resolveDispatchWorld: %v", err)
		}
		if res.Resolved != "worktree" || !res.Degraded {
			t.Fatalf("got %+v, want a degraded worktree", res)
		}
		if !strings.Contains(res.Reason, "croft-dlb") {
			t.Fatalf("reason %q must name the blocker", res.Reason)
		}
	})
}
