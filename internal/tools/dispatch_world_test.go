package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
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

	t.Run("a report dispatch without a configured image keeps the worktree silently", func(t *testing.T) {
		rt := RetainedRuntime{World: "auto"}
		res, err := resolveDispatchWorld(rt, "reviewer", true)
		if err != nil {
			t.Fatalf("resolveDispatchWorld: %v", err)
		}
		if res.Resolved != subagent.WorldWorktree || res.Degraded {
			t.Fatalf("got %+v, want an undegraded worktree", res)
		}
	})

	t.Run("explicit container names the missing image for a report dispatch", func(t *testing.T) {
		rt := RetainedRuntime{World: "container"}
		if _, err := resolveDispatchWorld(rt, "reviewer", true); err == nil {
			t.Fatal("container requested without a configured image must fail closed")
		} else if !strings.Contains(err.Error(), "workspace.containerd.image") {
			t.Fatalf("error = %q, want it to name the config key", err)
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

	t.Run("a report dispatch uses a wired container world", func(t *testing.T) {
		rt := RetainedRuntime{
			World:           "auto",
			ContainerWorlds: true,
			DispatchWorlds:  &DispatchWorlds{},
			Containment:     "auto",
			Network:         "allow_all",
		}
		res, err := resolveDispatchWorld(rt, "reviewer", true)
		if err != nil {
			t.Fatalf("resolveDispatchWorld: %v", err)
		}
		if res.Resolved != subagent.WorldContainer || res.Degraded {
			t.Fatalf("got %+v, want an undegraded container", res)
		}
	})
}

// TestDispatchWorldPreflightFailure pins the preflight decision: only an
// explicit container request fails closed; auto steps down to the worktree with
// the failure named in the recorded reason.
func TestDispatchWorldPreflightFailure(t *testing.T) {
	cause := errors.New("open probe sandbox: daemon unreachable")

	t.Run("auto degrades to the worktree", func(t *testing.T) {
		res, err := dispatchWorldPreflightFailure(
			subagent.WorldResolution{Requested: subagent.WorldAuto, Resolved: subagent.WorldContainer},
			cause,
		)
		if err != nil {
			t.Fatalf("auto must not fail on a preflight failure: %v", err)
		}
		if res.Resolved != subagent.WorldWorktree || !res.Degraded {
			t.Fatalf("got %+v, want a degraded worktree", res)
		}
		if !strings.Contains(res.Reason, "daemon unreachable") {
			t.Errorf("reason %q must name the failure", res.Reason)
		}
	})

	t.Run("container fails closed", func(t *testing.T) {
		res, err := dispatchWorldPreflightFailure(
			subagent.WorldResolution{Requested: subagent.WorldContainer, Resolved: subagent.WorldContainer},
			cause,
		)
		if err == nil {
			t.Fatalf("an explicit container request must fail closed, got %+v", res)
		}
		if !errors.Is(err, cause) {
			t.Errorf("error = %v, want it to wrap the preflight failure", err)
		}
		if !strings.Contains(err.Error(), "container") {
			t.Errorf("error = %q, want it to name the world", err)
		}
	})
}
