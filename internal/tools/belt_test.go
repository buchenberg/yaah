package tools

import (
	"runtime"
	"slices"
	"testing"
)

// TestBelt_AllowsAndRefuses pins the belt's contract: an unset belt
// admits everything, a configured belt admits only what it names, and
// every refusal is recorded so the session can report it.
func TestBelt_AllowsAndRefuses(t *testing.T) {
	empty := NewBelt(nil)
	if !empty.Allows("anything") {
		t.Error("an empty belt must admit every tool")
	}
	if len(empty.Refused()) != 0 {
		t.Errorf("an empty belt must refuse nothing, got %v", empty.Refused())
	}

	belt := NewBelt([]string{"read", "bash"})
	if !belt.Allows("read") || !belt.Allows("bash") {
		t.Error("allowlisted tools must be admitted")
	}
	if belt.Allows("write") {
		t.Error("a tool outside the belt must not be admitted")
	}
	belt.Refuse("write")
	belt.Refuse("read")
	belt.Refuse("write")
	if got := belt.Refused(); len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Errorf("refused = %v, want [read write] (deduplicated, sorted)", got)
	}
}

// TestBelt_UnknownNames pins the typo check: an allowlist name that
// matched no tool is reported, not silently ignored.
func TestBelt_UnknownNames(t *testing.T) {
	belt := NewBelt([]string{"read", "nope"})
	belt.Refuse("write")
	belt.Know("read", "bash")
	unknown := belt.Unknown()
	if len(unknown) != 1 || unknown[0] != "nope" {
		t.Errorf("unknown = %v, want [nope]", unknown)
	}
	// A tool the session knows about but did not install is not a typo.
	belt.Know("proposal")
	if got := belt.Unknown(); len(got) != 1 {
		t.Errorf("known-but-uninstalled tools must not read as typos, got %v", got)
	}
	if got := belt.Allowed(); len(got) != 2 || !slices.IsSorted(got) {
		t.Errorf("allowed = %v, want a sorted list of two", got)
	}
}

// TestRegistry_BeltRefusesAtRegistration is the core guarantee: a tool
// the belt does not admit is never installed — not at construction, and
// not when it is registered later (the runtime-wired tools register long
// after the leaf tools).
func TestRegistry_BeltRefusesAtRegistration(t *testing.T) {
	belt := NewBelt([]string{"read", "bash"})
	reg := NewRegistryWithBelt(belt)

	if reg.Get("write") != nil {
		t.Error("a tool outside the belt must never be installed")
	}
	if reg.Get("read") == nil {
		t.Error("allowlisted tools must be installed")
	}
	// bash is platform-gated: Windows has no bash, so the registry never
	// installs it and the belt records the absence.
	if runtime.GOOS != "windows" && reg.Get("bash") == nil {
		t.Error("bash must be installed where the platform has it")
	}

	// A late registration is refused through the same door.
	reg.Register(&WriteTool{})
	if reg.Get("write") != nil {
		t.Error("a late registration outside the belt must be refused")
	}
	if !slices.Contains(belt.Refused(), "write") {
		t.Errorf("the refusal must be recorded, got %v", belt.Refused())
	}
}

// TestRegistry_NoBeltKeepsEveryTool: the default registry is unchanged.
func TestRegistry_NoBeltKeepsEveryTool(t *testing.T) {
	reg := NewRegistry()
	if reg.Get("write") == nil || reg.Get("read") == nil {
		t.Error("the default registry must carry the full belt")
	}
}

// TestRegistry_AdmitWidensTheBelt pins the MCP seam (yaah-6mi9.1): a
// configured server's tools were not in the belt when it was built, so
// they are admitted at registration. Admit is the belt's one widening
// operation and retracts a stale refusal so the prompt cannot keep
// naming a tool the session now holds.
func TestRegistry_AdmitWidensTheBelt(t *testing.T) {
	belt := NewBelt([]string{"read"})
	reg := NewRegistryWithBelt(belt)

	// Before admission the registration is refused, exactly like any
	// late registration outside the belt.
	reg.Register(&WriteTool{})
	if reg.Get("write") != nil {
		t.Fatal("a registration outside the belt must be refused first")
	}

	// Admit-then-register is the honest order and the produced effect.
	reg.Admit("write")
	if !belt.Allows("write") {
		t.Error("Admit must widen the belt to the named tool")
	}
	// The stale refusal from the pre-admission attempt is retracted, so
	// Refused() cannot keep naming a tool the session is about to hold.
	if slices.Contains(belt.Refused(), "write") {
		t.Errorf("Admit must retract the stale refusal, got %v", belt.Refused())
	}
	reg.Register(&WriteTool{})
	if reg.Get("write") == nil {
		t.Error("an admitted registration must install the tool")
	}
}

// TestRegistry_AdmitOnAllowAll: an allow-all belt ignores Admit —
// everything is admitted already, and the accounting stays untouched.
func TestRegistry_AdmitOnAllowAll(t *testing.T) {
	belt := NewBelt(nil)
	reg := NewRegistry()
	reg.Admit("anything")
	if belt.Refused() != nil {
		t.Errorf("Admit on an allow-all belt must not record anything, got %v", belt.Refused())
	}
}
