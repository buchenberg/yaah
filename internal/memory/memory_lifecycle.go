package memory

import (
	"database/sql"
	"errors"
	"fmt"
)

// SupersedeMemory marks oldID as superseded by newID. The row is kept for
// audit but excluded from every recall path, so a contradicted fact stops
// surfacing instead of being silently retained beside its correction.
func (d *DB) SupersedeMemory(oldID, newID string) error {
	if oldID == newID {
		return fmt.Errorf("memory %s cannot supersede itself", oldID)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("supersede %s: %w", oldID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := supersedeInTx(tx, oldID, newID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("supersede %s: %w", oldID, err)
	}
	// Drop the superseded atom from the vector index so it stops surfacing.
	d.deleteVecByMemoryID(oldID)
	return nil
}

// supersedeInTx validates both ends of a supersede and writes the marker. The
// validation and the write share the caller's transaction because a
// half-applied supersede is the worst state available: the replacement saved,
// the claim it was meant to correct still live.
func supersedeInTx(tx *sql.Tx, oldID, newID string) error {
	oldScope, oldSup, err := liveAtomScope(tx, oldID)
	if err != nil {
		return err
	}
	if oldSup != "" {
		return fmt.Errorf("%w: %s (already superseded by %s)", ErrMemoryNotFound, oldID, oldSup)
	}
	newScope, newSup, err := liveAtomScope(tx, newID)
	if err != nil {
		return err
	}
	if newSup != "" {
		return fmt.Errorf("replacement %s is itself superseded by %s", newID, newSup)
	}
	// A session atom may not retire a global fact. The global claim would leave
	// recall and be lost for good once the session atom is pruned, which is a
	// silent demotion of exactly the knowledge the tiers exist to protect.
	if newScope == ScopeSession && oldScope == ScopeGlobal {
		return fmt.Errorf("a %s atom cannot supersede a %s one: write the correction as %s, or promote it afterwards", ScopeSession, ScopeGlobal, ScopeGlobal)
	}
	if _, err := tx.Exec(`UPDATE memory SET superseded_by = ? WHERE id = ?`, newID, oldID); err != nil {
		return fmt.Errorf("supersede %s: %w", oldID, err)
	}
	return nil
}

// liveAtomScope reads an atom's tier and its supersession marker. A missing row
// reports ErrMemoryNotFound; a non-empty marker means the atom is dead.
func liveAtomScope(tx *sql.Tx, id string) (scope, supersededBy string, err error) {
	var scopeCol, supCol sql.NullString
	err = tx.QueryRow(`SELECT scope, superseded_by FROM memory WHERE id = ?`, id).Scan(&scopeCol, &supCol)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	if err != nil {
		return "", "", err
	}
	return scopeCol.String, supCol.String, nil
}

// AddMemorySuperseding writes e and marks supersedesID as superseded by the
// atom that ends up representing it, in one transaction. It returns the atom's
// id and whether that atom already existed (an identical live atom is reused
// rather than duplicated, exactly as AddMemoryDedup does).
func (d *DB) AddMemorySuperseding(e Entry, supersedesID string) (string, bool, error) {
	digest := memoryDigest(e.Text)
	scope, err := NormalizeScope(e.Scope)
	if err != nil {
		return "", false, err
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()

	id := e.ID
	existed := false
	var dupID string
	err = tx.QueryRow(`SELECT id FROM memory WHERE digest = ? AND superseded_by IS NULL LIMIT 1`, digest).Scan(&dupID)
	switch {
	case err == nil:
		id, existed = dupID, true
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(
			`INSERT INTO memory (id, text, tags, source, scope, session_id, created_at, digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.Text, e.Tags, e.Source, scope, nullableText(e.SessionID), e.CreatedAt, digest,
		); err != nil {
			return "", false, err
		}
	default:
		return "", false, err
	}

	if supersedesID != "" && supersedesID != id {
		if err := supersedeInTx(tx, supersedesID, id); err != nil {
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	if supersedesID != "" && supersedesID != id {
		d.deleteVecByMemoryID(supersedesID)
	}
	return id, existed, nil
}

// SupersededBy reports which atom superseded id, or "" when id is still live.
func (d *DB) SupersededBy(id string) (string, error) {
	var by sql.NullString
	if err := d.sql.QueryRow(`SELECT superseded_by FROM memory WHERE id = ?`, id).Scan(&by); err != nil {
		return "", err
	}
	return by.String, nil
}

// PromoteMemory moves an atom from the session tier into the global one and
// returns the tier it came from. Promotion is one-way on purpose: a global fact
// is a claim about the world, and a demoted atom would only be re-promoted (and
// re-contested) on the next recall. Promoting an atom that is already global is
// a no-op rather than an error, so a retry after a partial failure is safe.
func (d *DB) PromoteMemory(id string) (string, error) {
	var scope, supersededBy sql.NullString
	err := d.sql.QueryRow(`SELECT scope, superseded_by FROM memory WHERE id = ?`, id).Scan(&scope, &supersededBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrMemoryNotFound, id)
	}
	if err != nil {
		return "", err
	}
	if supersededBy.Valid {
		return "", fmt.Errorf("memory %s is superseded by %s; promote the replacement instead", id, supersededBy.String)
	}
	if scope.String == ScopeGlobal {
		return scope.String, nil
	}
	if _, err := d.sql.Exec(`UPDATE memory SET scope = ? WHERE id = ?`, ScopeGlobal, id); err != nil {
		return "", fmt.Errorf("promote %s: %w", id, err)
	}
	// Refresh the vec0 scope metadata so a promoted atom is recallable as
	// global, not only under its old session tier.
	d.reindexVecByID(id)
	return scope.String, nil
}

// ReconcileMemoryDigests backfills the digest column for rows where it is
// NULL (e.g. entries added before the digest migration). Returns the number
// of rows updated.
func (d *DB) ReconcileMemoryDigests() (int, error) {
	rows, err := d.sql.Query(`SELECT id, text FROM memory WHERE digest IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("reconcile digests: query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id, text string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.text); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updated := 0
	for _, r := range pending {
		if _, err := d.sql.Exec(`UPDATE memory SET digest = ? WHERE id = ?`, memoryDigest(r.text), r.id); err != nil {
			return updated, fmt.Errorf("reconcile digests: update %s: %w", r.id, err)
		}
		updated++
	}
	return updated, nil
}
