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
		Evidence:   rec.Evidence,
		ArmedAt:    rec.ArmedAt,
		ExpiresAt:  rec.ExpiresAt,
		FiredAt:    rec.FiredAt,
		IntervalMs: rec.IntervalMs,
	})
}

func (s wakeStore) ListWatches(sessionID string) ([]wake.WatchRecord, error) {
	rows, err := s.db.ListSessionWatches(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]wake.WatchRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, wake.WatchRecord{
			ID:         r.ID,
			On:         r.On,
			Until:      r.Until,
			State:      r.State,
			Evidence:   r.Evidence,
			ArmedAt:    r.ArmedAt,
			ExpiresAt:  r.ExpiresAt,
			FiredAt:    r.FiredAt,
			IntervalMs: r.IntervalMs,
		})
	}
	return out, nil
}

func (s wakeStore) DeleteWatch(sessionID, id string) error {
	return s.db.DeleteSessionWatch(sessionID, id)
}
