package runner

import (
	"context"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"

	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"
	"github.com/buchenberg/yaah/internal/tools"
)

// degradedWorkspace reports a recorded downgrade, standing in for a lane whose
// backend passed its availability check and then failed to establish the jail.
// Embedding the interface keeps the stub to the one method under test.
type degradedWorkspace struct {
	tools.Workspace
	reason string
}

func (w degradedWorkspace) ConfinementDegraded() (string, bool) { return w.reason, w.reason != "" }

// healthyWorkspace is a workspace with no downgrade support at all, which is
// what a host-path (advisory) dispatch gets.
type healthyWorkspace struct{ tools.Workspace }

const degradedReason = "containment: fail-closed, confinement not established: writable root C:/world DENIES writes (profile too strict)"

// A dispatch that resolved to a jail but ran unconfined must say so on the scope:
// the pre-run fact records what was resolved, this second fact records what the
// body actually ran under, so a reader never mistakes a degraded lane for a
// jailed one.
func TestRecordConfinementDegradedFact(t *testing.T) {
	mgr, store := newScopeManagerForTest(t)
	root, err := mgr.Create("root", goatherdgit.NewLocalGitSandbox(newRunnerTestGitRepo(t), goatherdgit.WithCleanPolicy(goatherdgit.CleanPreserveUntracked)))
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	ctx := context.Background()

	conf := &subAgentConfinement{
		Requested: "auto",
		Resolved:  "paddock",
		Backend:   "winacl",
		Tier:      "restricted-token",
		Enforced:  true,
		Writable:  "C:/world",
		Network:   "broker",
		Broker:    41234,
		ws:        degradedWorkspace{reason: degradedReason},
	}
	recordConfinementFact(ctx, root, conf)
	recordConfinementDegradedFact(ctx, root, conf)

	slice, err := store.ReadOwnerPrefix(ctx, shepherdtrace.TrustedReadContext, root.OwnerID(), 100, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var found bool
	for _, id := range slice.FactIDs() {
		rec, ok := slice.FactsByID[id].(shepherdtrace.Record)
		if !ok || rec.Envelope.SchemaRef != "yaah.subagent.confinement.degraded.v1" {
			continue
		}
		found = true
		p := rec.Body.Payload
		// The post-run truth overrides the hope: advisory, not jailed.
		if p["resolved"] != "advisory" {
			t.Errorf("resolved = %v, want advisory", p["resolved"])
		}
		if p["enforced"] != false {
			t.Errorf("enforced = %v, want false", p["enforced"])
		}
		if p["degraded_reason"] != degradedReason {
			t.Errorf("degraded_reason = %v, want the workspace's own explanation", p["degraded_reason"])
		}
		// Everything else is carried over so the degraded fact stands alone.
		if p["requested"] != "auto" || p["backend"] != "winacl" || p["tier"] != "restricted-token" {
			t.Errorf("pre-run fields not carried over: %v", p)
		}
		if p["writable_root"] != "C:/world" {
			t.Errorf("writable_root = %v, want C:/world", p["writable_root"])
		}
		if p["network"] != "broker" {
			t.Errorf("network = %v, want broker", p["network"])
		}
		if got := toInt(p["broker_port"]); got != 41234 {
			t.Errorf("broker_port = %v (%T), want 41234", p["broker_port"], p["broker_port"])
		}
	}
	if !found {
		t.Fatal("no yaah.subagent.confinement.degraded.v1 fact was recorded for a degraded lane")
	}
}

// The fact is only for lanes that actually lost their jail: a healthy workspace,
// an advisory dispatch with no workspace, and a nil scope must all stay silent.
func TestRecordConfinementDegradedFact_SilentWhenJailHeldOrAbsent(t *testing.T) {
	mgr, store := newScopeManagerForTest(t)
	root, err := mgr.Create("root", goatherdgit.NewLocalGitSandbox(newRunnerTestGitRepo(t), goatherdgit.WithCleanPolicy(goatherdgit.CleanPreserveUntracked)))
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	ctx := context.Background()

	recordConfinementDegradedFact(ctx, root, &subAgentConfinement{
		Requested: "auto", Resolved: "paddock", Backend: "landlock", Enforced: true,
		ws: healthyWorkspace{},
	})
	recordConfinementDegradedFact(ctx, root, &subAgentConfinement{Requested: "off", Resolved: "advisory"})
	recordConfinementDegradedFact(ctx, root, nil)
	recordConfinementDegradedFact(ctx, nil, &subAgentConfinement{ws: degradedWorkspace{reason: degradedReason}})

	slice, err := store.ReadOwnerPrefix(ctx, shepherdtrace.TrustedReadContext, root.OwnerID(), 100, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	for _, id := range slice.FactIDs() {
		rec, ok := slice.FactsByID[id].(shepherdtrace.Record)
		if ok && rec.Envelope.SchemaRef == "yaah.subagent.confinement.degraded.v1" {
			t.Fatalf("recorded a degraded fact for a lane that never lost its jail: %v", rec.Body.Payload)
		}
	}
}
