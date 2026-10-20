package tools

import (
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
	unknown := belt.Unknown([]string{"read", "bash"})
	if len(unknown) != 1 || unknown[0] != "nope" {
		t.Errorf("unknown = %v, want [nope]", unknown)
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
	if reg.Get("read") == nil || reg.Get("bash") == nil {
		t.Error("allowlisted tools must be installed")
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
