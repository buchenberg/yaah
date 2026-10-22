package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// skill_repo.go persists the skill lifecycle introduced for
// compaction-as-sleep P1 (docs/compaction-as-sleep.md §8): how often a
// skill earns its keep (skill_usage), where it came from
// (skill_provenance), and every mutation ever applied to it
// (skill_ledger). The tables themselves are created by the schema v3
// migration in memory.go.

// SkillUsage is the usage record for one skill: use_count counts loads
// that were applied to the task, view_count counts inspections that did
// not become uses. ReuseAfterPatch is reserved for the P3 idempotency
// work (a patched skill must be re-used before it is patched again) and
// stays zero until then.
type SkillUsage struct {
	Name            string
	UseCount        int
	ViewCount       int
	ReuseAfterPatch int
	LastUsedAt      string // RFC 3339; empty when never used
	CreatedAt       string // RFC 3339
}

// SkillProvenance records who created a skill and from what source.
type SkillProvenance struct {
	Name      string
	CreatedBy string
	Source    string // e.g. "skill:create"; empty when unknown
	CreatedAt string // RFC 3339
}

// SkillLedgerEntry is one append-only record of a skill mutation.
// BeforeHash/AfterHash are content digests, so a ledger row shows what
// changed without storing the bodies; Delta carries a serialized patch
// once the sealed-proposal path (P2) supplies one.
type SkillLedgerEntry struct {
	ID         int64
	Name       string
	Action     string
	BeforeHash string
	AfterHash  string
	Delta      string
	CreatedAt  string // RFC 3339
}

// skillTimestamp is the canonical timestamp format for the skill tables.
// RFC 3339 in UTC keeps the columns lexically sortable and unambiguous
// without depending on the machine's timezone.
func skillTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// RecordSkillUsage bumps a skill's counters. used=true counts a real use
// (the skill's instructions were applied) and stamps last_used_at;
// used=false counts a view — listed or inspected — which never touches
// last_used_at, so "used" stays meaningful. The upsert makes the first
// call for a name create the row.
func (d *DB) RecordSkillUsage(name string, used bool) error {
	if name == "" {
		return fmt.Errorf("record skill usage: name is required")
	}
	now := skillTimestamp()
	if used {
		_, err := d.sql.Exec(
			`INSERT INTO skill_usage (name, use_count, view_count, reuse_after_patch, last_used_at, created_at)
			 VALUES (?, 1, 0, 0, ?, ?)
			 ON CONFLICT(name) DO UPDATE SET
				use_count = use_count + 1,
				last_used_at = excluded.last_used_at`,
			name, now, now,
		)
		return err
	}
	_, err := d.sql.Exec(
		`INSERT INTO skill_usage (name, use_count, view_count, reuse_after_patch, last_used_at, created_at)
		 VALUES (?, 0, 1, 0, NULL, ?)
		 ON CONFLICT(name) DO UPDATE SET view_count = view_count + 1`,
		name, now,
	)
	return err
}

// GetSkillUsage returns a skill's usage record. An unknown name yields the
// zero record (not an error): callers summarising a skills tree must not
// have to special-case a skill nobody has touched yet.
func (d *DB) GetSkillUsage(name string) (SkillUsage, error) {
	u := SkillUsage{Name: name}
	err := d.sql.QueryRow(
		`SELECT use_count, view_count, reuse_after_patch, COALESCE(last_used_at, ''), created_at
		 FROM skill_usage WHERE name = ?`,
		name,
	).Scan(&u.UseCount, &u.ViewCount, &u.ReuseAfterPatch, &u.LastUsedAt, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillUsage{Name: name}, nil
	}
	if err != nil {
		return SkillUsage{Name: name}, fmt.Errorf("get skill usage: %w", err)
	}
	return u, nil
}

// RecordSkillProvenance stamps who created a skill and from what source.
// Idempotent per name: the first record wins, so re-creating an archived
// skill never rewrites the original author's provenance.
func (d *DB) RecordSkillProvenance(name, createdBy, source string) error {
	if name == "" || createdBy == "" {
		return fmt.Errorf("record skill provenance: name and created_by are required")
	}
	_, err := d.sql.Exec(
		`INSERT INTO skill_provenance (name, created_by, source, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO NOTHING`,
		name, createdBy, source, skillTimestamp(),
	)
	return err
}

// GetSkillProvenance returns a skill's provenance record. An unknown name
// yields the zero record (not an error).
func (d *DB) GetSkillProvenance(name string) (SkillProvenance, error) {
	p := SkillProvenance{Name: name}
	err := d.sql.QueryRow(
		`SELECT created_by, COALESCE(source, ''), created_at FROM skill_provenance WHERE name = ?`,
		name,
	).Scan(&p.CreatedBy, &p.Source, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillProvenance{Name: name}, nil
	}
	if err != nil {
		return SkillProvenance{Name: name}, fmt.Errorf("get skill provenance: %w", err)
	}
	return p, nil
}

// RecordSkillLedger appends one mutation to a skill's ledger. The ledger is
// append-only — there is no update or delete path — so an autonomous
// consolidation that patches a skill stays auditable and reversible.
func (d *DB) RecordSkillLedger(name, action, beforeHash, afterHash, delta string) error {
	if name == "" || action == "" {
		return fmt.Errorf("record skill ledger: name and action are required")
	}
	_, err := d.sql.Exec(
		`INSERT INTO skill_ledger (name, action, before_hash, after_hash, delta, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		name, action, beforeHash, afterHash, delta, skillTimestamp(),
	)
	return err
}

// ListSkillLedger returns a skill's ledger rows oldest-first. name == ""
// returns the whole ledger. An unknown name yields no rows and no error.
func (d *DB) ListSkillLedger(name string) ([]SkillLedgerEntry, error) {
	query := `SELECT id, name, action, COALESCE(before_hash, ''), COALESCE(after_hash, ''), COALESCE(delta, ''), created_at
		FROM skill_ledger`
	var (
		rows *sql.Rows
		err  error
	)
	if name == "" {
		rows, err = d.sql.Query(query + ` ORDER BY id`)
	} else {
		rows, err = d.sql.Query(query+` WHERE name = ? ORDER BY id`, name)
	}
	if err != nil {
		return nil, fmt.Errorf("list skill ledger: %w", err)
	}
	defer rows.Close()

	var entries []SkillLedgerEntry
	for rows.Next() {
		var e SkillLedgerEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Action, &e.BeforeHash, &e.AfterHash, &e.Delta, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("list skill ledger: scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list skill ledger: %w", err)
	}
	return entries, nil
}
