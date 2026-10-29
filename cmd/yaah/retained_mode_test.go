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

func TestRetainedLaneActive(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mode             string
		retainCapable    bool
		containerSession bool
		want             bool
	}{
		{"local capable", "retained", true, false, true},
		{"local uncapturable", "retained", false, false, false},
		{"container session report lane", "retained", false, true, true},
		{"shared mode local", "shared", true, false, false},
		{"shared mode container", "shared", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retainedLaneActive(tc.mode, tc.retainCapable, tc.containerSession); got != tc.want {
				t.Errorf("retainedLaneActive(%q, %v, %v) = %v, want %v", tc.mode, tc.retainCapable, tc.containerSession, got, tc.want)
			}
		})
	}
}
