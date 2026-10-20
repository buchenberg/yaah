package subagent

import "testing"

func setLaneRegistry(t *testing.T, files map[string][]byte) {
	t.Helper()
	reg := NewRoleRegistry()
	if err := reg.LoadBytes(files); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { SetDefaultRoleRegistry(nil) })
}

// TestDispatchLaneFor pins the yaah-7yf routing matrix: an explicit
// `output: report` decouples the output contract from the substrate —
// mutating roles keep isolation (nothing sealed, worktree torn down),
// non-mutating roles go shared — while unmarked and proposal roles keep
// the historical retained-proposal behavior.
func TestDispatchLaneFor(t *testing.T) {
	setLaneRegistry(t, map[string][]byte{
		"report-mutating.md":     []byte("---\noutput: report\ntools: [read, bash]\n---\nbody"),
		"report-readonly.md":     []byte("---\noutput: report\ntools: [read, grep, glob]\n---\nbody"),
		"report-unknown-tool.md": []byte("---\noutput: report\ntools: [read, some_future_tool]\n---\nbody"),
		"proposal-explicit.md":   []byte("---\noutput: proposal\ntools: [read]\n---\nbody"),
		"unmarked-mutating.md":   []byte("---\ntools: [read, write]\n---\nbody"),
		"unmarked-readonly.md":   []byte("---\ntools: [read]\n---\nbody"),
		"output-garbage.md":      []byte("---\noutput: maybe\ntools: [read]\n---\nbody"),
	})

	tests := []struct {
		role string
		want DispatchLane
	}{
		{"report-mutating", LaneRetainedReport},
		{"report-readonly", LaneShared},
		// Tools outside the read-only whitelist mark the role mutating, so
		// a tool the classification has not caught up with still gets
		// isolation.
		{"report-unknown-tool", LaneRetainedReport},
		{"proposal-explicit", LaneRetainedProposal},
		// Unmarked roles never change behavior under a new default.
		{"unmarked-mutating", LaneRetainedProposal},
		{"unmarked-readonly", LaneRetainedProposal},
		// An unknown output value normalizes to the default.
		{"output-garbage", LaneRetainedProposal},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			if got := DispatchLaneFor(SubAgentRole(tt.role)); got != tt.want {
				t.Errorf("DispatchLaneFor(%q) = %q, want %q", tt.role, got, tt.want)
			}
		})
	}

	// An empty tool list mutates nothing: a report role with no tools is
	// safe to share.
	setLaneRegistry(t, map[string][]byte{
		"no-tools.md": []byte("---\noutput: report\n---\nbody"),
	})
	if got := DispatchLaneFor(SubAgentRole("no-tools")); got != LaneShared {
		t.Errorf("no-tools lane = %q, want shared", got)
	}

	// The normalizer warns and falls back, so the frontmatter survives
	// round-trips with the garbage value intact (rolefile owns the file
	// format; the lane layer only reads).
	if p := RoleProfileFor(SubAgentRole("output-garbage")); p.Output != "" {
		t.Errorf("garbage output should normalize to empty, got %q", p.Output)
	}
}
