package tools

// workspace_activation_live_test.go is the §2.4 exit criterion made
// runnable: an isolated session runs a real tool-driven task end to end
// — probe, create, D1 bootstrap, tool dispatch through the session
// workspace, diff, the accept seam, apply-out onto the host — against a
// real containerd daemon and a real image.
//
// It is inert by default, exactly like the kernel's own live suite: the
// daemon address is an env knob, and without it there is nothing to talk
// to. The image is also required, not defaulted: the D5 contract needs a
// GNU userland with git, rg, base64, and a Go toolchain, and no stock image
// guarantees that (alpine/git, for instance, has BusyBox find without
// -printf). Build or pick an image, then:
//
//	sudo env YAAH_LIVE_CONTAINERD_ADDR=/run/containerd/containerd.sock \
//	     YAAH_LIVE_CONTAINERD_IMAGE=docker.io/library/yaah-workspace:latest \
//	     go test ./internal/tools/ -run TestLive_ActivatedSession -count=1 \
//	     -timeout 30m -v
//
// `sudo env VAR=...`, not `sudo -E`: sudoers policies commonly refuse to
// preserve the environment, the knobs vanish, and the harness skips
// silently — run with -v to see the skip reason. The daemon socket needs
// root, so the test skips as the invoking user unless euid 0. The full
// runbook, including building a D5-satisfying image, is
// docs/isolated-workspace-live-testing.md.

import (
	"context"
	croft "github.com/buchenberg/croft"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/config"
)

const (
	liveAddrEnv        = "YAAH_LIVE_CONTAINERD_ADDR"
	liveImageEnv       = "YAAH_LIVE_CONTAINERD_IMAGE"
	liveNamespaceEnv   = "YAAH_LIVE_CONTAINERD_NAMESPACE"
	liveSnapshotterEnv = "YAAH_LIVE_CONTAINERD_SNAPSHOTTER"
	liveRootEnv        = "YAAH_LIVE_CONTAINERD_ROOT"
	liveTimeoutEnv     = "YAAH_LIVE_CONTAINERD_TIMEOUT"
)

// liveActivationEnv resolves one knob with its fallback.
func liveActivationEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// liveActivationConfig resolves the env or skips the test. Both the
// address and the image are required: without an address there is no
// daemon to talk to, and without an image there is nothing honest to
// probe (a defaulted image that fails the D5 contract would test the
// probe's refusal, not the activation). The resolved timeout comes
// back with the config so the caller cannot re-parse it differently —
// a malformed or non-positive YAAH_LIVE_CONTAINERD_TIMEOUT falls back
// to 10m exactly once, here.
func liveActivationConfig(t *testing.T) (config.WorkspaceConfig, time.Duration) {
	t.Helper()

	addr := strings.TrimSpace(os.Getenv(liveAddrEnv))
	image := strings.TrimSpace(os.Getenv(liveImageEnv))
	if addr == "" || image == "" {
		t.Skipf("set %s and %s to run the live activation verification (plan §2.4)", liveAddrEnv, liveImageEnv)
	}
	if os.Geteuid() != 0 {
		t.Skip("live activation tests need root for the containerd daemon socket (sudo env VAR=... preserves the knobs)")
	}

	timeout, err := time.ParseDuration(liveActivationEnv(liveTimeoutEnv, "10m"))
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Minute
	}

	return config.WorkspaceConfig{
		Backend: config.BackendContainerd,
		Root:    liveActivationEnv(liveRootEnv, "/workspace"),
		Containerd: config.ContainerdWorkspaceConfig{
			Address:     addr,
			Namespace:   liveActivationEnv(liveNamespaceEnv, "yaah-live"),
			Image:       image,
			Snapshotter: liveActivationEnv(liveSnapshotterEnv, ""),
		},
	}, timeout
}

