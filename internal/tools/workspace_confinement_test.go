package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
)

// confinedTestWorkspace builds a confined workspace rooted at root.
func confinedTestWorkspace(root string, backend containment.Backend, place confinement.Placement) *localWorkspace {
	pv := NewPathValidator(root, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return NewConfinedLocalWorkspace(pv, backend, place,
		containment.NetworkPolicy{Mode: containment.NetAllowAll})
}

// The orchestrator's workspace carries no confiner, so its shell writes on the
// host, unconfined — the Terminal-Bench contract (Plan 7 section 2). This is the
// guardrail: the split must never confine the orchestrator.
func TestOrchestrator_ExecUnconfined(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell redirection")
	}
	root := t.TempDir()
	outside := t.TempDir()
	pv := NewPathValidator(root, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	ws := newLocalWorkspace(pv)

	if ws.Confined() {
		t.Fatal("the orchestrator's workspace must not carry a confiner")
	}
	target := filepath.Join(outside, "escaped.txt")
	res, _ := ws.Exec(context.Background(), ExecRequest{
		Command: "sh", Args: []string{"-c", "echo hi > " + target},
	})
	if res.ExitCode != 0 {
		t.Fatalf("host exec failed: %+v", res)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the orchestrator must be able to write outside its workspace: %v", err)
	}
}

// A confined workspace writes inside its root and is refused outside — the
// kernel boundary the orchestrator's workspace does not have. Linux only (no
// Landlock elsewhere); the seam is skipped, not failed, on other hosts.
func TestConfinedWorkspace_WriteOutsideDenied(t *testing.T) {
	backend := landlock.Detect()
	if backend == nil {
		t.Skip("no Landlock on this host")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell redirection")
	}
	root := t.TempDir()
	outside := t.TempDir()
	ws := confinedTestWorkspace(root, backend, confinement.PlacementPaddock)

	inside := filepath.Join(root, "in.txt")
	res, err := ws.Exec(context.Background(), ExecRequest{
		Command: "sh", Args: []string{"-c", "echo ok > " + inside},
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("a write inside the root should succeed: %+v err=%v", res, err)
	}
	if _, statErr := os.Stat(inside); statErr != nil {
		t.Fatalf("the in-root write should land: %v", statErr)
	}

	escaped := filepath.Join(outside, "escaped.txt")
	res, err = ws.Exec(context.Background(), ExecRequest{
		Command: "sh", Args: []string{"-c", "echo no > " + escaped},
	})
	if err == nil && res.ExitCode == 0 {
		t.Fatalf("a write outside the writable root must be refused: %+v", res)
	}
	if _, statErr := os.Stat(escaped); !os.IsNotExist(statErr) {
		t.Fatalf("the out-of-root file must not exist (stat err = %v)", statErr)
	}
}

// PlacementAuto with no backend degrades visibly to advisory: the command still
// runs on the host, so a non-Linux host behaves exactly as before.
func TestConfinedWorkspace_AdvisoryWhenNoBackend(t *testing.T) {
	root := t.TempDir()
	ws := confinedTestWorkspace(root, nil, confinement.PlacementAuto)
	shell, flag := ws.Shell()
	res, err := ws.Exec(context.Background(), ExecRequest{Command: shell, Args: []string{flag, "echo advisory"}})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "advisory") {
		t.Fatalf("auto with no backend should run advisory: %+v err=%v", res, err)
	}
}

// PlacementPaddock with no backend fails closed: it refuses rather than running
// unconfined.
func TestConfinedWorkspace_PaddockFailsClosed(t *testing.T) {
	root := t.TempDir()
	ws := confinedTestWorkspace(root, nil, confinement.PlacementPaddock)
	_, err := ws.Exec(context.Background(), ExecRequest{Command: "true"})
	if err == nil {
		t.Fatal("a paddock placement with no backend must refuse, not run unconfined")
	}
}
