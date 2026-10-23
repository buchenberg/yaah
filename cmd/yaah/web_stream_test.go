package yaah

import (
	"strings"
	"testing"
)

// TestReleaseStream checks the teardown ownership rule: a handler whose
// request was cancelled after a reload installed a newer view must not clear
// the successor's state. Clearing it left the live client with a nil view and
// a nil ctrlCh, so approvals and tool events stopped arriving and the composer
// stayed wedged — the symptom in yaah-8z0.
func TestReleaseStream(t *testing.T) {
	t.Run("superseded stream leaves the newer view installed", func(t *testing.T) {
		oldView := &sseView{}
		newView := &sseView{}
		ws := &webServer{view: newView}

		if owned := ws.releaseStream(oldView); owned {
			t.Fatal("superseded stream claimed ownership")
		}
		if ws.view != newView {
			t.Fatal("superseded teardown cleared the live view")
		}
	})

	t.Run("owning stream clears the view", func(t *testing.T) {
		view := &sseView{}
		ws := &webServer{view: view}

		if owned := ws.releaseStream(view); !owned {
			t.Fatal("owning stream did not claim ownership")
		}
		if ws.view != nil {
			t.Fatalf("view = %v, want nil after owning teardown", ws.view)
		}
	})

	t.Run("teardown of an already released view is not ownership", func(t *testing.T) {
		view := &sseView{}
		ws := &webServer{view: view}

		if owned := ws.releaseStream(view); !owned {
			t.Fatal("first teardown should own the view")
		}
		if owned := ws.releaseStream(view); owned {
			t.Fatal("second teardown should not claim ownership")
		}
	})
}

// TestSSEWireStateFrame pins the catch-up frame's shape: running is a pointer so
// it is present on ctrl.state and absent from every other event. The client
// reconciles its live flag from it, which is what stops a reconnect from
// leaving the composer permanently busy (yaah-a8y).
func TestSSEWireStateFrame(t *testing.T) {
	running := true
	frame := string(marshalWire(sseWireEvent{Type: "ctrl.state", Running: &running}))
	if want := `"running":true`; !strings.Contains(frame, want) {
		t.Fatalf("ctrl.state frame = %s, want it to contain %s", frame, want)
	}

	other := string(marshalWire(sseWireEvent{Type: "done"}))
	if strings.Contains(other, "running") {
		t.Fatalf("done frame = %s, want no running field", other)
	}
}
