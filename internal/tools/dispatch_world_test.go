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

	t.Run("a wired dispatch setup lets an auto proposal use the container", func(t *testing.T) {
		rt := RetainedRuntime{
			World:           "auto",
			ContainerWorlds: true,
			DispatchWorlds:  &DispatchWorlds{},
			Containment:     "auto",
			Network:         "allow_all",
		}
		res, err := resolveDispatchWorld(rt, "developer", false)
		if err != nil {
			t.Fatalf("resolveDispatchWorld: %v", err)
		}
		if res.Resolved != subagent.WorldContainer || res.Degraded {
			t.Fatalf("got %+v, want an undegraded container", res)
		}
	})

	t.Run("explicit container keeps a proposal on the container world", func(t *testing.T) {
		rt := RetainedRuntime{
			World:           "container",
			ContainerWorlds: true,
			DispatchWorlds:  &DispatchWorlds{},
			Containment:     "auto",
			Network:         "allow_all",
		}
		res, err := resolveDispatchWorld(rt, "developer", false)
		if err != nil {
			t.Fatalf("resolveDispatchWorld: %v", err)
		}
		if res.Resolved != subagent.WorldContainer || res.Degraded {
			t.Fatalf("got %+v, want an undegraded container", res)
		}
	})

	t.Run("a shipped lane reports a missing setup as a degradation", func(t *testing.T) {
		rt := RetainedRuntime{World: "auto", ContainerWorlds: true, Containment: "auto", Network: "allow_all"}
		res, err := resolveDispatchWorld(rt, "developer", false)
		if err != nil {
			t.Fatalf("resolveDispatchWorld: %v", err)
		}
		if res.Resolved != "worktree" || !res.Degraded {
			t.Fatalf("got %+v, want a degraded worktree", res)
		}
		if !strings.Contains(res.Reason, "settlement") {
			t.Fatalf("reason %q must name the blocker", res.Reason)
		}
	})

	t.Run("a restrictive egress posture refuses a container world", func(t *testing.T) {
		rt := RetainedRuntime{
			World:           "auto",
			ContainerWorlds: true,
			DispatchWorlds:  &DispatchWorlds{},
			Containment:     "auto",
			Network:         "deny_all",
		}
		res, err := resolveDispatchWorld(rt, "reviewer", true)
		if err != nil {
			// A host with no usable syscall jail (for example macOS without a
			// Seatbelt backend) has no legal world for deny_all: the worktree
			// cannot enforce it either. The container must still be refused
			// for the network reason, not chosen.
			if !strings.Contains(err.Error(), "network") {
				t.Fatalf("error = %q, want it to name the network posture", err)
			}
		} else if res.Resolved != subagent.WorldWorktree || !res.Degraded {
			t.Fatalf("got %+v, want a degraded worktree", res)
		} else if !strings.Contains(res.Reason, "network") {
			t.Fatalf("reason %q must name the network posture", res.Reason)
		}

		explicit := rt
		explicit.World = "container"
		if _, err := resolveDispatchWorld(explicit, "reviewer", true); err == nil {
			t.Fatal("explicit container with deny_all must fail closed")
		} else if !strings.Contains(err.Error(), "network") {
			t.Fatalf("error = %q, want it to name the network posture", err)
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

// TestWorldFactPayloadShape pins the trace fact's key set to the parity ledger.
func TestWorldFactPayloadShape(t *testing.T) {
	got := worldFactPayload(subagent.WorldResolution{
		Requested: subagent.WorldAuto,
		Resolved:  subagent.WorldWorktree,
		Degraded:  true,
		Reason:    "container unavailable",
	})
	want := map[string]bool{"requested": true, "resolved": true, "degraded": true, "reason": true}
	if len(got) != len(want) {
		t.Fatalf("payload keys = %v, want %v", got, want)
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected world-fact key %q", k)
		}
	}
	if got["requested"] != "auto" || got["resolved"] != "worktree" || got["degraded"] != true {
		t.Errorf("payload = %v", got)
	}
}

func TestResolveDispatchWorld_ContainerSessionSibling(t *testing.T) {
	rt := RetainedRuntime{
		World:           "auto",
		ContainerWorlds: true,
		Containment:     "auto",
		Network:         "allow_all",
		DispatchWorlds: &DispatchWorlds{
			SessionSandbox: &SessionSandbox{},
		},
	}

	report, err := resolveDispatchWorld(rt, "reviewer", true)
	if err != nil {
		t.Fatalf("report dispatch: %v", err)
	}
	if report.Resolved != subagent.WorldContainer {
		t.Fatalf("report resolved = %q, want container", report.Resolved)
	}

	if _, err := resolveDispatchWorld(rt, "developer", false); err == nil {
		t.Fatal("a container-session proposal must not resolve to a worktree or a retained container")
	}
}
