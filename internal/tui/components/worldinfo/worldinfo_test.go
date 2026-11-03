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

func TestRowsZeroHidden(t *testing.T) {
	if got := AgentRows(worldstatus.Status{}, testTheme()); got != "" {
		t.Fatalf("zero Status rendered %q, want empty", got)
	}
	if got := SubAgentRows(worldstatus.Status{}, testTheme()); got != "" {
		t.Fatalf("zero Status rendered %q, want empty", got)
	}
}

func TestAgentRowsLocalDefault(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Derive(&config.Config{}, "", false)
	got := AgentRows(s, thp)
	for _, want := range []string{"Workspace", "local", "unrestricted"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	// Workspace/scope rows must not leak into the sub-agent rows.
	sub := SubAgentRows(s, thp)
	if strings.Contains(sub, "Workspace") || strings.Contains(sub, "unrestricted") {
		t.Errorf("sub-agent rows leaked workspace rows:\n%s", sub)
	}
}

func TestSubAgentRowsLocalDefault(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Derive(&config.Config{}, "", false)
	got := SubAgentRows(s, thp)
	for _, want := range []string{"Containment", "Network", "Dispatch", "retained · ", "auto → worktree"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestAgentRowsIsolatedShowsRoot(t *testing.T) {
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
	got := AgentRows(s, thp)
	for _, want := range []string{"containerd", "isolated", "/workspace"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	// The image name is dropped from the pane (the field stays on Status).
	if strings.Contains(got, "registry.example/yaah:1") {
		t.Errorf("image name should be dropped from the workspace row:\n%s", got)
	}
}

func TestSubAgentRowsSharedMode(t *testing.T) {
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
	got := SubAgentRows(s, thp)
	if !strings.Contains(got, "shared · no dispatch world") {
		t.Fatalf("shared mode not named:\n%s", got)
	}
}

func TestSubAgentRowsLanesDiffer(t *testing.T) {
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
	got := SubAgentRows(s, thp)
	if !strings.Contains(got, "proposal shared · report container") {
		t.Fatalf("lane difference not named:\n%s", got)
	}
}

func TestSubAgentRowsCollapsedSameWorld(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Status{
		Backend:       "containerd",
		Mode:          "retained",
		Containment:   "auto",
		Network:       "allow_all",
		WorldReq:      "container",
		ProposalWorld: "container",
		ReportWorld:   "container",
	}
	got := SubAgentRows(s, thp)
	if strings.Contains(got, "container → container") {
		t.Fatalf("dispatch ladder not collapsed:\n%s", got)
	}
	if !strings.Contains(got, "Dispatch") {
		t.Fatalf("dispatch row missing:\n%s", got)
	}
}

func TestSubAgentRowsContainmentSplit(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Status{
		Backend:             "containerd",
		Mode:                "retained",
		Containment:         "auto",
		ContainmentResolved: "paddock",
		JailOK:              true,
		JailName:            "Seatbelt",
		JailTier:            "native-syscall-deny",
		Network:             "deny_all",
		WorldReq:            "worktree",
		ProposalWorld:       "worktree",
		ReportWorld:         "worktree",
	}
	got := SubAgentRows(s, thp)
	for _, want := range []string{"Containment:", "paddock", "Paddock:", "Seatbelt · native-syscall-deny"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "jail") || strings.Contains(got, "Jail") {
		t.Fatalf("public metaphor leaks the jail wording:\n%s", got)
	}
}

func TestSubAgentRowsCroftLane(t *testing.T) {
	thp := testTheme()
	s := worldstatus.Status{
		Backend:             "containerd",
		Mode:                "retained",
		Containment:         "auto",
		ContainmentResolved: "paddock",
		JailOK:              true,
		JailName:            "Landlock",
		JailTier:            "native-syscall-deny",
		Network:             "deny_all",
		WorldReq:            "container",
		ProposalWorld:       "container",
		ReportWorld:         "container",
	}
	got := SubAgentRows(s, thp)
	if !strings.Contains(got, "Containment:") || !strings.Contains(got, "croft") {
		t.Fatalf("croft lane does not name the container boundary:\n%s", got)
	}
	if strings.Contains(got, "Paddock:") {
		t.Fatalf("paddock row shown on a lane that carries its own boundary:\n%s", got)
	}
}
