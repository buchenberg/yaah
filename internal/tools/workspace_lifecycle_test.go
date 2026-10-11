package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// cleanOpener hands out fresh fakes in call order — probe sandbox first,
// session sandbox second — and reaches them through the returned slice. The
// probe sandbox gets a passing ExecHook (the fake's shell does not implement
// the contract script's loop surface, and the probe is not under test here);
// the session sandbox keeps the working interpreter so sandboxWorkspace
// operations round-trip real writes.
func cleanOpener() (SandboxOpener, func() []*sandboxfake.Sandbox) {
	var opened []*sandboxfake.Sandbox
	open := func(config.WorkspaceConfig) (shepherdsandbox.Sandbox, error) {
		sb := sandboxfake.New()
		if len(opened) == 0 {
			sb.ExecHook = func(shepherdsandbox.ExecRequest) (shepherdsandbox.ExecResult, error) {
				return shepherdsandbox.ExecResult{ExitCode: 0}, nil
			}
		}
		opened = append(opened, sb)
		return sb, nil
	}
	return open, func() []*sandboxfake.Sandbox { return opened }
}

// sessionTestConfig is the minimal normalized containerd stanza the
// lifecycle accepts; config validation is upstream of StartSessionSandbox
// and is covered by the config package's own tests.
func sessionTestConfig() config.WorkspaceConfig {
	return config.WorkspaceConfig{
		Backend: config.BackendContainerd,
		Root:    "/workspace",
		Containerd: config.ContainerdWorkspaceConfig{
			Namespace: "default",
			Image:     "registry.example/yaah-workspace:latest",
		},
	}
}

func TestStartSessionSandbox_SessionLifecycle(t *testing.T) {
	open, fakes := cleanOpener()

	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	opened := fakes()
	if len(opened) != 2 {
		t.Fatalf("opener called %d times, want 2 (probe sandbox, then session sandbox)", len(opened))
	}
	probeSB, sessionSB := opened[0], opened[1]
	if !probeSB.Destroyed() {
		t.Error("the probe sandbox must be destroyed; the probe owns its full lifecycle")
	}
	if !sessionSB.Created() {
		t.Error("the session sandbox must be created before the first tool dispatch")
	}
	if sessionSB.Destroyed() {
		t.Error("the session sandbox must not be destroyed while the session is live")
	}

	// The workspace is bound to the session sandbox: a write through it
	// lands in the sandbox the lifecycle created. Paths resolve through
	// the workspace first, exactly as the migrated tools do (edit.go).
	ws := ss.Workspace()
	if ws.Local() {
		t.Error("the session workspace must report as non-local")
	}
	dir, err := ws.ResolvePath("proj")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if err := ws.MkdirAll(context.Background(), dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	notes, err := ws.ResolvePath("proj/notes.txt")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if err := ws.WriteFile(context.Background(), notes, []byte("isolated"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ws.ReadFile(context.Background(), notes)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "isolated" {
		t.Errorf("read back %q, want %q — the workspace must round-trip through the session sandbox", got, "isolated")
	}

	// Teardown: Destroy is once-only. A second call (failed startup guard
	// racing session close) must not reach the backend again, so the
	// fake's double-destroy error can never surface through teardown.
	if err := ss.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !sessionSB.Destroyed() {
		t.Error("Destroy must destroy the session sandbox")
	}
	if err := ss.Destroy(context.Background()); err != nil {
		t.Errorf("second Destroy = %v, want nil (idempotent teardown)", err)
	}
}

func TestStartSessionSandbox_ProbeFailureLeavesNoSessionSandbox(t *testing.T) {
	// The opener fails the D5 contract on the probe sandbox: the session
	// sandbox must never be opened.
	opened := 0
	open := func(config.WorkspaceConfig) (shepherdsandbox.Sandbox, error) {
		opened++
		sb := sandboxfake.New()
		sb.ExecHook = func(shepherdsandbox.ExecRequest) (shepherdsandbox.ExecResult, error) {
			return shepherdsandbox.ExecResult{ExitCode: 1, Stdout: "missing: rg\n"}, nil
		}
		return sb, nil
	}

	_, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, "", nil)
	if err == nil {
		t.Fatal("a failed image contract must fail the session")
	}
	if opened != 1 {
		t.Errorf("opener called %d times, want 1 (the session sandbox must not be opened when the probe fails)", opened)
	}
}

func TestStartSessionSandbox_CreateFailureDestroys(t *testing.T) {
	// The session sandbox's Create fails (here: the fake was already
	// created before the lifecycle got it, so Create refuses). The
	// lifecycle must destroy rather than leak — the sandbox is its own
	// from the moment the opener returns.
	preCreated := sandboxfake.New()
	if err := preCreated.Create(context.Background(), shepherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	calls := 0
	open := func(config.WorkspaceConfig) (shepherdsandbox.Sandbox, error) {
		calls++
		if calls == 1 {
			sb := sandboxfake.New()
			sb.ExecHook = func(shepherdsandbox.ExecRequest) (shepherdsandbox.ExecResult, error) {
				return shepherdsandbox.ExecResult{ExitCode: 0}, nil
			}
			return sb, nil
		}
		return preCreated, nil
	}

	_, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, "", nil)
	if err == nil {
		t.Fatal("a failed Create must fail the session")
	}
	if !errors.Is(err, sandboxfake.ErrAlreadyCreated) {
		t.Errorf("error = %v, want it to carry the create failure", err)
	}
	if !preCreated.Destroyed() {
		t.Error("the lifecycle must destroy the sandbox whose Create failed; a partially-provisioned container is still a container")
	}
}

func TestStartSessionSandbox_OpenerFailure(t *testing.T) {
	for _, tt := range []struct {
		name     string
		failCall int // 1 = probe sandbox, 2 = session sandbox
	}{
		{"probe sandbox open fails", 1},
		{"session sandbox open fails", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			open := func(config.WorkspaceConfig) (shepherdsandbox.Sandbox, error) {
				calls++
				if calls == tt.failCall {
					return nil, errors.New("daemon unreachable")
				}
				sb := sandboxfake.New()
				sb.ExecHook = func(shepherdsandbox.ExecRequest) (shepherdsandbox.ExecResult, error) {
					return shepherdsandbox.ExecResult{ExitCode: 0}, nil
				}
				return sb, nil
			}
			if _, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, "", nil); err == nil {
				t.Fatal("an opener failure must fail the session")
			} else if !strings.Contains(err.Error(), "daemon unreachable") {
				t.Errorf("error = %v, want it to name the opener failure", err)
			}
		})
	}
}

func TestSessionSandbox_BackendAndRoot(t *testing.T) {
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, "", nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	defer ss.Destroy(context.Background())

	if got := ss.Backend(); got != "fake" {
		t.Errorf("Backend() = %q, want the sandbox backend's own name", got)
	}
	if got := ss.Workspace().WorkDir(); got != "/workspace" {
		t.Errorf("WorkDir() = %q, want the configured root", got)
	}
}
