package worldinfo

import (
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
)

func TestStatusIsZero(t *testing.T) {
	if !(Status{}).IsZero() {
		t.Fatal("zero Status must report IsZero")
	}
	if (Status{Backend: "local"}).IsZero() {
		t.Fatal("populated Status must not report IsZero")
	}
}

func TestDeriveLocalDefault(t *testing.T) {
	s := Derive(&config.Config{}, "", false)
	if s.Backend != config.BackendLocal {
		t.Fatalf("Backend = %q, want local", s.Backend)
	}
	if s.Isolated {
		t.Fatal("Isolated = true, want false")
	}
	if s.Root != "" {
		t.Fatalf("Root = %q, want unrestricted", s.Root)
	}
	if s.Mode != "retained" || s.Containment != "auto" || s.Network != "allow_all" {
		t.Fatalf("defaults = %+v", s)
	}
	if s.WorldReq != "auto" {
		t.Fatalf("WorldReq = %q, want auto", s.WorldReq)
	}
	if s.ProposalWorld != "worktree" || s.ReportWorld != "worktree" {
		t.Fatalf("resolved = proposal %q report %q, want worktree/worktree", s.ProposalWorld, s.ReportWorld)
	}
	if s.Degraded {
		t.Fatalf("Degraded = true on the default local session: %+v", s)
	}
}

func TestDeriveWorkspaceRoot(t *testing.T) {
	s := Derive(&config.Config{}, "/tmp/agent-root", false)
	if s.Root != "/tmp/agent-root" {
		t.Fatalf("Root = %q, want the --workspace path", s.Root)
	}
}

func TestDeriveIsolated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Workspace.Backend = config.BackendContainerd
	cfg.Workspace.Root = "/work"
	cfg.Workspace.Containerd.Image = "registry.example/yaah:1"
	s := Derive(cfg, "/ignored", true)
	if s.Backend != config.BackendContainerd || !s.Isolated {
		t.Fatalf("workspace = %+v", s)
	}
	if s.Root != "/work" {
		t.Fatalf("Root = %q, want the in-sandbox root", s.Root)
	}
	if s.Image != "registry.example/yaah:1" {
		t.Fatalf("Image = %q", s.Image)
	}
}

func TestDeriveModeSharedHasNoWorld(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.SubAgent.Mode = "shared"
	s := Derive(cfg, "", false)
	if s.Mode != "shared" {
		t.Fatalf("Mode = %q", s.Mode)
	}
	if s.ProposalWorld != "shared" || s.ReportWorld != "shared" {
		t.Fatalf("resolved = %q/%q, want shared/shared", s.ProposalWorld, s.ReportWorld)
	}
	if s.Degraded {
		t.Fatalf("shared mode degraded: %+v", s)
	}
}

func TestDeriveExplicitContainerWithoutImageUnavailable(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.SubAgent.World = "container"
	s := Derive(cfg, "", false)
	if s.ProposalWorld != "unavailable" || s.ReportWorld != "unavailable" {
		t.Fatalf("resolved = %q/%q, want unavailable", s.ProposalWorld, s.ReportWorld)
	}
	if !s.Degraded || !strings.Contains(s.Reason, "container") {
		t.Fatalf("degradation = %v %q", s.Degraded, s.Reason)
	}
}

func TestDeriveImageOffersContainer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("per-dispatch containers are offered on Linux only")
	}
	cfg := &config.Config{}
	cfg.Workspace.Containerd.Image = "registry.example/yaah:1"
	s := Derive(cfg, "", false)
	if s.ProposalWorld != "container" || s.ReportWorld != "container" {
		t.Fatalf("resolved = %q/%q, want container/container", s.ProposalWorld, s.ReportWorld)
	}
	if s.Degraded {
		t.Fatalf("Degraded = true with a container available: %+v", s)
	}
}

func TestDeriveContainerSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("per-dispatch containers are offered on Linux only")
	}
	cfg := &config.Config{}
	cfg.Workspace.Backend = config.BackendContainerd
	cfg.Workspace.Containerd.Image = "registry.example/yaah:1"
	s := Derive(cfg, "", true)
	if s.ProposalWorld != "shared" {
		t.Fatalf("ProposalWorld = %q, want shared (container-session proposals are routed shared)", s.ProposalWorld)
	}
	if s.ReportWorld != "container" {
		t.Fatalf("ReportWorld = %q, want container", s.ReportWorld)
	}
}

func TestDeriveBrokerHosts(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.SubAgent.Network = "broker"
	cfg.Agent.SubAgent.NetworkHosts = []string{"a.example", "b.example"}
	s := Derive(cfg, "", false)
	if s.Network != "broker" || s.NetworkHosts != 2 {
		t.Fatalf("network = %q hosts = %d", s.Network, s.NetworkHosts)
	}
}

func TestDeriveNeverIncludesSocket(t *testing.T) {
	cfg := &config.Config{}
	cfg.Workspace.Backend = config.BackendContainerd
	cfg.Workspace.Containerd.Address = "/run/containerd/secret.sock"
	cfg.Workspace.Containerd.Image = "img"
	s := Derive(cfg, "", true)
	rendered := strings.Join([]string{
		s.Backend, s.Root, s.Image, s.Mode, s.Containment,
		s.Network, s.WorldReq, s.ProposalWorld, s.ReportWorld, s.Reason,
	}, " ")
	if strings.Contains(rendered, "secret.sock") {
		t.Fatalf("status leaked the containerd socket: %q", rendered)
	}
}

func TestResolveRequestedDefaults(t *testing.T) {
	if got := RequestedWorld(&config.Config{}); got != "auto" {
		t.Fatalf("RequestedWorld = %q", got)
	}
	cfg := &config.Config{}
	cfg.Agent.SubAgent.World = " Container "
	if got := RequestedWorld(cfg); got != "container" {
		t.Fatalf("RequestedWorld = %q, want normalized container", got)
	}
}

func TestContainmentNormalization(t *testing.T) {
	cases := map[string]string{
		"":         "auto",
		"auto":     "auto",
		"off":      "off",
		"advisory": "off",
		"paddock":  "paddock",
		"jail":     "paddock",
	}
	for in, want := range cases {
		cfg := &config.Config{}
		cfg.Agent.SubAgent.Containment = in
		if got := Containment(cfg); got != want {
			t.Errorf("Containment(%q) = %q, want %q", in, got, want)
		}
	}
}
