package runner

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

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

	reg, err := buildSubAgentRegistry(opts, profile, 0, "/host/some/worktree")
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
	pv := tools.NewPathValidator("/repo", false, nil)
	opts := taskRunnerOpts{pathValidator: pv}
	profile := subagent.RoleProfile{Tools: []string{"read", "go_refactor"}}

	reg, err := buildSubAgentRegistry(opts, profile, 0, "/repo/worktrees/child")
	if err != nil {
		t.Fatalf("buildSubAgentRegistry: %v", err)
	}

	if reg.Workspace == nil || reg.Workspace.Local() != true {
		t.Errorf("local sub-agent must keep the validator-derived local workspace, got %#v", reg.Workspace)
	}
	// NewPathValidator makes the root absolute, so the expectation goes
	// through the same normalization (on Windows CI the temp cwd sits on
	// another drive, turning "/repo/..." into "D:\repo\...").
	want, _ := filepath.Abs("/repo/worktrees/child")
	if got := reg.PathValidator; got == nil || got.WorkDir != want {
		t.Errorf("local sub-agent's validator must be rooted at the workdir: got %#v, want %s", got, want)
	}
	if reg.Get("go_refactor") == nil {
		t.Error("go_refactor stays available to host-side sub-agents; only isolated mode omits it")
	}
}

// TestBuildSubAgentRegistry_NestedTaskInherits proves the inheritance
// survives nesting: a sub-agent that can itself spawn sub-agents hands the
// same workspace to its children.
func TestBuildSubAgentRegistry_NestedTaskInherits(t *testing.T) {
	ws := fakeSandboxWorkspace{}
	opts := taskRunnerOpts{workspace: ws}
	profile := subagent.RoleProfile{Tools: []string{"read", "spawn_subagent"}}

	reg, err := buildSubAgentRegistry(opts, profile, 1, "")
	if err != nil {
		t.Fatalf("buildSubAgentRegistry: %v", err)
	}
	if reg.Get("spawn_subagent") == nil {
		t.Fatal("profile with spawn_subagent and remaining depth must register the task tool")
	}
}

// TestSubAgentSandbox_NilWhenIsolated pins the scope decision: an isolated
// sub-agent gets a pure-causal scope — a host git sandbox would checkpoint
// a tree the sub-agent never touches.
func TestSubAgentSandbox_NilWhenIsolated(t *testing.T) {
	opts := taskRunnerOpts{
		workspace: fakeSandboxWorkspace{},
		defaults:  config.Defaults{SupervisedRepoPath: "/repo"},
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
	opts := taskRunnerOpts{defaults: config.Defaults{SupervisedRepoPath: "/repo"}}

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
	profile := subagent.RoleProfile{Tools: []string{"read", "go_refactor"}}

	got := buildSubAgentSysPrompt(opts, "developer", profile, false)
	for _, want := range []string{
		"isolated container",
		"never claim they are saved",
		"`go_refactor`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("isolated sub-agent prompt must contain %q:\n%s", want, got)
		}
	}

	local := buildSubAgentSysPrompt(taskRunnerOpts{}, "developer", profile, false)
	if strings.Contains(local, "isolated container") {
		t.Error("a local sub-agent must not receive the isolated-workspace guidance")
	}
}
