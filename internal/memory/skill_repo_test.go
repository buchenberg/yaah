package memory

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRecordSkillUsage pins the use/view split: a use bumps use_count and
// stamps last_used_at, a view bumps view_count only, and both accumulate
// on the same row.
func TestRecordSkillUsage(t *testing.T) {
	d := openTestDB(t)

	tests := []struct {
		name      string
		used      bool
		wantUse   int
		wantView  int
		wantStamp bool
	}{
		{name: "first view", used: false, wantUse: 0, wantView: 1, wantStamp: false},
		{name: "first use", used: true, wantUse: 1, wantView: 1, wantStamp: true},
		{name: "second use", used: true, wantUse: 2, wantView: 1, wantStamp: true},
		{name: "second view", used: false, wantUse: 2, wantView: 2, wantStamp: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := d.RecordSkillUsage("tdd", tc.used); err != nil {
				t.Fatalf("RecordSkillUsage(used=%v): %v", tc.used, err)
			}
			got, err := d.GetSkillUsage("tdd")
			if err != nil {
				t.Fatalf("GetSkillUsage: %v", err)
			}
			if got.UseCount != tc.wantUse || got.ViewCount != tc.wantView {
				t.Errorf("use/view = %d/%d, want %d/%d",
					got.UseCount, got.ViewCount, tc.wantUse, tc.wantView)
			}
			if stamped := got.LastUsedAt != ""; stamped != tc.wantStamp {
				t.Errorf("last_used_at = %q, want stamped=%v", got.LastUsedAt, tc.wantStamp)
			}
			if got.CreatedAt == "" {
				t.Error("created_at must be stamped on the first write")
			}
		})
	}

	t.Run("name required", func(t *testing.T) {
		if err := d.RecordSkillUsage("", true); err == nil {
			t.Error("RecordSkillUsage with an empty name should fail")
		}
	})
}

// TestGetSkillUsage_unknown verifies the read getters return zero values
// rather than an error for a skill that has no row yet.
func TestGetSkillUsage_unknown(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetSkillUsage("never-seen")
	if err != nil {
		t.Fatalf("GetSkillUsage: %v", err)
	}
	if got.UseCount != 0 || got.ViewCount != 0 || got.LastUsedAt != "" || got.CreatedAt != "" {
		t.Errorf("unknown skill usage = %+v, want zero record", got)
	}

	prov, err := d.GetSkillProvenance("never-seen")
	if err != nil {
		t.Fatalf("GetSkillProvenance: %v", err)
	}
	if prov.CreatedBy != "" || prov.Source != "" || prov.CreatedAt != "" {
		t.Errorf("unknown skill provenance = %+v, want zero record", prov)
	}

	entries, err := d.ListSkillLedger("never-seen")
	if err != nil {
		t.Fatalf("ListSkillLedger: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("unknown skill ledger = %d rows, want 0", len(entries))
	}
}

// TestRecordSkillProvenance pins first-write-wins: a later record must not
// rewrite the original author of a skill.
func TestRecordSkillProvenance(t *testing.T) {
	d := openTestDB(t)

	tests := []struct {
		name      string
		createdBy string
		source    string
	}{
		{name: "agent author", createdBy: "agent", source: "skill:create"},
		{name: "later record", createdBy: "consolidator", source: "skill:patch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := d.RecordSkillProvenance("tdd", tc.createdBy, tc.source); err != nil {
				t.Fatalf("RecordSkillProvenance: %v", err)
			}
		})
	}

	got, err := d.GetSkillProvenance("tdd")
	if err != nil {
		t.Fatalf("GetSkillProvenance: %v", err)
	}
	if got.CreatedBy != "agent" || got.Source != "skill:create" {
		t.Errorf("provenance = %+v, want the first record (agent/skill:create)", got)
	}
	if got.CreatedAt == "" {
		t.Error("created_at must be stamped")
	}

	t.Run("fields required", func(t *testing.T) {
		if err := d.RecordSkillProvenance("tdd", "", "skill:create"); err == nil {
			t.Error("RecordSkillProvenance without created_by should fail")
		}
	})
}

// TestRecordSkillLedger verifies the ledger is append-only and returned
// oldest-first, per skill.
func TestRecordSkillLedger(t *testing.T) {
	d := openTestDB(t)

	mutations := []struct {
		name   string
		action string
		before string
		after  string
	}{
		{name: "tdd", action: "create", before: "", after: "hash-a"},
		{name: "tdd", action: "edit", before: "hash-a", after: "hash-b"},
		{name: "other", action: "create", before: "", after: "hash-c"},
		{name: "tdd", action: "archive", before: "hash-b", after: ""},
	}
	for _, m := range mutations {
		if err := d.RecordSkillLedger(m.name, m.action, m.before, m.after, ""); err != nil {
			t.Fatalf("RecordSkillLedger(%s/%s): %v", m.name, m.action, err)
		}
	}

	entries, err := d.ListSkillLedger("tdd")
	if err != nil {
		t.Fatalf("ListSkillLedger: %v", err)
	}
	wantActions := []string{"create", "edit", "archive"}
	if len(entries) != len(wantActions) {
		t.Fatalf("ledger has %d rows, want %d: %+v", len(entries), len(wantActions), entries)
	}
	for i, want := range wantActions {
		if entries[i].Action != want {
			t.Errorf("row %d action = %q, want %q", i, entries[i].Action, want)
		}
		if entries[i].CreatedAt == "" {
			t.Errorf("row %d missing created_at", i)
		}
	}
	if entries[1].BeforeHash != "hash-a" || entries[1].AfterHash != "hash-b" {
		t.Errorf("edit hashes = %q/%q, want hash-a/hash-b", entries[1].BeforeHash, entries[1].AfterHash)
	}

	all, err := d.ListSkillLedger("")
	if err != nil {
		t.Fatalf("ListSkillLedger(all): %v", err)
	}
	if len(all) != len(mutations) {
		t.Errorf("whole ledger has %d rows, want %d", len(all), len(mutations))
	}

	t.Run("fields required", func(t *testing.T) {
		if err := d.RecordSkillLedger("tdd", "", "", "", ""); err == nil {
			t.Error("RecordSkillLedger without an action should fail")
		}
	})
}

// TestMigrate_addsSkillLifecycleTables verifies the v2 → v3 migration: an
// existing database gains the three skill tables and the version stamp, and
// the lifecycle writes work against it.
func TestMigrate_addsSkillLifecycleTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
	CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	INSERT INTO schema_meta (key, value) VALUES ('version', '2');
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	legacy.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer d.Close()

	for _, table := range []string{"skill_usage", "skill_provenance", "skill_ledger"} {
		var n int
		if err := d.sql.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?", table,
		).Scan(&n); err != nil {
			t.Fatalf("ping %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s table missing after migration", table)
		}
	}

	var version string
	if err := d.sql.QueryRow(`SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != strconv.Itoa(currentSchemaVersion) {
		t.Errorf("schema version = %q, want %d", version, currentSchemaVersion)
	}

	if err := d.RecordSkillUsage("tdd", true); err != nil {
		t.Fatalf("RecordSkillUsage on migrated db: %v", err)
	}
}
