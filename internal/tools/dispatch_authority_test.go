package tools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
)

func TestResolveDispatchAuthority_Network(t *testing.T) {
	cases := []struct {
		mode  string
		hosts []string
		want  string
		wh    []string
	}{
		{mode: "", want: "host"},
		{mode: "allow_all", want: "host"},
		{mode: "deny_all", want: "none"},
		{mode: "broker", hosts: []string{"api.example.com"}, want: "broker", wh: []string{"api.example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			rt := RetainedRuntime{
				World:           "container",
				Network:         tc.mode,
				NetworkHosts:    tc.hosts,
				ContainerWorlds: true,
				DispatchWorlds:  &DispatchWorlds{Config: sessionTestConfig()},
			}
			got, err := resolveDispatchAuthority(rt, "developer")
			if err != nil {
				t.Fatalf("resolveDispatchAuthority: %v", err)
			}
			if got.network != tc.want {
				t.Errorf("network = %q, want %q", got.network, tc.want)
			}
			if !reflect.DeepEqual(got.brokerHosts, tc.wh) {
				t.Errorf("broker hosts = %v, want %v", got.brokerHosts, tc.wh)
			}
			if got.shaped() {
				t.Error("a network-only authority must not shape the workspace")
			}
		})
	}
}

func TestResolveDispatchAuthority_Grants(t *testing.T) {
	t.Run("read-write grants become writable roots", func(t *testing.T) {
		rt := RetainedRuntime{
			Network:         "allow_all",
			ContainerWorlds: true,
			Grants:          []config.GrantConfig{{Path: "src", Access: "readwrite"}, {Path: "docs", Access: "readonly"}},
			DispatchWorlds:  &DispatchWorlds{Config: sessionTestConfig()},
		}
		got, err := resolveDispatchAuthority(rt, "developer")
		if err != nil {
			t.Fatalf("resolveDispatchAuthority: %v", err)
		}
		if !reflect.DeepEqual(got.writableRoots, []string{"/workspace/src"}) {
			t.Errorf("writable roots = %v", got.writableRoots)
		}
		if got.maskWorkspace {
			t.Error("a writable grant must not mask the whole workspace")
		}
		if !got.shaped() {
			t.Error("read-write grants must shape the workspace")
		}
	})

	t.Run("all-read-only grants mask the workspace", func(t *testing.T) {
		rt := RetainedRuntime{
			Network:         "allow_all",
			ContainerWorlds: true,
			Grants:          []config.GrantConfig{{Path: "docs", Access: "readonly"}},
			DispatchWorlds:  &DispatchWorlds{Config: sessionTestConfig()},
		}
		got, err := resolveDispatchAuthority(rt, "developer")
		if err != nil {
			t.Fatalf("resolveDispatchAuthority: %v", err)
		}
		if len(got.writableRoots) != 0 {
			t.Errorf("writable roots = %v, want none", got.writableRoots)
		}
		if !got.maskWorkspace || !got.shaped() {
			t.Errorf("an all-read-only set must enable the mask: %+v", got)
		}
	})

	t.Run("role grants override session grants", func(t *testing.T) {
		rt := RetainedRuntime{
			Network:         "allow_all",
			ContainerWorlds: true,
			Grants:          []config.GrantConfig{{Path: "src", Access: "readwrite"}},
			GrantsForRole: func(role string) []config.GrantConfig {
				if role == "reviewer" {
					return []config.GrantConfig{{Path: "docs", Access: "readonly"}}
				}
				return nil
			},
			DispatchWorlds: &DispatchWorlds{Config: sessionTestConfig()},
		}
		got, err := resolveDispatchAuthority(rt, "reviewer")
		if err != nil {
			t.Fatalf("resolveDispatchAuthority: %v", err)
		}
		if !got.maskWorkspace || len(got.writableRoots) != 0 {
			t.Errorf("role override was not lowered: %+v", got)
		}
	})

	t.Run("absolute grants are refused", func(t *testing.T) {
		rt := RetainedRuntime{
			Network:         "allow_all",
			ContainerWorlds: true,
			Grants:          []config.GrantConfig{{Path: "/etc", Access: "readwrite"}},
			DispatchWorlds:  &DispatchWorlds{Config: sessionTestConfig()},
		}
		if _, err := resolveDispatchAuthority(rt, "developer"); err == nil {
			t.Fatal("an absolute grant must fail")
		} else if !strings.Contains(err.Error(), "world-relative") {
			t.Fatalf("error = %v, want it to name the relative rule", err)
		}
	})

	t.Run("escaping grants are refused", func(t *testing.T) {
		rt := RetainedRuntime{
			Network:         "allow_all",
			ContainerWorlds: true,
			Grants:          []config.GrantConfig{{Path: "../etc", Access: "readwrite"}},
			DispatchWorlds:  &DispatchWorlds{Config: sessionTestConfig()},
		}
		if _, err := resolveDispatchAuthority(rt, "developer"); err == nil {
			t.Fatal("a grant escaping the container root must fail")
		} else if !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("error = %v, want it to name the escape", err)
		}
	})
}

func TestDispatchAuthority_ApplyCopies(t *testing.T) {
	a := dispatchAuthority{
		writableRoots: []string{"/workspace/src"},
		maskWorkspace: true,
		network:       "broker",
		brokerHosts:   []string{"api.example.com"},
	}
	base := sessionTestConfig()
	got := a.apply(base)
	if !reflect.DeepEqual(got.WritableRoots, a.writableRoots) || !got.MaskWorkspace || got.Network != "broker" || !reflect.DeepEqual(got.BrokerHosts, a.brokerHosts) {
		t.Fatalf("apply did not lower authority: %+v", got)
	}
	// The session config is immutable; a dispatch must not leak its roots into
	// the next dispatch's view.
	if len(base.WritableRoots) != 0 || base.MaskWorkspace || base.Network != "" {
		t.Fatalf("apply mutated the session config: %+v", base)
	}
}
