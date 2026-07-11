package yaah

import "testing"

func TestRedactKey_masksAllButLast4(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"standard key", "sk-test1234567890", "*************7890"},
		{"empty", "", "(not set)"},
		{"too short", "sk-ab", "(too short)"},
		{"exactly 8", "sk-12345", "****2345"},
		{"long key", "sk-abcdefghijklmnopqrstuvwxyz", "*************************wxyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactKey(tt.key)
			if got != tt.want {
				t.Errorf("redactKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
