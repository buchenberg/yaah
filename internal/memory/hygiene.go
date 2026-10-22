package memory

import (
	"fmt"
	"time"
)

// Memory hygiene: the garbage collection half of docs/compaction-as-sleep.md
// §8 (deep sleep). A long-lived install writes memory atoms forever, so the
// store needs a way to shed the ones that were never worth keeping — without
// ever shedding one that proved it was. Only the memory table is touched;
// the conversation rows are not this file's business.
//
// The sweeps are deliberately narrow. Each is a single statement, cheap to
// run at session close, and conservative to the point of leaving garbage
// behind: recall quality is the resource being protected, and a forgotten
// atom costs more than a dead row.

// compactionSourceTag is the provenance tag the consolidator stamps into the
// tags JSON array of every atom it distills (cmd/yaah/consolidate.go). It is
// the only marker that distinguishes a compaction product from a memory a
// human or a turn wrote deliberately, so it gates the staleness sweep: a
// global atom without it is never swept, however old.
const compactionSourceTag = "source:compaction"

// StaleAge is the single staleness floor shared by the hygiene sweep and the
// stats view: a compaction atom younger than this had its whole lifetime to be
// recalled. Keeping it in one place prevents the sweep and its stats view
// from drifting apart.
const StaleAge = 30 * 24 * time.Hour

// PruneSession deletes the session-scoped atoms distilled by one conversation,
// returning the number of rows removed. It is for explicit session DELETION,
// not close: sessions are resumable, so close deliberately leaves a session's
// working context intact (the age-based PruneStaleCompaction reclaims dead
// atoms later). An empty sessionID is a no-op rather than a blanket delete.
//
// Two guards keep recall intact:
//
//   - Only scope = 'session' rows are candidates. Global atoms — the ones
//     meant to outlive the conversation — are never this method's business,
//     whatever session wrote them.
//   - Never-accessed is a precondition. An atom with access_count > 0 or
//     accessed_at > 0 earned its keep by being recalled, so it survives its
//     session; that is the whole difference between working context and a
//     dead atom.
func (d *DB) PruneSession(sessionID string) (int64, error) {
	if sessionID == "" {
		return 0, nil
	}
	res, err := d.sql.Exec(
		`DELETE FROM memory
		 WHERE scope = ?
		   AND session_id = ?
		   AND COALESCE(accessed_at, 0) = 0
		   AND COALESCE(access_count, 0) = 0`,
		ScopeSession, sessionID,
	)
	if err != nil {
		return 0, fmt.Errorf("prune session memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune session memory: rows affected: %w", err)
	}
	return n, nil
}

// PruneStaleCompaction deletes compaction atoms created before olderThan that
// were never accessed, returning the number of rows removed. It is the single
// sweep that closes the deep-sleep loop: the consolidator's output that no
// later recall ever touched. It covers both scopes because a never-accessed
// atom is dead weight whether the session that wrote it is still around or
// not — and age alone is safe precisely because access is the proof that an
// atom mattered.
//
// Every characteristic of a row must hold before it is deleted, and each one
// rules out a distinct mistake:
//
//   - the compactionSourceTag element in tags — an atom written by a human, a
//     turn, or the CLI carries no such tag and is never swept.
//   - created_at < olderThan — the atom had its whole lifetime to be useful,
//     including any session resume within that window.
//   - neither accessed_at nor access_count is set — access is proof the atom
//     earned its keep; a used one is kept indefinitely.
//
// A zero olderThan therefore deletes nothing (its Unix seconds are far in
// the past), which is the safe default for a caller that forgot to set it.
func (d *DB) PruneStaleCompaction(olderThan time.Time) (int64, error) {
	res, err := d.sql.Exec(
		`DELETE FROM memory
		 WHERE tags LIKE ?
		   AND created_at < ?
		   AND COALESCE(accessed_at, 0) = 0
		   AND COALESCE(access_count, 0) = 0`,
		"%\""+compactionSourceTag+"\"%", olderThan.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("prune stale compaction memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune stale compaction memory: rows affected: %w", err)
	}
	return n, nil
}
