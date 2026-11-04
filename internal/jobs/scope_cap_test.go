package jobs

import "testing"

func TestResolveDispatchScopeCap(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero selects default", 0, DefaultMaxDispatchScopes},
		{"negative selects default", -7, DefaultMaxDispatchScopes},
		{"explicit cap respected", 16, 16},
		{"one is a legal cap", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveDispatchScopeCap(tt.in); got != tt.want {
				t.Fatalf("ResolveDispatchScopeCap(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestDefaultMaxDispatchScopesIsPositive(t *testing.T) {
	// The cap guards a leak; a default of zero or less would mean "no cap".
	if DefaultMaxDispatchScopes <= 0 {
		t.Fatalf("DefaultMaxDispatchScopes = %d, want > 0", DefaultMaxDispatchScopes)
	}
}
