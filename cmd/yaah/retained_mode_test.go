package yaah

import "testing"

// Plan 7 Phase 2: the retained lane is the default; agents.subagent.mode:
// "shared" is the explicit opt-out that keeps the legacy direct-to-host lane.
func TestRetainedMode(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"retained", true},
		{"RETAINED", true},
		{"  retained  ", true},
		{"shared", false},
		{"SHARED", false},
		{" Shared ", false},
	}
	for _, c := range cases {
		if got := retainedMode(c.in); got != c.want {
			t.Errorf("retainedMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
