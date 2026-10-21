package runner

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// resolvedWorkdir is the path a validator will hold: absolute, with symlinks
// resolved. On Windows that also expands the 8.3 short name t.TempDir returns
// (RUNNER~1), which NewPathValidator's EvalSymlinks otherwise leaves the
// expectation disagreeing with.
func resolvedWorkdir(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs %s: %v", p, err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// fakeSandboxWorkspace is a non-local tools.Workspace stand-in: the runner
// only needs Local()==false and interface identity to prove inheritance.
type fakeSandboxWorkspace struct{}

func (fakeSandboxWorkspace) ResolvePath(p string) (string, error) {
	return path.Join("/workspace", p), nil
}
func (fakeSandboxWorkspace) WorkDir() string            { return "/workspace" }
func (fakeSandboxWorkspace) Join(elem ...string) string { return path.Join(elem...) }
func (fakeSandboxWorkspace) Local() bool                { return false }
func (fakeSandboxWorkspace) Shell() (string, string)    { return "sh", "-c" }
func (fakeSandboxWorkspace) ReadFile(context.Context, string) ([]byte, error) {
	return nil, fs.ErrNotExist
}
func (fakeSandboxWorkspace) WriteFile(context.Context, string, []byte, fs.FileMode) error {
	return nil
}
func (fakeSandboxWorkspace) Stat(context.Context, string) (fs.FileInfo, error) {
	return nil, fs.ErrNotExist
}
func (fakeSandboxWorkspace) Lstat(context.Context, string) (fs.FileInfo, error) {
	return nil, fs.ErrNotExist
}
func (fakeSandboxWorkspace) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, fs.ErrNotExist
}
func (fakeSandboxWorkspace) Remove(context.Context, string) error                { return nil }
func (fakeSandboxWorkspace) MkdirAll(context.Context, string, fs.FileMode) error { return nil }
func (fakeSandboxWorkspace) Exec(context.Context, tools.ExecRequest) (tools.ExecResult, error) {
	return tools.ExecResult{}, nil
}

// TestBuildSubAgentRegistry_InheritsSessionSandbox pins decision D2: with
// the session workspace set, the sub-agent's registry runs on that
// workspace, host-only profile tools are skipped, and the workdir argument
// is ignored (the container root is the workspace, so no host validator is
// derived).
func TestBuildSubAgentRegistry_InheritsSessionSandbox(t *testing.T) {
	ws := fakeSandboxWorkspace{}
	opts := taskRunnerOpts{workspace: ws}
	profile := subagent.RoleProfile{
		Tools: []string{"read", "write", "go_refactor", "grep"},
	}

	reg, _, err := buildSubAgentRegistry(opts, profile, 0, "/host/some/worktree")
	if err != nil {
		t.Fatalf("buildSubAgentRegistry: %v", err)
	}

	if reg.Workspace != tools.Workspace(ws) {
		t.Errorf("sub-agent registry must run on the session's sandbox workspace (D2), got %#v", reg.Workspace)
	}
	if reg.PathValidator != nil {
		t.Error("isolated sub-agents must not inherit a host path validator")
	}
	for _, name := range []string{"read", "write", "grep"} {
		if reg.Get(name) == nil {
			t.Errorf("profile tool %s must be registered", name)
		}
	}
	if reg.Get("go_refactor") != nil {
		t.Error("go_refactor is host-only; an isolated sub-agent must not get it")
	}
}

