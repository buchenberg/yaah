package acp

// MethodWake is the yaah extension notification the agent emits when an armed
// watch fires while the session is idle. It is a notification, not a request:
// clients that do not know it ignore it, and the digest is also queued as a
// follow-up so the next session/prompt always delivers it to the model.
const MethodWake = "yaah/wake"

// WakeNotification is the params of a yaah/wake notification. Prompt is the
// same bounded digest the model will see; it intentionally carries no
// predicate target beyond what the digest itself renders.
type WakeNotification struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
}

// notifyWake emits the extension notification if a connection is live. A
// missing connection (before Run or after teardown) is a no-op; the queued
// follow-up remains the delivery guarantee.
func (s *Server) notifyWake(sessionID, prompt string) {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return
	}
	c.notify(MethodWake, WakeNotification{SessionID: sessionID, Prompt: prompt})
}
