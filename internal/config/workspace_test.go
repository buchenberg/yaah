package config

import (
	"errors"
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
	// An unset fifo_dir normalizes into the yaah home: containerd's own
	// default is root-only, and a predictable name under the shared temp
	// dir could be pre-created by another local user to deny or subvert
	// the session's FIFO creation. When the home does not resolve
	// absolutely the field stays empty (validation then names it), so the
	// expectation mirrors the guard.
	want := filepath.Join(HomeDir(), "containerd-fifo")
	if !filepath.IsAbs(want) {
		want = ""
	}
	if cfg.Workspace.Containerd.FIFODir != want {
		t.Errorf("FIFODir = %q, want the normalized default %q", cfg.Workspace.Containerd.FIFODir, want)
	}
}

// TestLoad_relativeHomeFifoFallback pins the reachable broken-home case
// end to end: a relative $YAAH_HOME makes the fifo_dir default relative,
// so normalize must leave the field empty and validation must refuse the
// stanza with the cause named — not let the adapter fall back silently
// to its root-only default. (With $HOME unset instead, Load fails earlier
// at the config path, so the relative home is the case that reaches here.)
func TestLoad_relativeHomeFifoFallback(t *testing.T) {
	// A relative home resolves the config path against the process cwd,
	// so chdir into a scratch root and plant the stanza under it.
	dir := t.TempDir()
	t.Chdir(dir)
	rel := filepath.Join(dir, "relative", "yaah")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "config.yaml"),
		[]byte("workspace:\n  backend: containerd\n  containerd:\n    image: example/dev:latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAAH_HOME", "relative/yaah")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workspace.Containerd.FIFODir != "" {
		t.Errorf("FIFODir = %q, want empty rather than a relative default", cfg.Workspace.Containerd.FIFODir)
	}
	err = Validate(cfg)
	if err == nil {
		t.Fatal("Validate must refuse an empty fifo_dir for the containerd backend")
	}
	if !strings.Contains(err.Error(), "fifo_dir is empty") || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %v, want the fifo_dir emptiness and its cause named", err)
	}
}

// absFifoDir is a fifo_dir value the IsAbs rule accepts on every host OS
// the suite runs on: the rule judges host paths, and os.TempDir() returns
// $TMPDIR verbatim — which can itself be relative — so absolutize the
// join rather than trust the environment.
func absFifoDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(os.TempDir(), "yaah-test-fifo"))
	if err != nil {
		t.Fatalf("absolutize fifo dir: %v", err)
	}
	return dir
}

// TestValidate_workspace covers every rule in validateWorkspace, including the
// platform rule driven through its parameter so the test runs on any OS.
func TestValidate_workspace(t *testing.T) {
	fifo := absFifoDir(t)
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
			// A local session that names an image offers per-dispatch
			// container worlds to report lanes (yaah-ihfh.3), so the
			// settings those containers consume are validated too.
			name: "local session with a container image validates the sandbox scope",
			ws: WorkspaceConfig{
				Backend:    BackendLocal,
				Root:       "/",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest", FIFODir: fifo},
			},
			goos:    "linux",
			wantErr: `would scope the whole container`,
		},
		{
			name: "local session with a valid container image is valid",
			ws: WorkspaceConfig{
				Backend:    BackendLocal,
				Root:       "/workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest", FIFODir: fifo},
			},
			goos: "linux",
		},
		{
			// The platform rule stays the session container's: a local
			// session itself runs on the host, and the dispatch lane is
			// simply unavailable off-Linux.
			name: "local session with a container image is valid off linux",
			ws: WorkspaceConfig{
				Backend:    BackendLocal,
				Root:       "/workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest", FIFODir: fifo},
			},
			goos: "windows",
		},
		{
			name:    "unknown backend is refused with the valid options",
			ws:      WorkspaceConfig{Backend: "podman"},
			wantErr: `workspace.backend "podman" is invalid (must be local or containerd)`,
		},
		{
			name: "containerd on linux with image and root is valid",
			ws: WorkspaceConfig{
				Backend: BackendContainerd,
				Root:    "/workspace",
				Containerd: ContainerdWorkspaceConfig{
					Image:   "example/dev:latest",
					FIFODir: fifo,
				},
			},
			goos: "linux",
		},
		{
			name: "containerd off linux fails at load with the platform named",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "/workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest", FIFODir: fifo},
			},
			goos:    "windows",
			wantErr: "windows cannot run one (native Linux or WSL2 required)",
		},
		{
			name: "containerd without an image is refused",
			ws: WorkspaceConfig{
				Backend: BackendContainerd,
				Root:    "/workspace",
				Containerd: ContainerdWorkspaceConfig{
					FIFODir: fifo,
				},
			},
			goos:    "linux",
			wantErr: "workspace.containerd.image is required",
		},
		{
			name: "relative root is refused",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest", FIFODir: fifo},
			},
			goos:    "linux",
			wantErr: `workspace.root "workspace" must be an absolute path inside the sandbox`,
		},
		{
			name: "root / is refused as a whole-container scope",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "/",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest", FIFODir: fifo},
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
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest", FIFODir: fifo},
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
		{
			// Empty is what normalizeWorkspace leaves when the yaah home
			// does not resolve absolutely; the adapter's own default is
			// root-only, so validation must refuse it with the cause named
			// instead of letting an unprivileged session fail later at FIFO
			// creation.
			name: "empty fifo dir is refused with the home named",
			ws: WorkspaceConfig{
				Backend:    BackendContainerd,
				Root:       "/workspace",
				Containerd: ContainerdWorkspaceConfig{Image: "example/dev:latest"},
			},
			goos:    "linux",
			wantErr: "workspace.containerd.fifo_dir is empty",
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
