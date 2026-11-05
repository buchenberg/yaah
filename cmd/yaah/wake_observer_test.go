package yaah

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/wake"
)

func TestWakeObserverEmitsHookEventsWithoutTargets(t *testing.T) {
	dir := t.TempDir()
	hooks := events.NewHookEmitter(dir, "sess")
	obs := newWakeObserver(hooks)

	// The targets are deliberately distinctive: they must not appear in the
	// JSONL, only their kinds.
	httpSpec := wake.Spec{On: "http:https://secret.example/health"}
	ctx := obs.WaitStarted(context.Background(), httpSpec, time.Second)
	obs.WaitFinished(ctx, httpSpec, true, false, false, false, 5*time.Millisecond)

	fileInfo := wake.WatchInfo{ID: "w1", On: "file:secret/path", State: wake.StateArmed}
	obs.WatchArmed(fileInfo)
	code := 0
	fileInfo.Evidence = &wake.Evidence{Kind: "command", ExitCode: &code}
	obs.WatchFired(fileInfo, false)
	obs.WatchCancelled("w2")
	hooks.Close()

	raw, err := os.ReadFile(filepath.Join(dir, "sess.jsonl"))
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	for _, leaked := range []string{"secret.example", "secret/path"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("hook log leaked the predicate target %q:\n%s", leaked, raw)
		}
	}

	var got []events.HookEvent
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev events.HookEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode hook line %q: %v", line, err)
		}
		got = append(got, ev)
	}
	want := []events.HookEventType{
		events.WaitStart, events.WaitEnd, events.WatchArm, events.WatchFire, events.WatchCancel,
	}
	if len(got) != len(want) {
		t.Fatalf("hook events = %+v, want %d events", got, len(want))
	}
	for i, typ := range want {
		if got[i].Event != typ {
			t.Errorf("event %d = %s, want %s", i, got[i].Event, typ)
		}
	}
	if got[0].WakeKind != "http" || got[1].WakeKind != "http" || got[1].WakeState != "fired" {
		t.Errorf("wait lifecycle = %+v", got[:2])
	}
	if got[2].WakeID != "w1" || got[3].WakeState != "fired" || got[4].WakeState != "cancelled" {
		t.Errorf("watch lifecycle = %+v", got[2:])
	}
}
