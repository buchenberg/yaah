// Package sessions is the engine-level session registry: one read-and-delete
// seam over the persisted conversation store that every consumer shares —
// the ACP session/list and session/delete callbacks, the web session picker
// endpoint, the `yaah sessions` CLI, and the interactive resume picker. The
// TUI and REPL reach the same rows through the CLI/picker surfaces; adding a
// view means calling the registry, not re-implementing pagination.
//
// The registry owns the parts every surface used to duplicate: cwd
// canonicalization (via internal/cwdpath), cursor chasing with an
// empty-page break, and error mapping into three sentinels callers can match
// with errors.Is. It deliberately does NOT import internal/acp (protocol) or
// build agent sessions: resolving an id to a *memory.Session is membership
// only, so a view decides for itself how to attach.
package sessions

import (
	"errors"
	"fmt"
	"sync"

	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/memory"
)

// Registry sentinels. Each wraps the store's own error, so callers may match
// either spelling (errors.Is) and a surface can keep the message it already
// shows users.
var (
	// ErrNotFound marks an unknown session id (delete, resolve). The wrapped
	// error is memory.ErrSessionNotFound.
	ErrNotFound = errors.New("session not found")
	// ErrInvalidCursor marks a malformed pagination cursor, as rejected by
	// the store. The wrapped error is memory.ErrInvalidCursor.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrInvalidCWD marks a cwd filter rejected by cwdpath.Canonical (a
	// relative path). Its message carries the canonicalizer's reason.
	ErrInvalidCWD = errors.New("invalid session cwd")
)

// Store is the narrow slice of the persisted session store the registry
// needs. *memory.DB satisfies it, and so does any fake in a view's tests.
type Store interface {
	ListSessionsPage(filter memory.SessionListFilter) ([]memory.SessionSummary, string, error)
	GetSession(id string) (memory.Session, error)
	DeleteSession(id string) error
}

// Filter narrows a registry query.
type Filter struct {
	// CWD limits the query to sessions started in exactly this directory;
	// the registry canonicalizes it (symlinks resolved) so it matches the
	// stored session cwds. Empty matches any cwd.
	CWD string
	// Cursor is the opaque pagination cursor from a previous page; empty
	// for the first page.
	Cursor string
	// Limit caps the rows returned. List treats <= 0 as the store's default
	// page size (memory.DefaultSessionPageSize), so an unbounded List still
	// stops after one page. Page passes it straight through.
	Limit int
	// CwdRawFallback is a picker-only escape hatch: when the canonicalized
	// CWD pass returns no rows and the raw CWD is non-empty and spelled
	// differently, List retries with the raw string so rows written before
	// cwd canonicalization (or through a symlinked PWD) still appear. Page
	// ignores it — it always issues exactly one store query.
	CwdRawFallback bool
}

// defaultListLimit is the store's own page size. List treats a non-positive
// Filter.Limit as this many rows: without the clamp an unbounded List would
// chase every page up to maxChasePages. Derived from the store's constant
// rather than restated, so the two cannot silently diverge.
const defaultListLimit = memory.DefaultSessionPageSize

// maxChasePages bounds List's cursor chase: a picker is a chooser, not a bulk
// export, and a store bug must not turn into an unbounded loop.
const maxChasePages = 50

// Registry is a handle on a session store. The zero value is not usable: use
// New (borrowed store) or OpenDefault (owned handle).
type Registry struct {
	store  Store
	closer func() error

	closeOnce sync.Once
	closeErr  error
}

// New returns a registry over a caller-owned store. Close on the result is a
// no-op: the caller keeps ownership of the handle (the web server borrows the
// agent session's store, which the session closes).
func New(store Store) *Registry {
	return &Registry{store: store}
}

// OpenDefault opens the default store (~/.yaah/state.db) and returns a
// registry that owns the handle; Close releases it.
func OpenDefault() (*Registry, error) {
	db, err := memory.OpenDefault()
	if err != nil {
		return nil, err
	}
	return &Registry{store: dbStore{db: db}, closer: db.Close}, nil
}

// Page returns exactly one store page: the rows matching f plus the cursor for
// the next page ("" on the last page). This is the primitive a protocol
// surface needs — ACP session/list and the web picker own their page size and
// pass the client's cursor straight through, so no chasing happens here.
func (r *Registry) Page(f Filter) ([]memory.SessionSummary, string, error) {
	cwd, err := canonicalFilter(f.CWD)
	if err != nil {
		return nil, "", err
	}
	return r.page(cwd, f.Cursor, f.Limit)
}

// page issues one store query with the cwd it is given, verbatim. Callers
// decide the spelling: Page passes the canonicalized filter, List's legacy
// fallback deliberately passes the raw one.
func (r *Registry) page(cwd, cursor string, limit int) ([]memory.SessionSummary, string, error) {
	rows, next, err := r.store.ListSessionsPage(memory.SessionListFilter{
		CWD: cwd, Cursor: cursor, Limit: limit,
	})
	if err != nil {
		return nil, "", mapListError(err)
	}
	return rows, next, nil
}

