package yaah

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
)

func TestTraceConflictsAggregate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	dir := filepath.Join(home, "traces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setTestTraceConfig(t, home, dir)

	path := filepath.Join(dir, "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	appendFact := func(owner, schema, kind string, mode shepherdtrace.RecordMode, payload map[string]any) {
		t.Helper()
		_, err := store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
			AppendIntentID: owner + ":" + schema + ":" + kind,
			Groups: []shepherdtrace.AppendGroup{{
				TraceOwnerID: owner,
				FactDrafts: []shepherdtrace.RecordDraft{{
					Mode:      mode,
					SchemaRef: schema,
					KindLabel: kind,
					Payload:   payload,
				}},
			}},
		})
		if err != nil {
			t.Fatalf("append %s: %v", schema, err)
		}
	}

	appendFact("sub-a1", "yaah.subagent.usage.v1", "subagent:usage", shepherdtrace.Capture, map[string]any{"lane": "retained", "parent_session": "sess-a"})
	appendFact("sub-a2", "yaah.subagent.usage.v1", "subagent:usage:2", shepherdtrace.Capture, map[string]any{"lane": "shared", "parent_session": "sess-a"})
	appendFact("sess-a", tools.SchemaProposalSealed, "proposal", shepherdtrace.Declaration, map[string]any{"proposal_id": "p1"})
	appendFact("sub-a1", "goatherd.merge.proposed.v1", "merge:proposed", shepherdtrace.Declaration, map[string]any{"proposal_id": "p1"})
	appendFact("sess-a", tools.SchemaProposalConflict, "conflict", shepherdtrace.Declaration, map[string]any{
		"kind": "same_path_divergent", "paths": []string{"a.txt", "b.txt"}, "proposals": []string{"p1", "p2"},
	})
	appendFact("sub-b1", "yaah.subagent.usage.v1", "subagent:usage", shepherdtrace.Capture, map[string]any{"lane": "shared", "parent_session": "sess-b"})
	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if err := db.RecordTraceOwner("sub-old", "sess-a", "worker"); err != nil {
		t.Fatalf("record legacy owner: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close state db: %v", err)
	}
	appendFact("sub-old", "yaah.subagent.usage.v1", "subagent:usage:old", shepherdtrace.Capture, map[string]any{"lane": "shared"})
	appendFact("sess-b", tools.SchemaProposalConflict, "conflict", shepherdtrace.Declaration, map[string]any{
		"kind": "delete_modify", "paths": []string{"a.txt"}, "proposals": []string{"p3", "p4"},
	})
	store.Close(context.Background())

	var buf bytes.Buffer
	cmd := newShepherdTraceConflictsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var got conflictAggregate
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, buf.String())
	}
	if got.Sessions != 2 || got.Conflicts != 2 || got.ConflictSessions != 2 {
		t.Errorf("sessions/conflicts = %d/%d/%d, want 2/2/2 (report %+v)", got.Sessions, got.Conflicts, got.ConflictSessions, got)
	}
	if got.Dispatches["retained"] != 1 || got.Dispatches["shared"] != 3 {
		t.Errorf("dispatches = %+v, want retained=1 shared=3", got.Dispatches)
	}
	if got.SealedProposals != 2 || got.ConflictProposals != 4 {
		t.Errorf("sealed/proposals = %d/%d, want 2/4", got.SealedProposals, got.ConflictProposals)
	}
	if got.FanoutSessions != 1 || got.FanoutSessionsWithConflicts != 1 {
		t.Errorf("fanout = %d/%d, want 1/1", got.FanoutSessions, got.FanoutSessionsWithConflicts)
	}
	if len(got.RepeatedPaths) != 1 || got.RepeatedPaths[0].Path != "a.txt" || got.RepeatedPaths[0].Count != 2 {
		t.Errorf("repeated paths = %+v, want a.txt×2", got.RepeatedPaths)
	}
	if got.Kinds["same_path_divergent"] != 1 || got.Kinds["delete_modify"] != 1 {
		t.Errorf("kinds = %+v, want both conflict kinds", got.Kinds)
	}
}

func TestTraceConflictsSessionWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	dir := filepath.Join(home, "traces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setTestTraceConfig(t, home, dir)
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: "one",
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: "sub-only",
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode: shepherdtrace.Capture, SchemaRef: "yaah.subagent.usage.v1", KindLabel: "subagent:usage",
				Payload: map[string]any{"lane": "retained", "parent_session": "sess-only"},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close(context.Background())

	var buf bytes.Buffer
	cmd := newShepherdTraceConflictsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--session", "sess-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("session sess-only")) || !bytes.Contains(buf.Bytes(), []byte("dispatches")) {
		t.Errorf("windowed report = %s", buf.String())
	}
}