// TestBuildSubAgentRegistry_LocalRouteUnchanged guards the non-isolated
// path: with a path validator and no workspace, the sub-agent gets the
// validator rooted at its workdir exactly as before — the inheritance
// change must not leak into local sessions.
func TestBuildSubAgentRegistry_LocalRouteUnchanged(t *testing.T) {
	// A real workdir: since the confinement default is "auto", the registry
	// build prepares the redirect inside it.
	root := t.TempDir()
	child := filepath.Join(root, "worktrees", "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	// A Go module: the Go toolchain tools are gated on the workspace
	// being one (yaah-9n0), so the fixture says it is.
	if err := os.WriteFile(filepath.Join(child, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	pv := tools.NewPathValidator(root, false, nil)
	opts := taskRunnerOpts{pathValidator: pv}
	profile := subagent.RoleProfile{Tools: []string{"read", "go_refactor"}}

	reg, _, err := buildSubAgentRegistry(opts, profile, 0, child)
	if err != nil {
		t.Fatalf("buildSubAgentRegistry: %v", err)
	}

	if reg.Workspace == nil || reg.Workspace.Local() != true {
		t.Errorf("local sub-agent must keep the validator-derived local workspace, got %#v", reg.Workspace)
	}
	want := resolvedWorkdir(t, child)
	if got := reg.PathValidator; got == nil || got.WorkDir != want {
		t.Errorf("local sub-agent's validator must be rooted at the workdir: got %#v, want %s", got, want)
	}
	if reg.Get("go_refactor") == nil {
		t.Error("go_refactor stays available to host-side sub-agents; only isolated mode omits it")
	}
}

// TestBuildSubAgentRegistry_WorkdirWithoutSessionValidator pins the
// #312/#318 root cause: in the default local mode the session carries no
// path validator, but a workdir dispatch must still root the sub-agent's
// file tools and shell at its assigned checkout. Without this the
// supervised session's fork variants all wrote the ONE main tree —
// worktrees stayed pristine, sealed deltas came out empty, and choose
// had nothing to apply.
func TestBuildSubAgentRegistry_WorkdirWithoutSessionValidator(t *testing.T) {
	opts := taskRunnerOpts{} // pathValidator == nil: default local mode
	profile := subagent.RoleProfile{Tools: []string{"read", "bash", "write"}}
	// A real worktree exists by the time a dispatch happens — and since the
	// confinement default is "auto", the registry build prepares the redirect in
	// it, so the path must be real.
	workdir := t.TempDir()

	reg, _, err := buildSubAgentRegistry(opts, profile, 0, workdir)
	if err != nil {
		t.Fatalf("buildSubAgentRegistry: %v", err)
	}

	if reg.Workspace == nil || !reg.Workspace.Local() {
		t.Errorf("workdir dispatch must derive a local workspace, got %#v", reg.Workspace)
	}
	want := resolvedWorkdir(t, workdir)
	if got := reg.PathValidator; got == nil || got.WorkDir != want || got.WorkspaceRoot != want {
		t.Errorf("validator must be rooted at the workdir: got %#v, want %s", got, want)
	}
}

// TestBuildSubAgentSysPrompt_WorkdirHeader pins that a local-mode workdir
// dispatch's environment header advertises the assigned worktree, not the
// orchestrator's process cwd (#312).
func TestBuildSubAgentSysPrompt_WorkdirHeader(t *testing.T) {
	got := buildSubAgentSysPrompt(taskRunnerOpts{}, "developer", subagent.RoleProfile{}, false, "/repo/shepherd-worktrees/var-a", nil)
	if !strings.Contains(got, "/repo/shepherd-worktrees/var-a") {
		t.Errorf("env header must advertise the workdir, got:\n%.400s", got)
	}
}

// TestBuildSubAgentRegistry_NestedTaskInherits proves the inheritance
// survives nesting: a sub-agent that can itself spawn sub-agents hands the
// same workspace to its children.
func TestBuildSubAgentRegistry_NestedTaskInherits(t *testing.T) {
	ws := fakeSandboxWorkspace{}
	opts := taskRunnerOpts{workspace: ws}
	profile := subagent.RoleProfile{Tools: []string{"read", "delegate"}}

	reg, _, err := buildSubAgentRegistry(opts, profile, 1, "")
	if err != nil {
		t.Fatalf("buildSubAgentRegistry: %v", err)
	}
	if reg.Get("delegate") == nil {
		t.Fatal("profile with delegate and remaining depth must register the task tool")
	}
}

// TestSubAgentSandbox_NilWhenIsolated pins the scope decision: an isolated
// sub-agent gets a pure-causal scope — a host git sandbox would checkpoint
// a tree the sub-agent never touches.
func TestSubAgentSandbox_NilWhenIsolated(t *testing.T) {
	opts := taskRunnerOpts{
		workspace: fakeSandboxWorkspace{},
		defaults:  config.Defaults{RetainedRepoPath: "/repo"},
	}
	if got := subAgentSandbox(opts, ""); got != nil {
		t.Errorf("isolated sub-agent scope sandbox must be nil, got %T", got)
	}
	if got := subAgentSandbox(opts, "/repo/worktrees/child"); got != nil {
		t.Errorf("workdir must not conjure a host sandbox in isolated mode, got %T", got)
	}
}

// TestSubAgentSandbox_LocalUnchanged keeps the local behaviour pinned:
// workdir selects a worktree sandbox, no workdir the shared repository.
func TestSubAgentSandbox_LocalUnchanged(t *testing.T) {
	opts := taskRunnerOpts{defaults: config.Defaults{RetainedRepoPath: "/repo"}}

	if got := subAgentSandbox(opts, "/repo/worktrees/child"); got == nil {
		t.Error("local sub-agent with a workdir must get a worktree sandbox")
	}
	if got := subAgentSandbox(opts, ""); got == nil {
		t.Error("local sub-agent without a workdir must get the shared-repo sandbox")
	}
}

// TestBuildSubAgentSysPrompt_IsolatedGuidance pins the prompt delta: an
// isolated sub-agent is told it runs in the container, is warned not to
// claim its changes are saved, and is told which of its profile's tools
// were dropped. A local sub-agent must not receive it.
func TestBuildSubAgentSysPrompt_IsolatedGuidance(t *testing.T) {
	opts := taskRunnerOpts{workspace: fakeSandboxWorkspace{}}
	// bash is in the profile so the role passes the shell gate (#329) —
	// this test pins that the header and the Available Shell block agree
	// on the container's shell, which needs a shell-carrying role.
	profile := subagent.RoleProfile{Tools: []string{"read", "bash", "go_refactor"}}

	got := buildSubAgentSysPrompt(opts, "developer", profile, false, "", nil)
	for _, want := range []string{
		"isolated container",
		"never claim they are saved",
		"`go_refactor`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("isolated sub-agent prompt must contain %q:\n%s", want, got)
		}
	}

	// The environment header must advertise the sandbox, not the host:
	// the workspace's workdir (the header's working-directory line is
	// what relative paths key off) and the container's shell. Assert on
	// the rendered lines, not substrings — "sh" alone matches "shell".
	if !strings.Contains(got, "Default shell: sh ") {
		t.Errorf("isolated sub-agent's environment header must advertise the container's sh:\n%s", got)
	}
	if !strings.Contains(got, "Working directory: /workspace") {
		t.Errorf("isolated sub-agent's environment header must name the sandbox workdir:\n%s", got)
	}
	if strings.Contains(got, "Use bash for ALL") {
		t.Errorf("the Available Shell block must agree with the header, not advertise the host's bash:\n%s", got)
	}

	local := buildSubAgentSysPrompt(taskRunnerOpts{}, "developer", profile, false, "", nil)
	if strings.Contains(local, "isolated container") {
		t.Error("a local sub-agent must not receive the isolated-workspace guidance")
	}
}

// TestSubAgentUnavailableTools pins the environment gate (yaah-9n0): a
// shell the machine does not have and Go toolchain machinery in a
// non-Go workspace are left out, and the prompt names them so the model
// does not call a tool that is not registered.
func TestSubAgentUnavailableTools(t *testing.T) {
	nonGo := t.TempDir()
	profile := subagent.RoleProfile{Tools: []string{"read", "bash", "powershell", "go_test", "go_outline"}}

	got := subAgentUnavailableTools(taskRunnerOpts{}, profile, nonGo)
	for _, want := range []string{"go_test", "go_outline"} {
		if !slices.Contains(got, want) {
			t.Errorf("Go machinery must be unavailable outside a Go module: %v", got)
		}
	}
	if !slices.Contains(got, "powershell") && runtime.GOOS != "windows" {
		if _, err := exec.LookPath("pwsh"); err != nil {
			t.Errorf("powershell must be unavailable without pwsh: %v", got)
		}
	}
	if slices.Contains(got, "read") || slices.Contains(got, "bash") {
		t.Errorf("ordinary tools must stay available: %v", got)
	}

	// A Go module keeps the Go machinery.
	goDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	got = subAgentUnavailableTools(taskRunnerOpts{}, profile, goDir)
	for _, name := range []string{"go_test", "go_outline"} {
		if slices.Contains(got, name) {
			t.Errorf("%s must be available in a Go module: %v", name, got)
		}
	}
}

// TestSubAgentPromptNamesUnavailableTools: the sub-agent is told which
// tools are gone, so it does not call one and narrate the failure.
func TestSubAgentPromptNamesUnavailableTools(t *testing.T) {
	nonGo := t.TempDir()
	profile := subagent.RoleProfile{Tools: []string{"read", "go_test"}}
	prompt := buildSubAgentSysPrompt(taskRunnerOpts{systemPrompt: "BASE"}, "developer", profile, false, nonGo, nil)
	if !strings.Contains(prompt, "## Unavailable tools") {
		t.Errorf("the prompt must name the unavailable tools:\n%s", prompt)
	}
	if !strings.Contains(prompt, "`go_test`") {
		t.Errorf("the prompt must name the dropped tool:\n%s", prompt)
	}
}
