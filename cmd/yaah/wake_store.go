package yaah

import (
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/wake"
)

// wakeStore adapts the session database to wake.Store. The adapter lives in
// the composition root so internal/memory does not depend on the wake engine
// and internal/wake does not depend on SQLite.
type wakeStore struct {
	db *memory.DB
}

var _ wake.Store = wakeStore{}

func (s wakeStore) SaveWatch(sessionID string, rec wake.WatchRecord) error {
	return s.db.SaveSessionWatch(sessionID, memory.SessionWatch{
		ID:         rec.ID,
		On:         rec.On,
		Until:      rec.Until,
		State:      rec.State,
		Evidence:   wake.MarshalEvidence(rec.Evidence),
		ArmedAt:    rec.ArmedAt,
		ExpiresAt:  rec.ExpiresAt,
		FiredAt:    rec.FiredAt,
		IntervalMs: rec.IntervalMs,
		Owner:      rec.Owner,
		LeaseUntil: rec.LeaseUntil,
	})
}

func (s wakeStore) ListWatches(sessionID string) ([]wake.WatchRecord, error) {
	rows, err := s.db.ListSessionWatches(sessionID)
	if err != nil {
		return nil, err
	}
	return toWakeRecords(rows), nil
}

func (s wakeStore) ClaimWatches(sessionID, owner string, leaseUntil int64) ([]wake.WatchRecord, error) {
	rows, err := s.db.ClaimSessionWatches(sessionID, owner, leaseUntil)
	if err != nil {
		return nil, err
	}
	return toWakeRecords(rows), nil
}

func (s wakeStore) ClaimFire(sessionID, id, owner string, armedAt, firedAt, leaseUntil int64, evidence string) (bool, error) {
	return s.db.ClaimSessionWatchFire(sessionID, id, owner, armedAt, firedAt, leaseUntil, evidence)
}

func (s wakeStore) ReleaseWatchLease(sessionID, id, owner string) (bool, error) {
	return s.db.ReleaseSessionWatchLease(sessionID, id, owner)
}

func (s wakeStore) DeleteWatch(sessionID, id string) error {
	return s.db.DeleteSessionWatch(sessionID, id)
}

func toWakeRecords(rows []memory.SessionWatch) []wake.WatchRecord {
	out := make([]wake.WatchRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, wake.WatchRecord{
			ID:         r.ID,
			On:         r.On,
			Until:      r.Until,
			State:      r.State,
			Evidence:   wake.ParseEvidence(r.Evidence),
			ArmedAt:    r.ArmedAt,
			ExpiresAt:  r.ExpiresAt,
			FiredAt:    r.FiredAt,
			IntervalMs: r.IntervalMs,
			Owner:      r.Owner,
			LeaseUntil: r.LeaseUntil,
		})
	}
	return out
}
