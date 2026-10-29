package worldinfo

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tui/colors"
	worldstatus "github.com/buchenberg/yaah/internal/worldinfo"
)

func testTheme() *colors.Theme {
	th := colors.NewDarkTheme()
	return &th
}

func TestFormatZeroHidesSection(t *testing.T) {
	if got := Format(worldstatus.Status{}, testTheme()); got != "" {
		t.Fatalf("zero Status rendered %q, want empty", got)
	}
}

func TestFormatLocalDefault(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Derive(&config.Config{}, "", false)
	got := Format(s, thp)
	for _, want := range []string{"World", "Workspace", "local", "unrestricted", "Mode", "retained", "Dispatch", "auto → worktree"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatIsolatedShowsImageAndRoot(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Status{
		Backend:       "containerd",
		Isolated:      true,
		Root:          "/workspace",
		Image:         "registry.example/yaah:1",
		Mode:          "retained",
		Containment:   "auto",
		Network:       "allow_all",
		WorldReq:      "auto",
		ProposalWorld: "worktree",
		ReportWorld:   "worktree",
	}
	got := Format(s, thp)
	for _, want := range []string{"containerd", "isolated", "registry.example/yaah:1", "/workspace"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatSharedMode(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Status{
		Backend:       "local",
		Mode:          "shared",
		Containment:   "auto",
		Network:       "allow_all",
		WorldReq:      "auto",
		ProposalWorld: "shared",
		ReportWorld:   "shared",
	}
	got := Format(s, thp)
	if !strings.Contains(got, "shared · no dispatch world") {
		t.Fatalf("shared mode not named:\n%s", got)
	}
}

func TestFormatLanesDiffer(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Status{
		Backend:       "containerd",
		Isolated:      true,
		Root:          "/workspace",
		Mode:          "retained",
		Containment:   "auto",
		Network:       "allow_all",
		WorldReq:      "auto",
		ProposalWorld: "shared",
		ReportWorld:   "container",
	}
	got := Format(s, thp)
	if !strings.Contains(got, "proposal shared · report container") {
		t.Fatalf("lane difference not named:\n%s", got)
	}
}
