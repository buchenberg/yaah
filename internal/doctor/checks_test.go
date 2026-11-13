package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"

	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"
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
}

func TestCheckPipeline_PromptCachingKnob(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Default.PromptCaching = true
	check := CheckPipeline(cfg, nil)
	if !strings.Contains(check.Detail, "prompt_caching") {
		t.Errorf("prompt_caching knob missing from report: %q", check.Detail)
	}
}

func TestCheckPipeline_ConfigError(t *testing.T) {
	check := CheckPipeline(nil, errors.New("boom"))
	if check.Status != "WARN" {
		t.Errorf("status = %q, want WARN", check.Status)
	}
}

// absFifoDir is a fifo_dir value the IsAbs rule accepts on every host OS
// the suite runs on: os.TempDir() returns $TMPDIR verbatim — which can
// itself be relative — so absolutize the join.
func absFifoDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(os.TempDir(), "yaah-fifo"))
	if err != nil {
		t.Fatalf("absolutize fifo dir: %v", err)
	}
	return dir
}

// providerCheckConfig builds a minimal config whose default provider is
// credentialed, so the check reaches the per-provider credential loop.
func providerCheckConfig() *config.Config {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"good": {APIKey: "sk-real", BaseURL: "https://api.good.example/v1"},
		},
	}
	cfg.Agent.Default.Provider = "good"
	return cfg
}

// TestCheckProvider_EmptyKeyOnRemoteBaseURL pins the yaah-gkll.1 fix: an
// empty api_key against a remote base_url used to be skipped with a bare
// continue — the exact provider shape that dispatched 401s in PR #1573.
func TestCheckProvider_EmptyKeyOnRemoteBaseURL(t *testing.T) {
	cfg := providerCheckConfig()
	cfg.Providers["broken"] = config.Provider{BaseURL: "https://api.deepseek.com/v1"}

	check := CheckProvider(cfg, nil)
	if check.Status != "WARN" {
		t.Fatalf("status = %q, want WARN (got detail %q)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "broken") || !strings.Contains(check.Detail, "cannot authenticate") {
		t.Errorf("detail must name the provider and the failure, got: %q", check.Detail)
	}
}

// TestCheckProvider_UnsetEnvVarNamesTheVar keeps the actionable message:
// when the key is a ${VAR} reference that resolves to nothing, the check
// names the variable to export.
func TestCheckProvider_UnsetEnvVarNamesTheVar(t *testing.T) {
	t.Setenv("YAAH_DOCTOR_UNSET_KEY", "")
	cfg := providerCheckConfig()
	cfg.Providers["envref"] = config.Provider{
		APIKey:  "${YAAH_DOCTOR_UNSET_KEY}",
		BaseURL: "https://api.deepseek.com/v1",
	}

	check := CheckProvider(cfg, nil)
	if check.Status != "WARN" {
		t.Fatalf("status = %q, want WARN", check.Status)
	}
	if !strings.Contains(check.Detail, "YAAH_DOCTOR_UNSET_KEY") {
		t.Errorf("detail must name the unset env var, got: %q", check.Detail)
	}
}

// TestCheckProvider_LocalKeylessAndSetEnvAreClean: the keyless local
// endpoint, a set env ref, and the OAuth lane all stay OK — the tightened
// loop must not flag providers that can actually authenticate.
func TestCheckProvider_LocalKeylessAndSetEnvAreClean(t *testing.T) {
	t.Setenv("YAAH_DOCTOR_SET_KEY", "sk-from-env")
	cfg := providerCheckConfig()
	cfg.Providers["ollama"] = config.Provider{APIKey: "ollama", BaseURL: "http://localhost:11434/v1"}
	cfg.Providers["envref"] = config.Provider{APIKey: "${YAAH_DOCTOR_SET_KEY}", BaseURL: "https://api.deepseek.com/v1"}
	cfg.Providers["oauthp"] = config.Provider{Auth: "oauth", BaseURL: "https://oauth.example/v1"}

	check := CheckProvider(cfg, nil)
	if check.Status != "OK" {
		t.Errorf("status = %q, want OK (got detail %q)", check.Status, check.Detail)
	}
}

// TestCheckSubAgents_CredentialResolved pins the sub-agent-lane fix: a
// configured sub-agent provider whose credential does not resolve is a WARN
// — every dispatch would silently inherit the planner's provider — not an
// OK line that only proves the name exists.
func TestCheckSubAgents_CredentialResolved(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"planner": {APIKey: "sk-real", BaseURL: "https://api.good.example/v1"},
			"sub":     {BaseURL: "https://api.deepseek.com/v1"}, // no resolvable key
		},
	}
	cfg.Agent.Default.Provider = "planner"
	cfg.Agent.Default.Model = "main-model"
	cfg.Agent.SubAgent.Provider = "sub"

	check := CheckSubAgents(cfg, nil)
	if check.Status != "WARN" {
		t.Fatalf("status = %q, want WARN (got detail %q)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "no usable credential") || !strings.Contains(check.Detail, "planner") {
		t.Errorf("detail must state the inheritance fallback, got: %q", check.Detail)
	}
}

