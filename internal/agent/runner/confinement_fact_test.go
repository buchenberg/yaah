package runner

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"

	"github.com/buchenberg/yaah/internal/gitsandbox"
)

// toInt reads a payload number whichever way it survived the round trip.
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		return -1
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
		return -1
	default:
		return -1
	}
}

// Plan 7 Phase 3: the resolved jail placement lands on the sub-agent's scope, so
// an advisory run is never read as jailed and the writable root is legible.
func TestRecordConfinementFact(t *testing.T) {
	mgr, store := newScopeManagerForTest(t)
	root, err := mgr.Create("root", gitsandbox.NewLocalSandbox(newRunnerTestGitRepo(t)))
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}

	recordConfinementFact(context.Background(), root, &subAgentConfinement{
		Requested: "auto",
		Resolved:  "paddock",
		Backend:   "landlock",
		Tier:      "native-syscall-deny",
		Enforced:  true,
		Writable:  "/tmp/wt",
		Network:   "broker",
		Broker:    41234,
	})

	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, root.OwnerID(), 100, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	found := false
	for _, id := range slice.FactIDs() {
		rec, ok := slice.FactsByID[id].(shepherdtrace.Record)
		if !ok || rec.Envelope.SchemaRef != "yaah.subagent.confinement.v1" {
			continue
		}
		found = true
		if rec.Body.Payload["resolved"] != "paddock" {
			t.Errorf("resolved = %v, want paddock", rec.Body.Payload["resolved"])
		}
		if rec.Body.Payload["writable_root"] != "/tmp/wt" {
			t.Errorf("writable_root = %v, want /tmp/wt", rec.Body.Payload["writable_root"])
		}
		if rec.Body.Payload["backend"] != "landlock" {
			t.Errorf("backend = %v, want landlock", rec.Body.Payload["backend"])
		}
		if rec.Body.Payload["enforced"] != true {
			t.Errorf("enforced = %v, want true", rec.Body.Payload["enforced"])
		}
		// The egress posture rides the jail, so it is recorded with it.
		if rec.Body.Payload["network"] != "broker" {
			t.Errorf("network = %v, want broker", rec.Body.Payload["network"])
		}
		// The payload may round-trip through JSON (float64) or keep the Go type.
		if got := toInt(rec.Body.Payload["broker_port"]); got != 41234 {
			t.Errorf("broker_port = %v (%T), want 41234", rec.Body.Payload["broker_port"], rec.Body.Payload["broker_port"])
		}
	}
	if !found {
		t.Fatal("the confinement fact was not recorded on the scope")
	}

	// Nil scope or record is a silent no-op.
	recordConfinementFact(context.Background(), nil, &subAgentConfinement{Resolved: "advisory"})
	recordConfinementFact(context.Background(), root, nil)
}
