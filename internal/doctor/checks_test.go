package doctor

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/config"
)

// TestCheckPipeline_MatchesSourceOfTruth pins the doctor's pipeline
// report to the pipeline package's defaults — the report used to carry
// its own copy of the middleware list that drifted (review B10e).
func TestCheckPipeline_MatchesSourceOfTruth(t *testing.T) {
	check := CheckPipeline(&config.Config{}, nil)
	if check.Status != "OK" {
		t.Fatalf("status = %q, want OK", check.Status)
	}
	for _, name := range pipeline.DefaultPipelineNames() {
		if !strings.Contains(check.Detail, name) {
			t.Errorf("pipeline report %q missing default middleware %q", check.Detail, name)
		}
	}
	if strings.Contains(check.Detail, "staleness") {
		t.Errorf("pipeline report still lists deleted middleware staleness: %q", check.Detail)
	}
}

func TestCheckPipeline_AdditiveEnabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Middleware.Enabled = []string{"shepherd_trace"}
	check := CheckPipeline(cfg, nil)
	if !strings.Contains(check.Detail, "shepherd_trace") {
		t.Errorf("enabled entry missing from report: %q", check.Detail)
	}
	for _, name := range pipeline.DefaultPipelineNames() {
		if !strings.Contains(check.Detail, name) {
			t.Errorf("default middleware %q dropped when enabled list is set: %q", name, check.Detail)
		}
	}
}

func TestCheckPipeline_Disabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Middleware.Disabled = []string{"approval"}
	check := CheckPipeline(cfg, nil)
	active, _, _ := strings.Cut(check.Detail, " (disabled")
	if strings.Contains(active, "approval") {
		t.Errorf("disabled middleware still reported active: %q", check.Detail)
	}
	if !strings.Contains(check.Detail, "disabled: approval") {
		t.Errorf("report should mention disabled middleware: %q", check.Detail)
	}
}

func TestCheckPipeline_ConfigError(t *testing.T) {
	check := CheckPipeline(nil, errors.New("boom"))
	if check.Status != "WARN" {
		t.Errorf("status = %q, want WARN", check.Status)
	}
}

// TestCheckWorkspace_LocalMode: no workspace stanza means the local
// substrate, byte-for-byte the legacy behaviour.
func TestCheckWorkspace_LocalMode(t *testing.T) {
	check := CheckWorkspace(&config.Config{}, nil)
	if check.Status != "OK" || !strings.Contains(check.Detail, "local") {
		t.Errorf("local mode: %+v", check)
	}
}

// TestCheckWorkspace_LocalExplicit: an explicit "local" reads the same.
func TestCheckWorkspace_LocalExplicit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Workspace.Backend = config.BackendLocal
	check := CheckWorkspace(cfg, nil)
	if check.Status != "OK" || !strings.Contains(check.Detail, "local") {
		t.Errorf("explicit local: %+v", check)
	}
}

// TestCheckWorkspace_Isolated reports the image, namespace, root, and the
// linked backends — the facts §2.4 wants visible. The platform gate is
// asserted too: config validation refuses non-Linux at load, so the FAIL
// branch is only reachable by reading a Linux-written config elsewhere.
func TestCheckWorkspace_Isolated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Workspace.Backend = config.BackendContainerd
	cfg.Workspace.Root = "/workspace"
	cfg.Workspace.Containerd = config.ContainerdWorkspaceConfig{
		Address:   "/run/containerd/containerd.sock",
		Namespace: "yaah",
		Image:     "registry.example/yaah-workspace:latest",
	}
	check := CheckWorkspace(cfg, nil)
	if runtime.GOOS != "linux" {
		if check.Status != "FAIL" {
			t.Errorf("non-Linux isolated mode must FAIL, got %+v", check)
		}
		return
	}
	if check.Status != "OK" {
		t.Fatalf("isolated mode on Linux: %+v", check)
	}
	for _, want := range []string{
		"registry.example/yaah-workspace:latest", // the image
		"yaah",                                   // the namespace
		"/workspace",                             // the containment root
		"containerd",                             // a linked backend
	} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("isolated report %q missing %q", check.Detail, want)
		}
	}
}

func TestCheckWorkspace_UnknownBackend(t *testing.T) {
	cfg := &config.Config{}
	cfg.Workspace.Backend = "kubernetes"
	check := CheckWorkspace(cfg, nil)
	if check.Status != "FAIL" {
		t.Errorf("unknown backend must FAIL, got %+v", check)
	}
}

func TestCheckWorkspace_ConfigError(t *testing.T) {
	check := CheckWorkspace(nil, errors.New("boom"))
	if check.Status != "WARN" {
		t.Errorf("status = %q, want WARN", check.Status)
	}
}
