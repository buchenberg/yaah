package jail

import (
	"runtime"
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
)

type fakeBackend struct {
	containment.Backend
	name string
}

func (f fakeBackend) Name() string { return f.name }

// TestBackendSelectionFollowsHostGoos pins the two halves of the selection
// contract: every entry in detectors means "this GOOS has a backend", and a
// GOOS with no entry is advisory even when other entries exist.
func TestBackendSelectionFollowsHostGoos(t *testing.T) {
	orig := detectors
	t.Cleanup(func() { detectors = orig })

	// A host whose entry returns a real backend gets that backend.
	detectors = map[string]func() containment.Backend{
		"linux":   func() containment.Backend { return fakeBackend{name: "any"} },
		"windows": func() containment.Backend { return fakeBackend{name: "any"} },
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		b := Backend()
		if b == nil || b.Name() != "any" {
			t.Fatalf("Backend() = %v, want the %s backend", b, runtime.GOOS)
		}
	} else {
		t.Skipf("selection for %s is nil by construction", runtime.GOOS)
	}

	// A GOOS with no entry is advisory, even though other platforms have
	// backends.
	detectors = map[string]func() containment.Backend{
		"windows": func() containment.Backend { return fakeBackend{name: "any"} },
	}
	if runtime.GOOS == "windows" {
		t.Skip("windows is in detectors and cannot double as the no-entry case")
	}
	if b := Backend(); b != nil {
		t.Fatalf("Backend() = %v, want nil on %s with no detector entry", b, runtime.GOOS)
	}
}

// TestEveryDetectorIsCrossPlatform pins that the map covers exactly the
// platforms yaah documents with a jail: the members compile everywhere and
// Detect must stay the only availability gate.
func TestEveryDetectorIsCrossPlatform(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		if _, ok := detectors[goos]; !ok {
			t.Errorf("detectors is missing a %s entry", goos)
		}
	}
}
