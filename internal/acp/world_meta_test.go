package acp

import (
	"encoding/json"
	"testing"

	"github.com/buchenberg/yaah/internal/worldinfo"
)

// TestServer_sessionNewAndLoadCarryWorldMeta pins the `_meta` extension: the
// posture rides session/new and session/load results, and a zero status omits
// the block entirely.
func TestServer_sessionNewAndLoadCarryWorldMeta(t *testing.T) {
	status := worldinfo.Status{
		Backend:       "local",
		Mode:          "retained",
		Containment:   "auto",
		Network:       "allow_all",
		WorldReq:      "auto",
		ProposalWorld: "worktree",
		ReportWorld:   "worktree",
	}
	sess := &fakeSession{id: "sess-world", world: status}
	h := newACPHarness(t, sess)
	initialize(t, h)

	id := newSession(t, h)
	if err := h.send("session/load", 3, loadParams(t, h, id)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	var loaded SessionLoadResult
	if err := json.Unmarshal(msg.Result, &loaded); err != nil {
		t.Fatalf("session/load result: %v", err)
	}
	if loaded.Meta == nil || loaded.Meta.World == nil {
		t.Fatalf("session/load _meta missing: %s", msg.Result)
	}
	if loaded.Meta.World.Backend != "local" || loaded.Meta.World.Resolved != "worktree" || loaded.Meta.World.World != "auto" {
		t.Fatalf("session/load world = %+v", loaded.Meta.World)
	}
}

func TestServer_zeroWorldStatusOmitsMeta(t *testing.T) {
	h := newACPHarness(t, &fakeSession{id: "sess-noworld"})
	initialize(t, h)
	cwd, _ := json.Marshal(h.cwd)
	if err := h.send("session/new", 2, `{"cwd":`+string(cwd)+`,"mcpServers":[]}`); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	var res SessionNewResult
	if err := json.Unmarshal(msg.Result, &res); err != nil {
		t.Fatalf("session/new result: %v", err)
	}
	if res.Meta != nil {
		t.Fatalf("zero world status emitted _meta: %s", msg.Result)
	}
}

// TestWorldMetaOf_ReportLaneWhenItDiffers pins the report lane on the block: a
// container session resolves proposals shared but reports to a sibling
// container, and a client must not be told only half the posture.
func TestWorldMetaOf_ReportLaneWhenItDiffers(t *testing.T) {
	m := worldMetaOf(worldinfo.Status{
		Backend:       "containerd",
		Isolated:      true,
		Mode:          "retained",
		Containment:   "auto",
		Network:       "allow_all",
		WorldReq:      "auto",
		ProposalWorld: "shared",
		ReportWorld:   "container",
	})
	if m == nil || m.World == nil {
		t.Fatal("world meta missing")
	}
	if m.World.Resolved != "shared" || m.World.Report != "container" {
		t.Fatalf("world meta = %+v, want resolved shared and report container", m.World)
	}
}
