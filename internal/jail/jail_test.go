package jail

import (
	"runtime"
	"slices"
	"sort"
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
)

type fakeBackend struct {
	containment.Backend
	name string
}

func (f fakeBackend) Name() string { return f.name }

// TestBackendFollowsDetectorEntry pins the pure selection function against
// all three shapes on every host: an entry yields its backend, an absent
// entry and a nil entry yield nil. Nothing is host-conditional and no package
// state is touched.
func TestBackendFollowsDetectorEntry(t *testing.T) {
	stub := func() containment.Backend { return fakeBackend{name: "any"} }

	present := map[string]func() containment.Backend{"linux": stub, "windows": stub}
	for _, goos := range []string{"linux", "windows"} {
		if b := backendFor(goos, present); b == nil || b.Name() != "any" {
			t.Errorf("backendFor(%q): = %v, want the stub backend", goos, b)
		}
	}

	// An entry the host lacks — Windows has no Landlock kernel and Linux has
	// no winacl token shape — is advisory, as is a nil entry.
	missing := map[string]func() containment.Backend{"windows": stub}
	for _, goos := range []string{"linux", "darwin"} {
		if b := backendFor(goos, missing); b != nil {
			t.Errorf("backendFor(%q) with no entry: = %v, want nil", goos, b)
		}
	}
	nilEntry := map[string]func() containment.Backend{"linux": nil}
	for _, goos := range []string{"linux", "darwin"} {
		if b := backendFor(goos, nilEntry); b != nil {
			t.Errorf("backendFor(%q) with a nil entry: = %v, want nil", goos, b)
		}
	}
	// A table missing the host entirely, e.g. macOS, selects nothing.
	if b := backendFor("darwin", present); b != nil {
		t.Errorf("backendFor(%q): = %v, want nil — no detector entry", "darwin", b)
	}
}

// TestDetectorKeySet pins the exact production table: exactly the two members
// the containment family ships. A removed key would silently demote a
// platform to advisory; this names the drift.
func TestDetectorKeySet(t *testing.T) {
	got := make([]string, 0, len(detectors))
	for k := range detectors {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"linux", "windows"}
	if !slices.Equal(got, want) {
		t.Errorf("detectors keys = %v, want exactly %v", got, want)
	}
}

// TestBackendOnThisHost pins Runtime's real selection against the member's
// own Detect on the platforms CI runs: a platform whose entry yields a
// backend gets one, and a platform whose mechanism is unavailable gets nil —
// no key wrongly substitutes for another's mechanism.
func TestBackendOnThisHost(t *testing.T) {
	switch runtime.GOOS {
	case "linux":
		if got, want := Backend() != nil, landlock.Detect() != nil; got != want {
			t.Errorf("Backend() != nil = %v, want %v (the host's Landlock availability)", got, want)
		}
	case "windows":
		// Windows asserts itself in the runner/doctor confinement tests: the
		// placement resolution exercises the winacl path end to end on CI.
		return
	default:
		if Backend() != nil {
			t.Errorf("Backend() = %v on %s, want nil (no detector entry)", Backend(), runtime.GOOS)
		}
	}
}
