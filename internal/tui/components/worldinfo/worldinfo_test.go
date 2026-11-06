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
	for _, want := range []string{"Containment:", "Seatbelt"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	// The Paddock row is gone: the Containment row names the mechanism, and
	// neither the placement vocabulary nor the enforcement tier leaks.
	for _, ban := range []string{"Paddock:", "paddock", "native-syscall-deny"} {
		if strings.Contains(got, ban) {
			t.Errorf("output leaks %q:\n%s", ban, got)
		}
	}
	if strings.Contains(got, "jail") || strings.Contains(got, "Jail") {
		t.Fatalf("public metaphor leaks the jail wording:\n%s", got)
	}

	s.JailOK = false
	s.JailName = ""
	s.JailTier = ""
	s.JailWhy = "no paddock backend on this host"
	unavailable := SubAgentRows(s, thp)
	if !strings.Contains(unavailable, "Unavailable") {
		t.Fatalf("unavailable containment is not a single word:\n%s", unavailable)
	}
	for _, ban := range []string{"unavailable ·", "unavailable on this host", "no paddock backend"} {
		if strings.Contains(unavailable, ban) {
			t.Errorf("output leaks the multi-word refusal %q:\n%s", ban, unavailable)
		}
	}
	if strings.Contains(unavailable, "jail") || strings.Contains(unavailable, "Jail") {
		t.Fatalf("public metaphor leaks the jail wording on the unavailable path:\n%s", unavailable)
	}

	// The fail-closed placement is a single word too.
	s.ContainmentResolved = "refused"
	refused := SubAgentRows(s, thp)
	if !strings.Contains(refused, "Refused") {
		t.Fatalf("refused containment is not a single word:\n%s", refused)
	}
	if strings.Contains(refused, "fail closed") || strings.Contains(refused, "refused (") {
		t.Errorf("output leaks the multi-word refusal:\n%s", refused)
	}

	// The leak guard must also cover the producer strings, not just the
	// renderer's echo: render a status derived from the real probe path.
	cfg := &config.Config{}
	cfg.Agent.SubAgent.Containment = "auto"
	derived := SubAgentRows(worldstatus.Derive(cfg, "workdir", false), thp)
	if strings.Contains(derived, "jail") || strings.Contains(derived, "Jail") {
		t.Fatalf("producer wording leaks the jail metaphor:\n%s", derived)
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
	if !strings.Contains(got, "Containment:") || !strings.Contains(got, "Croft") {
		t.Fatalf("croft lane does not name the container boundary:\n%s", got)
	}
	// The croft lane carries its own boundary: the host jail backend must not
	// leak into it, and no Paddock row is shown.
	if strings.Contains(got, "Landlock") {
		t.Fatalf("host jail backend named on the croft lane:\n%s", got)
	}
	if strings.Contains(got, "Paddock:") {
		t.Fatalf("paddock row shown on a lane that carries its own boundary:\n%s", got)
	}
}
