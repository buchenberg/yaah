package tools

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/worldinfo"
)

// TestWorldInfoParityWithRuntimeLadder pins the session-level derivation
// (internal/worldinfo) to the per-dispatch ladder the runtime actually runs
// (resolveDispatchWorld). Doctor and every UI surface display worldinfo, so a
// drift here would describe a world a dispatch would not get. The availability
// record itself is single-sourced (TestDispatchWorldAvailabilityHasOne
// Producer); this stays as regression armor on the inputs each side derives.
func TestWorldInfoParityWithRuntimeLadder(t *testing.T) {
	linux := runtime.GOOS == "linux"
	withImage := func() *config.Config {
		cfg := &config.Config{}
		cfg.Workspace.Containerd.Image = "registry.example/yaah:1"
		return cfg
	}

	cases := []struct {
		name      string
		cfg       *config.Config
		isolated  bool
		container bool
	}{
		{name: "default local", cfg: &config.Config{}},
		{
			name: "explicit worktree",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				cfg.Agent.SubAgent.World = "worktree"
				return cfg
			}(),
		},
		{
			name: "explicit container without image",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				cfg.Agent.SubAgent.World = "container"
				return cfg
			}(),
		},
		{name: "image offers container", cfg: withImage(), container: linux},
		{
			name: "image with explicit worktree",
			cfg: func() *config.Config {
				cfg := withImage()
				cfg.Agent.SubAgent.World = "worktree"
				return cfg
			}(),
			container: linux,
		},
		{
			name: "container session",
			cfg: func() *config.Config {
				cfg := withImage()
				cfg.Workspace.Backend = config.BackendContainerd
				return cfg
			}(),
			isolated:  true,
			container: linux,
		},
		{
			name: "paddock placement",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				cfg.Agent.SubAgent.Containment = "paddock"
				return cfg
			}(),
		},
		{
			name: "restrictive egress",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				cfg.Agent.SubAgent.Network = "deny_all"
				return cfg
			}(),
		},
		{
			name: "broker egress",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				cfg.Agent.SubAgent.Network = "broker"
				cfg.Agent.SubAgent.NetworkHosts = []string{"a.example"}
				return cfg
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := RetainedRuntime{
				World:       tc.cfg.Agent.SubAgent.World,
				Containment: tc.cfg.Agent.SubAgent.Containment,
				Network:     tc.cfg.Agent.SubAgent.Network,
				Grants:      tc.cfg.Agent.SubAgent.Grants,
				// Mirrors the composition root: dispatch worlds are wired
				// whenever Linux has an image; the session sandbox is only
				// non-nil in an isolated session.
				ContainerWorlds: tc.container,
			}
			if tc.container {
				rt.DispatchWorlds = &DispatchWorlds{Config: tc.cfg.Workspace}
				if tc.isolated {
					rt.DispatchWorlds.SessionSandbox = &SessionSandbox{}
				}
			}

			res := worldinfo.Resolve(tc.cfg, tc.isolated)

			want, wantErr := resolveDispatchWorld(rt, "developer", false)
			assertSameResolution(t, "proposal", res.Proposal, res.ProposalErr, want, wantErr)

			want, wantErr = resolveDispatchWorld(rt, "developer", true)
			assertSameResolution(t, "report", res.Report, res.ReportErr, want, wantErr)
		})
	}
}

func assertSameResolution(t *testing.T, lane string, got subagent.WorldResolution, gotErr error, want subagent.WorldResolution, wantErr error) {
	t.Helper()
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("%s: err = %v, runtime err = %v", lane, gotErr, wantErr)
	}
	if gotErr != nil {
		if gotErr.Error() != wantErr.Error() {
			t.Fatalf("%s: err = %q, runtime err = %q", lane, gotErr, wantErr)
		}
		return
	}
	if got.Resolved != want.Resolved || got.Degraded != want.Degraded || got.Reason != want.Reason {
		t.Fatalf("%s: got %+v, runtime %+v", lane, got, want)
	}
}