// List returns up to f.Limit rows for a CLI or picker that wants "recent N"
// without paging by hand. It chases the store's cursors until it has f.Limit
// rows, reaches the last page, sees an empty page, or hits maxChasePages.
//
// The empty-page break is load-bearing: a store page with no rows is the end
// of the result set no matter what cursor it reports, and trusting the cursor
// instead would spin forever.
func (r *Registry) List(f Filter) ([]memory.SessionSummary, error) {
	canonical, err := canonicalFilter(f.CWD)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	rows, err := r.chase(canonical, f.Cursor, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 || !f.CwdRawFallback || f.CWD == "" || canonical == f.CWD {
		return rows, nil
	}
	// Legacy rows carry the path the process saw (symlink and all); the
	// canonical pass above cannot see them, so retry with the raw spelling
	// exactly as the caller gave it.
	return r.chase(f.CWD, f.Cursor, limit)
}

// chase pages through the store's cursors, collecting rows in store order. The
// cwd is passed through verbatim — canonicalization happened before the chase.
func (r *Registry) chase(cwd, cursor string, limit int) ([]memory.SessionSummary, error) {
	var rows []memory.SessionSummary
	for pages := 0; pages < maxChasePages; pages++ {
		pageLimit := limit
		if limit > 0 {
			// Ask only for what is still missing so the store's own page
			// size stays the ceiling on one query.
			if pageLimit = limit - len(rows); pageLimit <= 0 {
				break
			}
		}
		batch, next, err := r.page(cwd, cursor, pageLimit)
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
		if len(batch) == 0 || next == "" {
			break
		}
		cursor = next
	}
	return rows, nil
}

// Resolve reports whether id names a persisted session and returns its stored
// row. It is a membership check, not an attach: no agent session is built and
// no history is loaded, so each view keeps its own attachment path. An
// unknown id wraps ErrNotFound.
func (r *Registry) Resolve(id string) (memory.Session, error) {
	sess, err := r.store.GetSession(id)
	if err != nil {
		if errors.Is(err, memory.ErrSessionNotFound) {
			return memory.Session{}, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return memory.Session{}, err
	}
	return sess, nil
}

// TranscriptStore is the extra method a store needs for Transcript to work.
// It is a separate interface rather than part of Store so that stores which
// only answer listing/deleting (fakes in tests, partial implementations) keep
// compiling; Transcript reports ErrNoTranscript for those instead of failing
// to build.
type TranscriptStore interface {
	GetMessages(sessionID string) ([]memory.Message, error)
}

// ErrNoTranscript marks a store that cannot serve message history.
var ErrNoTranscript = errors.New("session store cannot serve transcripts")

// Transcript resolves a session and returns it together with its message
// history in conversation order. Every CLI / protocol surface that wants to
// read a conversation goes through here instead of opening the database
// directly, which is what kept 'yaah sessions' limited to list/delete.
func (r *Registry) Transcript(id string) (memory.Session, []memory.Message, error) {
	sess, err := r.Resolve(id)
	if err != nil {
		return memory.Session{}, nil, err
	}
	ts, ok := r.store.(TranscriptStore)
	if !ok {
		return sess, nil, ErrNoTranscript
	}
	msgs, err := ts.GetMessages(sess.ID)
	if err != nil {
		return sess, nil, err
	}
	return sess, msgs, nil
}

// Delete removes a session and its messages. Deleting an unknown id wraps
// ErrNotFound so a protocol surface can succeed silently (per the ACP delete
// contract) instead of reporting an internal failure.
func (r *Registry) Delete(id string) error {
	if err := r.store.DeleteSession(id); err != nil {
		if errors.Is(err, memory.ErrSessionNotFound) {
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return err
	}
	return nil
}

// Close releases the store handle when the registry owns it (OpenDefault) and
// does nothing when the store was borrowed (New). It is idempotent and safe on
// a nil registry.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.closer != nil {
			r.closeErr = r.closer()
		}
	})
	return r.closeErr
}

// canonicalFilter validates and canonicalizes a cwd filter, mapping the
// canonicalizer's rejection onto ErrInvalidCWD. Both sentinels match: the
// wrap keeps the cwdpath error in the chain, and the cwdpath message is what
// surfaces first (surfaces own their own prefixes).
func canonicalFilter(cwd string) (string, error) {
	canonical, err := cwdpath.Canonical(cwd)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidCWD, err)
	}
	return canonical, nil
}

// mapListError re-flags a malformed cursor: both sentinels match, so a view
// can check the registry sentinel while a store-level caller keeps working.
func mapListError(err error) error {
	if errors.Is(err, memory.ErrInvalidCursor) {
		return fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	return err
}
