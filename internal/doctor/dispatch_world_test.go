package doctor

import (
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
)

func TestCheckDispatchWorld(t *testing.T) {
	t.Run("auto reports the worktree without a container image", func(t *testing.T) {
		check := CheckDispatchWorld(&config.Config{}, nil)
		if check.Status != "OK" {
			t.Fatalf("got %+v, want OK", check)
		}
		if !strings.Contains(check.Detail, "worktree") {
			t.Fatalf("detail must name the resolved world: %q", check.Detail)
		}
	})

	t.Run("explicit container without an image is a preflight FAIL", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Agent.SubAgent.World = "container"
		check := CheckDispatchWorld(cfg, nil)
		if check.Status != "FAIL" {
			t.Fatalf("got %+v, want FAIL — a container request must not surface mid-run", check)
		}
		if !strings.Contains(check.Detail, "container") {
			t.Fatalf("detail = %q", check.Detail)
		}
	})

	t.Run("shared mode has no world", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Agent.SubAgent.Mode = "shared"
		check := CheckDispatchWorld(cfg, nil)
		if check.Status != "OK" || !strings.Contains(check.Detail, "no dispatch world") {
			t.Fatalf("got %+v, want the shared-lane note", check)
		}
	})

	t.Run("a configured image reports report-lane containers", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("per-dispatch containers are offered on Linux only")
		}
		cfg := &config.Config{}
		cfg.Workspace.Containerd.Image = "registry.example/yaah-workspace:latest"
		check := CheckDispatchWorld(cfg, nil)
		if check.Status != "OK" {
			t.Fatalf("got %+v, want OK", check)
		}
		if !strings.Contains(check.Detail, "container for report lanes") || !strings.Contains(check.Detail, "croft-dlb") {
			t.Fatalf("detail must name the lane split and the gate: %q", check.Detail)
		}
	})
}
