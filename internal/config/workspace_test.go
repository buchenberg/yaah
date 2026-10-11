package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoad_workspaceDefaults pins the no-stanza and no-file behaviour: the
// local backend and the /workspace containment root, so the legacy path is
// selected by default rather than by accident.
func TestLoad_workspaceDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workspace.Backend != BackendLocal {
		t.Errorf("Backend = %q, want %q (the legacy default)", cfg.Workspace.Backend, BackendLocal)
	}
	if cfg.Workspace.Root != "/workspace" {
		t.Errorf("Root = %q, want /workspace", cfg.Workspace.Root)
	}
	if cfg.Workspace.Containerd.Namespace != "default" {
		t.Errorf("Namespace = %q, want the normalized default", cfg.Workspace.Containerd.Namespace)
	}
}

// TestLoad_workspaceStanza pins the parse and normalize behaviour for a real
// containerd stanza, including ${VAR} substitution on the socket address.
func TestLoad_workspaceStanza(t *testing.T) {
	t.Setenv("YAAH_WORKSPACE_SOCKET", "/run/containerd/containerd.sock")
	writeConfig(t, `
workspace:
  backend: containerd
  root: /sandbox
  containerd:
    address: ${YAAH_WORKSPACE_SOCKET}
    namespace: prod
    image: example/dev:latest
    snapshotter: zfs
    fifo_dir: /tmp/yaah-prod-fifo
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workspace.Backend != BackendContainerd {
		t.Errorf("Backend = %q, want containerd", cfg.Workspace.Backend)
	}
	if cfg.Workspace.Root != "/sandbox" {
		t.Errorf("Root = %q, want /sandbox", cfg.Workspace.Root)
	}
	if cfg.Workspace.Containerd.Address != "/run/containerd/containerd.sock" {
		t.Errorf("Address = %q, want the substituted socket path", cfg.Workspace.Containerd.Address)
	}
	if cfg.Workspace.Containerd.Namespace != "prod" {
		t.Errorf("Namespace = %q, want prod", cfg.Workspace.Containerd.Namespace)
	}
	if cfg.Workspace.Containerd.Image != "example/dev:latest" {
		t.Errorf("Image = %q", cfg.Workspace.Containerd.Image)
	}
	if cfg.Workspace.Containerd.Snapshotter != "zfs" {
		t.Errorf("Snapshotter = %q, want zfs", cfg.Workspace.Containerd.Snapshotter)
	}
	if cfg.Workspace.Containerd.FIFODir != "/tmp/yaah-prod-fifo" {
		t.Errorf("FIFODir = %q, want the configured fifo directory", cfg.Workspace.Containerd.FIFODir)
	}
}

// TestLoad_workspaceStanzaDefaults pins that a stanza naming only the backend
// still yields a fully-formed config.
func TestLoad_workspaceStanzaDefaults(t *testing.T) {
	writeConfig(t, `
workspace:
  backend: containerd
  containerd:
    image: example/dev:latest
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workspace.Root != "/workspace" {
		t.Errorf("Root = %q, want the /workspace default", cfg.Workspace.Root)
	}
	if cfg.Workspace.Containerd.Namespace != "default" {
		t.Errorf("Namespace = %q, want the normalized default", cfg.Workspace.Containerd.Namespace)
	}
	// An unset fifo_dir normalizes to a per-user dir under the system temp
	// dir: containerd's own default is root-only, so it would refuse the
	// client's FIFO creation for an unprivileged session.
	want := filepath.Join(os.TempDir(), fmt.Sprintf("yaah-fifo-%d", os.Getuid()))
	if cfg.Workspace.Containerd.FIFODir != want {
		t.Errorf("FIFODir = %q, want the normalized default %q", cfg.Workspace.Containerd.FIFODir, want)
	}
}

// TestValidate_workspace covers every rule in validateWorkspace, including the
// platform rule driven through its parameter so the test runs on any OS.
func TestValidate_workspace(t *testing.T) {
	cases := []struct {
		name    string
		ws      WorkspaceConfig
		goos    string
		wantErr string // substring; empty means the config must be valid
	}{
		{
			name: "local backend is always valid",
			ws:   WorkspaceConfig{Backend: BackendLocal},
		},
		{
			name: "empty backend defaults to local and is valid",
			ws:   WorkspaceConfig{},
		},
		{
			name:    "unknown backend is refused with the valid options",
			ws:      WorkspaceConfig{Backend: "podman"},
			wantErr: `workspace.backend "podman" is invalid (must be local or containerd)`,
		},
		{
			name: "containerd on linux with image and root is valid",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "/workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest"},
			},
			goos: "linux",
		},
		{
			name: "containerd off linux fails at load with the platform named",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "/workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest"},
			},
			goos:    "windows",
			wantErr: "windows cannot run one (native Linux or WSL2 required)",
		},
		{
			name: "containerd without an image is refused",
			ws: WorkspaceConfig{
				Backend: BackendContainerd,
				Root:    "/workspace",
			},
			goos:    "linux",
			wantErr: "workspace.containerd.image is required",
		},
		{
			name: "relative root is refused",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest"},
			},
			goos:    "linux",
			wantErr: `workspace.root "workspace" must be an absolute path inside the sandbox`,
		},
		{
			name: "root / is refused as a whole-container scope",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "/",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest"},
			},
			goos:    "linux",
			wantErr: `would scope the whole container`,
		},
		{
			// path.Clean collapses "//" and "/a/.." to "/", so the rule must
			// judge the cleaned value or these aliases would slip a
			// whole-container scope through the "/" refusal.
			name: "root aliases that clean to / are refused too",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "//",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest"},
			},
			goos:    "linux",
			wantErr: `would scope the whole container`,
		},
		{
			name: "relative fifo dir is refused",
			ws: WorkspaceConfig{
				Backend: BackendContainerd,
				Root:    "/workspace",
				Containerd: ContainerdWorkspaceConfig{
					Image:   "example/dev:latest",
					FIFODir: "yaah-fifo",
				},
			},
			goos:    "linux",
			wantErr: `workspace.containerd.fifo_dir "yaah-fifo" must be an absolute path`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateWorkspace(&tc.ws, tc.goos)
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("validateWorkspace = %v, want no errors", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("validateWorkspace accepted an invalid config")
			}
			for _, e := range errs {
				if !strings.Contains(e, tc.wantErr) {
					t.Errorf("error %q does not contain %q", e, tc.wantErr)
				}
			}
		})
	}
}

// TestValidate_workspaceSurfacedInConfigErrors pins that the workspace rules
// reach the caller through Validate's fail-fast aggregation, not just through
// the helper.
func TestValidate_workspaceSurfacedInConfigErrors(t *testing.T) {
	cfg := defaultConfig()
	cfg.Workspace = WorkspaceConfig{Backend: "podman"}
	err := Validate(cfg)
	var ve ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate = %v, want a ValidationError", err)
	}
	found := false
	for _, e := range ve.Errors {
		if strings.Contains(e, `workspace.backend "podman" is invalid`) {
			found = true
		}
	}
	if !found {
		t.Errorf("Validate errors %v do not include the workspace backend rule", ve.Errors)
	}
}
