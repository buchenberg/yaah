package runner

import (
	"context"
	"testing"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"

	"github.com/buchenberg/yaah/internal/gitsandbox"
)

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
		Writable:  "/tmp/wt",
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
	}
	if !found {
		t.Fatal("the confinement fact was not recorded on the scope")
	}

	// Nil scope or record is a silent no-op.
	recordConfinementFact(context.Background(), nil, &subAgentConfinement{Resolved: "advisory"})
	recordConfinementFact(context.Background(), root, nil)
}
