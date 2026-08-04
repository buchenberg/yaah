package agent

import "github.com/buchenberg/yaah/internal/types"

func (l *Loop) persistMessage(msg types.Message) {
	if l.Persister == nil {
		return
	}
	l.Persister.Persist(msg)
}