// TestCheckSubAgents_CredentialedProviderStaysOK: a sub-agent provider with
// a real key (and the OAuth lane, whose token lives in the store) reports
// the pre-existing OK shape.
func TestCheckSubAgents_CredentialedProviderStaysOK(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"sub":   {APIKey: "sk-real", BaseURL: "https://api.good.example/v1"},
			"oauth": {Auth: "oauth", BaseURL: "https://oauth.example/v1"},
		},
	}
	cfg.Agent.Default.Provider = "sub"
	cfg.Agent.Default.Model = "main-model"

	for name := range cfg.Providers {
		cfg.Agent.SubAgent.Provider = name
		check := CheckSubAgents(cfg, nil)
		if check.Status != "OK" {
			t.Errorf("%s: status = %q, want OK (got detail %q)", name, check.Status, check.Detail)
		}
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

// TestCheckWorkspace_Isolated reports the image, namespace, root, socket,
// and the linked backends — the facts §2.4 wants visible. The backends are
// asserted against the registry the detail was built from, not a literal,
// so the assertion holds in any binary (the doctor test binary links only
// the in-core git backend; the yaah binary links containerd too).
func TestCheckWorkspace_Isolated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Workspace.Backend = config.BackendContainerd
	cfg.Workspace.Root = "/workspace"
	cfg.Workspace.Containerd = config.ContainerdWorkspaceConfig{
		Address:   "/run/containerd/containerd.sock",
		Namespace: "yaah",
		Image:     "registry.example/yaah-workspace:latest",
		FIFODir:   absFifoDir(t),
	}
	check := CheckWorkspace(cfg, nil)
	if runtime.GOOS != "linux" {
		// Session wiring refuses an isolated config off-Linux
		// (config.Validate runs at wiring, not load), and doctor
		// reports the same refusal.
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
		"socket /run/containerd/containerd.sock", // the configured socket
		"sandbox library: goatherd-runtime sandbox/croft (containerd), supervision: shepherd (git)",
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

// TestCheckWorkspace_DefaultSocket: an empty address is a valid
// configuration — containerd's own endpoint resolution — and must still
// be accounted for in the output, not silently dropped.
func TestCheckWorkspace_DefaultSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the socket detail only renders on the Linux branch")
	}
	cfg := &config.Config{}
	cfg.Workspace.Backend = config.BackendContainerd
	cfg.Workspace.Root = "/workspace"
	cfg.Workspace.Containerd = config.ContainerdWorkspaceConfig{
		Namespace: "yaah",
		Image:     "registry.example/yaah-workspace:latest",
		FIFODir:   absFifoDir(t),
	}
	check := CheckWorkspace(cfg, nil)
	if check.Status != "OK" {
		t.Fatalf("a valid default-socket stanza: %+v", check)
	}
	if !strings.Contains(check.Detail, "socket containerd default") {
		t.Errorf("report %q must mark the default socket", check.Detail)
	}
}

// TestCheckWorkspace_UnusableStanzaFails: doctor's verdict must match
// session startup's — config.Validate runs at wiring, not load, so doctor
// runs the same workspace rules itself. Each case isolates one rule: the
// stanza is otherwise valid, so the failure names the broken rule.
func TestCheckWorkspace_UnusableStanzaFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the workspace validation detail only renders on the Linux branch")
	}
	for _, tc := range []struct {
		name   string
		image  string
		root   string
		wantIn string
	}{
		{"no image", "", "/workspace", "workspace.containerd.image"},
		{"whole-container root", "registry.example/yaah-workspace:latest", "/", "workspace.root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Workspace.Backend = config.BackendContainerd
			cfg.Workspace.Root = tc.root
			cfg.Workspace.Containerd.Image = tc.image
			check := CheckWorkspace(cfg, nil)
			if check.Status != "FAIL" || !strings.HasPrefix(check.Detail, "workspace.") || !strings.Contains(check.Detail, tc.wantIn) {
				t.Errorf("the %s case must FAIL naming its own rule, got %+v", tc.name, check)
			}
		})
	}
}

// TestCheckShepherd pins the R2 shepherd check's outcomes: a healthy
// trace store reports the recovery counts, and a store that cannot open
// fails loudly. Tracing is integral, so there is no "off" outcome.
func TestCheckShepherd(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// The root must fork to be recoverable: a root that never forked
	// left no trace record (the kernel's documented rule), and the
	// doctor's counts must show the rebuilt tree.
	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("sess-doc", goatherdgit.NewLocalGitSandbox(newDoctorGitRepo(t)))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if _, err := mgr.Fork(root.ID(), "sub-doc", nil); err != nil {
		t.Fatalf("fork: %v", err)
	}
	store.Close(context.Background())

	cfg := &config.Config{}
	cfg.Agent.Default.ShepherdTracePath = tracePath
	healthy := CheckShepherd(cfg, nil)
	if healthy.Status != "OK" || !strings.Contains(healthy.Detail, "2 scope(s) (1 root(s))") {
		t.Errorf("healthy check = %+v, want OK with recovery counts", healthy)
	}

	corrupt := filepath.Join(t.TempDir(), "trace.sqlite")
	if err := os.WriteFile(corrupt, []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatalf("write corrupt store: %v", err)
	}
	cfg.Agent.Default.ShepherdTracePath = corrupt
	broken := CheckShepherd(cfg, nil)
	if broken.Status != "FAIL" {
		t.Errorf("corrupt-store check = %+v, want FAIL", broken)
	}
}

// newDoctorGitRepo creates a committed repository (the doctor's
// recovery resolves one).
func newDoctorGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "test@test.com"}, {"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args[0], err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return dir
}
