package agent

import "github.com/buchenberg/yaah/internal/types"

// emitHook delegates to the Hooks component. Lazily creates it from
// HookDir/SessionID if not yet initialized. Backward-compatible wrapper.
func (l *Loop) emitHook(event HookEvent) {
	if l.Hooks == nil {
		l.Hooks = NewHookEmitter(l.HookDir, l.SessionID)
	}
	l.Hooks.Emit(event)
}

// closeHook delegates to the Hooks component. Backward-compatible wrapper.
func (l *Loop) closeHook() {
	if l.Hooks != nil {
		l.Hooks.Close()
	}
}

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
