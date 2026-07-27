package agent

import "github.com/buchenberg/yaah/internal/types"

// persistMessage delegates to the Persister component. Lazily creates it
// from DB/WriteDebouncer/SessionID if not yet initialized. Backward-compatible
// wrapper.
func (l *Loop) persistMessage(msg types.Message) {
	if l.Persister == nil {
		l.Persister = NewSessionPersister(l.DB, l.WriteDebouncer, l.SessionID)
		l.Persister.SetMsgIdx(l.MsgIdx)
	}
	l.Persister.Persist(msg)
}
