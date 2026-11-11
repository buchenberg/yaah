package yaah

import "testing"

func TestSwitchMode(t *testing.T) {
	tests := []struct {
		name          string
		startReadOnly bool
		arg           string
		wantReadOnly  bool
		wantOK        bool
		wantReply     string
	}{
		{name: "empty reports full", arg: "", wantOK: true, wantReply: "session mode: full"},
		{name: "empty reports read-only", startReadOnly: true, arg: "", wantReadOnly: true, wantOK: true, wantReply: "session mode: read-only"},
		{name: "read-only switches", arg: "read-only", wantReadOnly: true, wantOK: true, wantReply: "session mode switched: read-only — the new tool gate takes effect on the next run"},
		{name: "plan maps to read-only", arg: "plan", wantReadOnly: true, wantOK: true, wantReply: "session mode switched: read-only — the new tool gate takes effect on the next run"},
		{name: "read-only is case-insensitive", arg: "READ-ONLY", wantReadOnly: true, wantOK: true, wantReply: "session mode switched: read-only — the new tool gate takes effect on the next run"},
		{name: "already read-only is a no-op", startReadOnly: true, arg: "read-only", wantReadOnly: true, wantOK: true, wantReply: "session mode is already read-only"},
		{name: "full switches", startReadOnly: true, arg: "full", wantOK: true, wantReply: "session mode switched: full — the new tool gate takes effect on the next run"},
		{name: "build maps to full", startReadOnly: true, arg: "build", wantOK: true, wantReply: "session mode switched: full — the new tool gate takes effect on the next run"},
		{name: "already full is a no-op", arg: "full", wantOK: true, wantReply: "session mode is already full"},
		{name: "unknown refused", arg: "banana", wantOK: false, wantReply: `"banana" is not a mode — available: read-only, full`},
		{name: "unknown trims whitespace", arg: "  banana  ", wantOK: false, wantReply: `"banana" is not a mode — available: read-only, full`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &agentSession{readOnly: tt.startReadOnly}
			got, ok := s.switchMode(tt.arg)
			if ok != tt.wantOK {
				t.Fatalf("switchMode(%q) ok = %v, want %v (reply %q)", tt.arg, ok, tt.wantOK, got)
			}
			if got != tt.wantReply {
				t.Errorf("switchMode(%q) reply = %q, want %q", tt.arg, got, tt.wantReply)
			}
			if s.IsReadOnly() != tt.wantReadOnly {
				t.Errorf("switchMode(%q) IsReadOnly() = %v, want %v", tt.arg, s.IsReadOnly(), tt.wantReadOnly)
			}
		})
	}
}
