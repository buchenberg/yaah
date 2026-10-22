package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/skills"
)

func newSkillTool(t *testing.T, readOnly bool) (*SkillTool, *memory.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := memory.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &SkillTool{Dirs: []string{dir}, Store: db, ReadOnly: readOnly}, db, dir
}

func runSkill(t *testing.T, tool *SkillTool, args string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute(%s): %v", args, err)
	}
	return out
}

func TestSkillLifecycle_RecordsTracking(t *testing.T) {
	tool, db, _ := newSkillTool(t, false)

	runSkill(t, tool, `{"action":"create","name":"tdd","description":"a desc","body":"a body"}`)

	prov, err := db.GetSkillProvenance("tdd")
	if err != nil {
		t.Fatalf("GetSkillProvenance: %v", err)
	}
	if prov.CreatedBy != "agent" || prov.Source != "skill:create" {
		t.Errorf("provenance = %+v, want agent/skill:create", prov)
	}

	// load is a view, not a use (P3 decides the use).
	runSkill(t, tool, `{"action":"load","name":"tdd"}`)
	usage, err := db.GetSkillUsage("tdd")
	if err != nil {
		t.Fatalf("GetSkillUsage: %v", err)
	}
	if usage.ViewCount != 1 || usage.UseCount != 0 {
		t.Errorf("usage = %+v, want view=1 use=0", usage)
	}

	// edit records a ledger row whose before/after hashes differ.
	runSkill(t, tool, `{"action":"edit","name":"tdd","description":"new desc","body":"new body"}`)

	entries, err := db.ListSkillLedger("tdd")
	if err != nil {
		t.Fatalf("ListSkillLedger: %v", err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if len(entries) != 2 || actions[0] != "create" || actions[1] != "edit" {
		t.Fatalf("ledger actions = %v, want [create edit]", actions)
	}
	if entries[1].BeforeHash == "" || entries[1].BeforeHash == entries[1].AfterHash {
		t.Errorf("edit ledger hashes = %q/%q, want distinct non-empty", entries[1].BeforeHash, entries[1].AfterHash)
	}

	// delete archives (not deletes) and appends an archive row.
	runSkill(t, tool, `{"action":"delete","name":"tdd"}`)
	entries, err = db.ListSkillLedger("tdd")
	if err != nil {
		t.Fatalf("ListSkillLedger after archive: %v", err)
	}
	if len(entries) != 3 || entries[2].Action != "archive" {
		t.Fatalf("ledger actions = %v, want [create edit archive]", actions)
	}
}

func TestSkillDelete_ArchivesNotDeletes(t *testing.T) {
	tool, _, dir := newSkillTool(t, false)
	runSkill(t, tool, `{"action":"create","name":"tdd","description":"desc","body":"body"}`)

	if skills.FindSkill(tool.Dirs, "tdd") == nil {
		t.Fatal("skill not found before delete")
	}
	original := filepath.Join(dir, "tdd", "SKILL.md")
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("SKILL.md missing before delete: %v", err)
	}

	runSkill(t, tool, `{"action":"delete","name":"tdd"}`)

	if skills.FindSkill(tool.Dirs, "tdd") != nil {
		t.Fatal("skill still discoverable after archive")
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("original SKILL.md still present after archive (err=%v)", err)
	}
	archived := filepath.Join(dir, skills.ArchiveDir, "tdd.md")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("archived file missing at %s: %v", archived, err)
	}
}

func TestSkillDelete_ReadOnlyRefused(t *testing.T) {
	tool, db, dir := newSkillTool(t, true)

	// Seed the skill out-of-band: a read-only tool refuses create too, so we
	// cannot exercise delete through the same tool.
	if _, err := skills.Create(dir, "tdd", "desc", "body"); err != nil {
		t.Fatalf("skills.Create: %v", err)
	}

	_, err := tool.Execute(context.Background(), `{"action":"delete","name":"tdd"}`)
	if err == nil {
		t.Fatal("delete on read-only tool should fail")
	}
	if skills.FindSkill(tool.Dirs, "tdd") == nil {
		t.Fatal("skill was archived despite read-only")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "tdd", "SKILL.md")); statErr != nil {
		t.Fatalf("SKILL.md missing after refused delete: %v", statErr)
	}
	entries, _ := db.ListSkillLedger("tdd")
	if len(entries) != 0 {
		t.Fatalf("ledger = %+v, want no rows (seeded out-of-band)", entries)
	}
}

func TestSkillNilStore_NoTracking(t *testing.T) {
	dir := t.TempDir()
	tool := &SkillTool{Dirs: []string{dir}} // Store nil

	runSkill(t, tool, `{"action":"create","name":"tdd","description":"desc","body":"body"}`)
	runSkill(t, tool, `{"action":"load","name":"tdd"}`)
	runSkill(t, tool, `{"action":"edit","name":"tdd","description":"new","body":"new"}`)
	runSkill(t, tool, `{"action":"delete","name":"tdd"}`)

	if skills.FindSkill(tool.Dirs, "tdd") != nil {
		t.Fatal("skill still discoverable after archive with nil store")
	}
}

func TestSkillContentHash_Deterministic(t *testing.T) {
	a := skillContentHash("tdd", "desc", "body")
	b := skillContentHash("tdd", "desc", "body")
	if a != b {
		t.Fatalf("hash not deterministic: %q vs %q", a, b)
	}
	if skillContentHash("tdd", "desc", "other") == a {
		t.Fatal("hash did not change when body changed")
	}
	if skillContentHash("other", "desc", "body") == a {
		t.Fatal("hash did not change when name changed")
	}
}