// TestLive_ActivatedSession is the plan's step-8 exit criterion: the
// whole activation story against the real backend — the D5 probe, the
// Create, the D1 bootstrap from a host git work tree, a tool-driven task
// dispatched through the session's own registry (so the workspace
// injection, not just the substrate, is what runs the tools), a diff
// against the session baseline, the containerd-only delta-patch property
// (D1's load-bearing piece), and the accept seam landing the work back
// onto the host.
func TestLive_ActivatedSession(t *testing.T) {
	wcfg, timeout := liveActivationConfig(t)

	src := gitHostRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// The real construction path the wiring uses: registry-resolved
	// backend, probe on a throwaway sandbox, Create, bootstrap (copy-in,
	// capture, rebind) — all inside one bound.
	ss, err := StartSessionSandbox(ctx, wcfg, OpenContainerdWorkspaceSandbox, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox (probe + create + bootstrap): %v", err)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		if derr := ss.Destroy(dctx); derr != nil {
			t.Errorf("Destroy: %v", derr)
		}
	}()
	if got := ss.Backend(); got != config.BackendContainerd {
		t.Errorf("Backend() = %q, want %q", got, config.BackendContainerd)
	}

	// The step-7 gate against the real workspace: the curated registry
	// must accept it, because both diagnostic lists are empty.
	reg := NewIsolatedRegistry()
	if err := reg.SetWorkspace(ss.Workspace()); err != nil {
		t.Fatalf("SetWorkspace on the curated registry: %v", err)
	}
	// The accept seam joins the registry the way the wiring registers
	// it, so the task below drives every operation — read, command,
	// write, delete, apply-out — through the registry a real session
	// dispatches through.
	reg.Register(&ApplyOutTool{Sandbox: ss, HostDir: src})

	// The tool-driven task, dispatched through the registry: this is
	// what makes the test an activation test rather than a substrate
	// test. If workspace injection ever breaks, these tools run on the
	// host — and the host-untouched assertions below catch it, because
	// the host is the one place the changes must not be yet.
	readOut, err := reg.Get("read").Execute(ctx, `{"filePath": "readme.txt"}`)
	if err != nil {
		t.Fatalf("read through the registry: %v", err)
	}
	if !strings.Contains(readOut, "hello") {
		t.Fatalf("the copy-in must land the source tree; read returned %q", readOut)
	}
	// rg comes from the image (the D5 contract), and the command runs
	// against the containment root, not the host's working directory.
	// rg prints no filename for a single-file search, so the assertion
	// is the matched content plus pwd; the host-untouched assertions
	// below are the strong container-vs-host discriminator.
	bashOut, err := reg.Get("bash").Execute(ctx, `{"command": "pwd && rg -n hello readme.txt"}`)
	if err != nil {
		t.Fatalf("bash through the registry: %v", err)
	}
	if !strings.Contains(bashOut, wcfg.Root) || !strings.Contains(bashOut, "hello") {
		t.Fatalf("the command must run inside the container (pwd %q, rg output %q)", wcfg.Root, bashOut)
	}
	// The containment root is a knob; resolve against it rather than
	// hardcoding /workspace.
	readmePath, err := ss.Workspace().ResolvePath("readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := ss.Workspace().Stat(ctx, readmePath); serr != nil {
		t.Fatalf("Stat inside the container (GNU stat from the image): %v", serr)
	}

	// The session's work through the same registry: modify, add, delete.
	if _, err := reg.Get("write").Execute(ctx, `{"filePath": "readme.txt", "content": "changed in the container\n"}`); err != nil {
		t.Fatalf("write through the registry: %v", err)
	}
	if _, err := reg.Get("write").Execute(ctx, `{"filePath": "result.txt", "content": "new from the container\n"}`); err != nil {
		t.Fatalf("write (new file) through the registry: %v", err)
	}
	if _, err := reg.Get("delete").Execute(ctx, `{"filePath": "pkg/run.sh"}`); err != nil {
		t.Fatalf("delete through the registry: %v", err)
	}

	// The isolation discriminator: the tools above ran inside the
	// container, so the host tree must be exactly as the bootstrap left
	// it. A regression that leaves tool dispatch on the host fails here
	// — before the apply-out makes the two sides indistinguishable.
	if got, rerr := os.ReadFile(filepath.Join(src, "readme.txt")); rerr != nil || string(got) != "hello\n" {
		t.Fatalf("host readme.txt = %q, %v — in-container writes leaked onto the host", got, rerr)
	}
	if _, serr := os.Lstat(filepath.Join(src, "result.txt")); !os.IsNotExist(serr) {
		t.Fatalf("host result.txt exists (%v) — in-container writes leaked onto the host", serr)
	}
	if _, serr := os.Lstat(filepath.Join(src, "pkg", "run.sh")); serr != nil {
		t.Fatalf("host pkg/run.sh missing before apply-out (%v) — the in-container delete leaked", serr)
	}

	diff, files, err := ss.DiffSinceBaseline(ctx, 0)
	if err != nil {
		t.Fatalf("DiffSinceBaseline: %v", err)
	}
	if len(files) != 3 || diff == "" {
		t.Fatalf("the delta must name the modify/add/delete: %d files, diff %q", len(files), diff)
	}

	// The containerd-only property the bootstrap's closing Apply exists
	// for: a capture after mutation carries the outward binary patch.
	cur, err := ss.sb.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, ok := cur.Data[croft.StateKeyDeltaPatch].(string); !ok {
		t.Fatalf("the capture carries no %s — the D1 delta anchor is not in effect against the real backend", croft.StateKeyDeltaPatch)
	}

	// The accept seam, dispatched through the registry exactly as a
	// session would: preview, approve, apply onto the host.
	if out, aerr := reg.Get("apply_out").Execute(ctx, `{"dry_run": true}`); aerr != nil {
		t.Fatalf("apply_out dry run: %v", aerr)
	} else if !strings.Contains(out, "Dry run") {
		t.Errorf("dry-run output: %q", out)
	}
	if _, aerr := reg.Get("apply_out").Execute(ctx, `{}`); aerr != nil {
		t.Fatalf("apply_out: %v", aerr)
	}
	if got, rerr := os.ReadFile(filepath.Join(src, "readme.txt")); rerr != nil || string(got) != "changed in the container\n" {
		t.Errorf("host readme.txt = %q, %v — the accepted change did not land", got, rerr)
	}
	if got, rerr := os.ReadFile(filepath.Join(src, "result.txt")); rerr != nil || string(got) != "new from the container\n" {
		t.Errorf("host result.txt = %q, %v — the new file did not land", got, rerr)
	}
	if _, serr := os.Lstat(filepath.Join(src, "pkg", "run.sh")); !os.IsNotExist(serr) {
		t.Errorf("host pkg/run.sh still exists (%v) — the deletion did not land", serr)
	}
}