// TestWorldInfoParityWithRoleOverride pins a per-role world override to the
// runtime's role-aware request, so doctor's role presentation resolves the
// same world a dispatch would.
func TestWorldInfoParityWithRoleOverride(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.SubAgent.World = "worktree"
	cfg.Agent.SubAgent.Roles = map[string]config.RoleConfig{
		"developer": {World: "container"},
	}
	isolated := false
	rt := RetainedRuntime{
		World: cfg.Agent.SubAgent.World,
		WorldForRole: func(role string) string {
			if rc, ok := cfg.Agent.SubAgent.Roles[role]; ok {
				return rc.World
			}
			return ""
		},
		Containment: cfg.Agent.SubAgent.Containment,
		Network:     cfg.Agent.SubAgent.Network,
	}

	want, wantErr := resolveDispatchWorld(rt, "developer", false)
	got, gotErr := subagent.ResolveWorld(subagent.WorldRequest{
		Requested:   "container",
		Lane:        "proposal",
		Retained:    true,
		Containment: cfg.Agent.SubAgent.Containment,
		Network:     cfg.Agent.SubAgent.Network,
	}, worldinfo.Availability(cfg, isolated))
	assertSameResolution(t, "role developer", got, gotErr, want, wantErr)
}

// TestWorldInfoParityAvailabilityRecord pins the two input derivations — the
// config-derived one display surfaces use and the wiring-derived one a dispatch
// uses — to the record worldinfo.AvailabilityFor produces. Both sides resolve
// against that one builder, so this test fails if either starts inventing its
// own container facts.
func TestWorldInfoParityAvailabilityRecord(t *testing.T) {
	winning := func(mutate func(rt *RetainedRuntime)) RetainedRuntime {
		rt := RetainedRuntime{}
		mutate(&rt)
		return rt
	}

	cases := []struct {
		name     string
		cfg      *config.Config
		isolated bool
		rt       RetainedRuntime
	}{
		{
			name: "unwired local session",
			cfg:  &config.Config{},
			rt:   winning(func(*RetainedRuntime) {}),
		},
		{
			name: "wired dispatch setup",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				cfg.Workspace.Containerd.Image = "registry.example/yaah:1"
				return cfg
			}(),
			rt: winning(func(rt *RetainedRuntime) {
				rt.ContainerWorlds = true
				rt.DispatchWorlds = &DispatchWorlds{}
			}),
		},
		{
			name: "container session",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				cfg.Workspace.Backend = config.BackendContainerd
				cfg.Workspace.Containerd.Image = "registry.example/yaah:1"
				return cfg
			}(),
			isolated: true,
			rt: winning(func(rt *RetainedRuntime) {
				rt.ContainerWorlds = true
				rt.DispatchWorlds = &DispatchWorlds{SessionSandbox: &SessionSandbox{}}
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fromConfig := worldinfo.Availability(tc.cfg, tc.isolated)
			fromWiring := worldinfo.AvailabilityFor(worldinfo.ContainerSetup{
				Offered: tc.rt.ContainerWorlds,
				Retains: tc.rt.DispatchWorlds != nil,
				Session: tc.rt.DispatchWorlds != nil && tc.rt.DispatchWorlds.SessionSandbox != nil,
			}, fromConfig.Jail)
			if fromConfig != fromWiring {
				t.Fatalf("availability records diverged:\nconfig %+v\nwiring %+v", fromConfig, fromWiring)
			}
		})
	}
}

// TestDispatchWorldAvailabilityHasOneProducer asserts the single source: the
// runtime ladder must build its availability record with
// worldinfo.AvailabilityFor, and no non-test file in this package may carry a
// copy of the policy (its reason strings or a raw paddock probe).
func TestDispatchWorldAvailabilityHasOneProducer(t *testing.T) {
	const producer = "worldinfo.AvailabilityFor("
	retained, err := os.ReadFile("retained.go")
	if err != nil {
		t.Fatalf("read retained.go: %v", err)
	}
	if !strings.Contains(string(retained), producer) {
		t.Fatalf("retained.go must resolve against %s, not its own record", producer)
	}

	duplicated := []string{
		"container settlement is unavailable in this session",
		"a container session has no host worktree",
		"per-dispatch container worlds need workspace.containerd.image",
		"subagent.WorldAvailability{",
		"jail.Backend()",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, dup := range duplicated {
			if strings.Contains(string(src), dup) {
				t.Errorf("%s duplicates the availability policy (%q); use worldinfo.%s", name, dup, "AvailabilityFor / JailAvailable")
			}
		}
	}
}
