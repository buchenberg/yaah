package tools

import (
	"slices"
	"testing"
)

// The curated registry (plan §3, Option A): the isolated session's
// registry omits the host-only leaf tools so Registry.SetWorkspace's gate
// passes. The runtime-wired host-only tools (role, background_process,
// supervised_task) are omitted at the wiring's registration sites; these
// tests pin the leaf-level half of the contract.

// TestNewIsolatedRegistry_OmitsExactlyHostOnlyLeaves is the leaf-level
// activation matrix (plan §8): local and isolated registries differ by
// exactly the host-only leaf tools, and nothing else.
func TestNewIsolatedRegistry_OmitsExactlyHostOnlyLeaves(t *testing.T) {
	local := NewRegistry()
	isolated := NewIsolatedRegistry()

	localNames := local.List()
	isolatedNames := isolated.List()

	onlyLocal := []string{}
	for _, name := range localNames {
		if !slices.Contains(isolatedNames, name) {
			onlyLocal = append(onlyLocal, name)
		}
	}
	onlyIsolated := []string{}
	for _, name := range isolatedNames {
		if !slices.Contains(localNames, name) {
			onlyIsolated = append(onlyIsolated, name)
		}
	}

	if len(onlyIsolated) != 0 {
		t.Errorf("isolated registry has tools local mode lacks: %v", onlyIsolated)
	}
	if len(onlyLocal) != 1 || onlyLocal[0] != "go_refactor" {
		t.Errorf("local-only tools = %v, want exactly [go_refactor] — the sole host-only leaf tool", onlyLocal)
	}
}

// TestNewIsolatedRegistry_Selectable pins the point of the curation: both
// diagnostic lists SetWorkspace refuses on are empty, and the gate
// actually accepts an isolated workspace for this registry.
func TestNewIsolatedRegistry_Selectable(t *testing.T) {
	reg := NewIsolatedRegistry()
	if unmigrated := reg.UnmigratedFilesystemTools(); len(unmigrated) != 0 {
		t.Errorf("unmigrated = %v, want none", unmigrated)
	}
	if hostOnly := reg.HostOnlyFilesystemTools(); len(hostOnly) != 0 {
		t.Errorf("hostOnly = %v, want none", hostOnly)
	}
	if err := reg.SetWorkspace(newSandboxWS(t, nil)); err != nil {
		t.Errorf("SetWorkspace on the curated registry: %v", err)
	}
}

// TestHostOnlyLeafClassification pins the classification the filter and
// the SetWorkspace gate share: today exactly one leaf tool is host-only.
// A new host-only leaf tool must update this test consciously — it is
// omitted from isolated registries automatically (same marker), but the
// wiring's prompt delta and omitted-tool record enumerate names too.
func TestHostOnlyLeafClassification(t *testing.T) {
	for name := range leafTools {
		got := isHostOnlyTool(NewLeafTool(name))
		want := name == "go_refactor"
		if got != want {
			t.Errorf("isHostOnlyTool(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestLeafToolIsHostOnly pins the per-name form of the host-only filter
// (the one the sub-agent registry uses, plan §1.3/D2): it must agree with
// the marker classification above, and unknown names are simply not
// host-only — the caller never registers them anyway.
func TestLeafToolIsHostOnly(t *testing.T) {
	for name := range leafTools {
		if got, want := LeafToolIsHostOnly(name), name == "go_refactor"; got != want {
			t.Errorf("LeafToolIsHostOnly(%s) = %v, want %v", name, got, want)
		}
	}
	if LeafToolIsHostOnly("not_a_tool") {
		t.Error("an unknown leaf name must not be host-only")
	}
}
