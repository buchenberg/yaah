package acp

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNotifyWakeWritesExtensionNotification(t *testing.T) {
	var out bytes.Buffer
	s := &Server{conn: &connection{
		w:    &out,
		logf: func(string, ...any) {},
	}}

	s.notifyWake("sess-1", "watch fired: ci is green")

	var msg Message
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &msg); err != nil {
		t.Fatalf("decode notification: %v (%q)", err, out.String())
	}
	if msg.JSONRPC != "2.0" || msg.Method != MethodWake || len(msg.ID) != 0 {
		t.Fatalf("notification = %+v, want a yaah/wake notification", msg)
	}
	var params WakeNotification
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params.SessionID != "sess-1" || params.Prompt != "watch fired: ci is green" {
		t.Fatalf("params = %+v", params)
	}
}

func TestBuildSessionInstallsWakeRequester(t *testing.T) {
	fake := &fakeSession{id: "sess-1"}
	s := &Server{
		sessions: make(map[string]*sessionState),
		newSession: func(BuildOptions) (Session, error) {
			return fake, nil
		},
		logf: func(string, ...any) {},
	}
	if _, err := s.buildSession(BuildOptions{}, ""); err != nil {
		t.Fatalf("buildSession: %v", err)
	}
	fake.mu.Lock()
	fn := fake.wake
	fake.mu.Unlock()
	if fn == nil {
		t.Fatal("buildSession did not install a wake requester")
	}
	if fn("watch prompt") {
		t.Fatal("the ACP requester must return false so the digest also queues")
	}
}
