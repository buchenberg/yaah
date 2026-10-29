package yaah

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/worldinfo"
)

func TestWorldBannerLines(t *testing.T) {
	t.Run("zero status prints nothing", func(t *testing.T) {
		if got := worldBannerLines(worldinfo.Status{}); len(got) != 0 {
			t.Fatalf("lines = %v, want none", got)
		}
	})

	t.Run("local default", func(t *testing.T) {
		lines := worldBannerLines(worldinfo.Derive(nil, "", false))
		if len(lines) != 0 {
			t.Fatalf("nil config lines = %v, want none", lines)
		}
		s := worldinfo.Status{
			Backend:       "local",
			Mode:          "retained",
			Containment:   "auto",
			Network:       "allow_all",
			WorldReq:      "auto",
			ProposalWorld: "worktree",
			ReportWorld:   "worktree",
		}
		lines = worldBannerLines(s)
		if len(lines) != 2 {
			t.Fatalf("lines = %v, want 2", lines)
		}
		for _, want := range []string{"workspace: local", "unrestricted", "mode: retained"} {
			if !strings.Contains(lines[0], want) {
				t.Errorf("line 1 missing %q: %q", want, lines[0])
			}
		}
		for _, want := range []string{"world: auto → worktree", "containment: auto", "network: allow_all"} {
			if !strings.Contains(lines[1], want) {
				t.Errorf("line 2 missing %q: %q", want, lines[1])
			}
		}
	})

	t.Run("shared mode", func(t *testing.T) {
		s := worldinfo.Status{
			Backend:       "local",
			Mode:          "shared",
			Containment:   "auto",
			Network:       "allow_all",
			WorldReq:      "auto",
			ProposalWorld: "shared",
			ReportWorld:   "shared",
		}
		lines := worldBannerLines(s)
		if len(lines) != 2 || !strings.Contains(lines[1], "shared · no dispatch world") {
			t.Fatalf("lines = %v", lines)
		}
	})

	t.Run("degraded reason shown", func(t *testing.T) {
		s := worldinfo.Status{
			Backend:       "local",
			Mode:          "retained",
			Containment:   "auto",
			Network:       "allow_all",
			WorldReq:      "auto",
			ProposalWorld: "worktree",
			ReportWorld:   "worktree",
			Degraded:      true,
			Reason:        "container world unavailable",
		}
		lines := worldBannerLines(s)
		if len(lines) != 2 || !strings.Contains(lines[1], "container world unavailable") {
			t.Fatalf("lines = %v", lines)
		}
	})
}

func TestWorldSuffix(t *testing.T) {
	if got := worldSuffix("", false, ""); got != "" {
		t.Fatalf("empty world suffix = %q", got)
	}
	got := worldSuffix("worktree", true, "no image")
	if !strings.Contains(got, "worktree") || !strings.Contains(got, "degraded") {
		t.Fatalf("suffix = %q", got)
	}
}
