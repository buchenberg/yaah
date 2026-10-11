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
// GNU userland with git, rg, and a Go toolchain, and no stock image
// guarantees that (alpine/git, for instance, has BusyBox find without
// -printf). Build or pick an image, then:
//
//	sudo -E go test ./internal/tools/ -run TestLive_ActivatedSession -count=1 \
//	  -timeout 30m
//
// with (at minimum):
//
//	YAAH_LIVE_CONTAINERD_ADDR=/run/containerd/containerd.sock
//	YAAH_LIVE_CONTAINERD_IMAGE=registry.example/yaah-workspace:latest
//
// Optional knobs: NAMESPACE (default yaah-live), SNAPSHOTTER (the
// adapter's default), ROOT (default /workspace), TIMEOUT (default 10m —
// a cold image pull rides the same bound; pass a matching go test
// -timeout). The daemon socket needs root, so the test skips as the
// invoking user unless euid 0 — sudo -E preserves the env.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	containerdbackend "github.com/buchenberg/shepherd-kernel-go/sandbox/containerd"

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
// probe's refusal, not the activation).
func liveActivationConfig(t *testing.T) config.WorkspaceConfig {
	t.Helper()

	addr := strings.TrimSpace(os.Getenv(liveAddrEnv))
	image := strings.TrimSpace(os.Getenv(liveImageEnv))
	if addr == "" || image == "" {
		t.Skipf("set %s and %s to run the live activation verification (plan §2.4)", liveAddrEnv, liveImageEnv)
	}
	if os.Geteuid() != 0 {
		t.Skip("live activation tests need root for the containerd daemon socket (sudo -E preserves the env)")
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
	}
}

// TestLive_ActivatedSession is the plan's step-8 exit criterion: the
// whole activation story against the real backend — the D5 probe, the
// Create, the D1 bootstrap from a host git work tree, tool-shaped work
// through the session workspace, a diff against the session baseline,
// the containerd-only delta-patch property (D1's load-bearing piece),
// and the accept seam landing the work back onto the host.
func TestLive_ActivatedSession(t *testing.T) {
	wcfg := liveActivationConfig(t)
	timeout, _ := time.ParseDuration(liveActivationEnv(liveTimeoutEnv, "10m"))

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

	// Tool-shaped work through the session workspace: the copy-in
	// landed, reads and writes round-trip, GNU stat and rg are the
	// image's, not the host's.
	ws := ss.Workspace()
	readme, err := ws.ResolvePath("readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got, rerr := ws.ReadFile(ctx, readme); rerr != nil || string(got) != "hello\n" {
		t.Fatalf("the copy-in must land the source tree: readme.txt = %q, %v", got, rerr)
	}
	if _, err := ws.Stat(ctx, readme); err != nil {
		t.Fatalf("Stat inside the container: %v", err)
	}
	if res, xerr := ws.Exec(ctx, ExecRequest{Command: "sh", Args: []string{"-c", "rg -n hello readme.txt"}}); xerr != nil || res.ExitCode != 0 {
		t.Fatalf("rg inside the container (D5 contract): exit %d, %v", res.ExitCode, xerr)
	}

	// The session's work: modify, add, delete — then diff against the
	// session baseline.
	if err := ws.WriteFile(ctx, readme, []byte("changed in the container\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, _ := ws.ResolvePath("result.txt")
	if err := ws.WriteFile(ctx, result, []byte("new from the container\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script, _ := ws.ResolvePath("pkg/run.sh")
	if err := ws.Remove(ctx, script); err != nil {
		t.Fatal(err)
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
	if _, ok := cur.Data[containerdbackend.StateKeyDeltaPatch].(string); !ok {
		t.Fatalf("the capture carries no %s — the D1 delta anchor is not in effect against the real backend", containerdbackend.StateKeyDeltaPatch)
	}

	// The accept seam, exactly as the model drives it: preview, approve,
	// apply onto the host.
	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}
	if out, aerr := tool.Execute(ctx, `{"dry_run": true}`); aerr != nil {
		t.Fatalf("apply_out dry run: %v", aerr)
	} else if !strings.Contains(out, "Dry run") {
		t.Errorf("dry-run output: %q", out)
	}
	if _, aerr := tool.Execute(ctx, `{}`); aerr != nil {
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
