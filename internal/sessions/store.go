package sessions

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/buchenberg/yaah/internal/memory"
)

// dbStore adapts *memory.DB to Store. It exists for one reason: the store
// spells "no such session" two ways — DeleteSession returns
// memory.ErrSessionNotFound while GetSession passes the driver's
// sql.ErrNoRows straight through — and the registry should map exactly one
// sentinel (see Registry.Resolve). Keeping the translation here leaves
// registry.go free of database/sql.
type dbStore struct {
	db *memory.DB
}

var _ Store = dbStore{}

func (s dbStore) ListSessionsPage(filter memory.SessionListFilter) ([]memory.SessionSummary, string, error) {
	return s.db.ListSessionsPage(filter)
}

// GetSession normalizes the driver's "no rows" into memory.ErrSessionNotFound.
func (s dbStore) GetSession(id string) (memory.Session, error) {
	sess, err := s.db.GetSession(id)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Session{}, fmt.Errorf("%w: %s", memory.ErrSessionNotFound, id)
	}
	return sess, err
}

func (s dbStore) DeleteSession(id string) error {
	return s.db.DeleteSession(id)
}
