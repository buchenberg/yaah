package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/gitsandbox"
)

func rehydrateDelta() *GitDelta {
	return &GitDelta{Changes: []DeltaChange{
		{Path: "out.txt", Kind: deltaKindAdd, Mode: 0o644, Content: []byte("landed\n")},
	}}
}

func TestLoadProposalRegistry_RehydratesAndSettles(t *testing.T) {
	f := newDeltaFixture(t)
	sealed := f.add(t, "proposal:rehydrate", rehydrateDelta())

	reg, warnings := LoadProposalRegistry(f.repo, f.fx.mgr, f.rootID, f.sessionID)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	ps := reg.all()
	if len(ps) != 1 {
		t.Fatalf("proposals = %d, want 1", len(ps))
	}
	got := ps[0]
	if got.id != sealed.id || got.deltaDigest != sealed.deltaDigest {
		t.Fatalf("rehydrated = %+v, want id/digest from %+v", got, sealed)
	}
	if got.world != "container" || got.basisDigest != f.basisDigest {
		t.Fatalf("rehydrated world/basis = %q/%s", got.world, got.basisDigest)
	}
	if got.result != "wrote "+sealed.id {
		t.Errorf("result = %q", got.result)
	}
	if got.delta == nil || len(got.delta.Changes) != 1 {
		t.Fatalf("manifest = %+v", got.delta)
	}

	if _, err := reg.settleRetainedProposal(context.Background(), got.id, "select"); err != nil {
		t.Fatalf("select after rehydration: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(f.repo, "out.txt"))
	if err != nil || string(data) != "landed\n" {
		t.Fatalf("host out.txt = %q, %v", data, err)
	}
	if _, err := os.Stat(got.artifactDir); !os.IsNotExist(err) {
		t.Errorf("artifact dir should be consumed, stat err = %v", err)
	}
}

func TestLoadProposalRegistry_InterruptedSettlementIsQuarantined(t *testing.T) {
	f := newDeltaFixture(t)
	p := f.add(t, "proposal:interrupted", rehydrateDelta())
	if err := os.WriteFile(filepath.Join(p.artifactDir, proposalApplying), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, warnings := LoadProposalRegistry(f.repo, f.fx.mgr, f.rootID, f.sessionID)
	if len(reg.all()) != 0 {
		t.Fatal("an interrupted settlement must not be rehydrated")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "interrupted settlement") {
		t.Fatalf("warnings = %v, want one interrupted-settlement warning", warnings)
	}
}

func TestLoadProposalRegistry_ConsumesSettledArtifact(t *testing.T) {
	f := newDeltaFixture(t)
	p := f.add(t, "proposal:settled", rehydrateDelta())
	if err := os.WriteFile(filepath.Join(p.artifactDir, proposalSettled), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, warnings := LoadProposalRegistry(f.repo, f.fx.mgr, f.rootID, f.sessionID)
	if len(reg.all()) != 0 || len(warnings) != 0 {
		t.Fatalf("got %d proposals, warnings %v; want consumed silently", len(reg.all()), warnings)
	}
	if _, err := os.Stat(p.artifactDir); !os.IsNotExist(err) {
		t.Errorf("settled artifact should be removed, stat err = %v", err)
	}
}

func TestLoadProposalRegistry_RejectsTamperedManifest(t *testing.T) {
	f := newDeltaFixture(t)
	p := f.add(t, "proposal:tampered", rehydrateDelta())
	if err := os.WriteFile(filepath.Join(p.artifactDir, proposalDeltaFile), []byte(`{"changes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, warnings := LoadProposalRegistry(f.repo, f.fx.mgr, f.rootID, f.sessionID)
	if len(reg.all()) != 0 {
		t.Fatal("a manifest whose digest no longer matches metadata must not be rehydrated")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "digest") {
		t.Fatalf("warnings = %v, want one digest warning", warnings)
	}
}

func TestLoadProposalRegistry_IgnoresAnotherSession(t *testing.T) {
	f := newDeltaFixture(t)
	f.add(t, "proposal:other-session", rehydrateDelta())

	reg, warnings := LoadProposalRegistry(f.repo, f.fx.mgr, f.rootID, "some-other-session")
	if len(reg.all()) != 0 || len(warnings) != 0 {
		t.Fatalf("got %d proposals, warnings %v; want another session ignored", len(reg.all()), warnings)
	}
}

func TestSettleGitDelta_RecordsSettledFact(t *testing.T) {
	f := newDeltaFixture(t)
	p := f.add(t, "proposal:fact", rehydrateDelta())
	f.settle(t, p.id, "select")

	parent, ok := f.fx.mgr.Get(f.rootID)
	if !ok {
		t.Fatal("root scope missing")
	}
	slice, err := f.fx.store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, parent.OwnerID(), 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read owner prefix: %v", err)
	}
	found := false
	for _, id := range slice.FactIDs() {
		rec, ok := slice.FactsByID[id]
		if !ok {
			continue
		}
		if rec.GetEnvelope().SchemaRef == SchemaProposalSettled {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no %s fact on the host root owner path", SchemaProposalSettled)
	}
}

func TestLoadProposalRegistry_RestartRootRebinds(t *testing.T) {
	f := newDeltaFixture(t)
	f.add(t, "proposal:restart", rehydrateDelta())

	// A restart gives the session a new root scope id; the durable session id
	// must still find the artifact, and settlement must use the current root.
	root, err := f.fx.mgr.Create("restarted-root", gitsandbox.NewLocalSandbox(f.repo))
	if err != nil {
		t.Fatalf("create restarted root: %v", err)
	}
	reg, warnings := LoadProposalRegistry(f.repo, f.fx.mgr, root.ID(), f.sessionID)
	if len(warnings) != 0 || len(reg.all()) != 1 {
		t.Fatalf("proposals = %d, warnings %v; want one", len(reg.all()), warnings)
	}
	p := reg.all()[0]
	if p.parentID != root.ID() {
		t.Fatalf("parentID = %q, want the current root %q", p.parentID, root.ID())
	}
	if _, err := reg.settleRetainedProposal(context.Background(), p.id, "select"); err != nil {
		t.Fatalf("select after restart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "out.txt")); err != nil {
		t.Fatalf("host out.txt: %v", err)
	}
}
