package tools

import (
	"context"
	"fmt"
	"strings"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	containerdbackend "github.com/buchenberg/shepherd-kernel-go/sandbox/containerd"

	"github.com/buchenberg/yaah/internal/config"
)

// OpenContainerdWorkspaceSandbox resolves the configured containerd backend
// through the kernel's sandbox registry — the config-driven seam (the
// isolated-workspace plan §1.1/step 4). The blank-imported adapter package
// self-registers the "containerd" backend (kernel v0.1.5+), so no host-side
// factory is wired here, and adding one would panic by the registry's design.
//
// The returned sandbox is NOT provisioned: Create remains the caller's
// explicit lifecycle step, per the kernel's OpenSandbox contract.
func OpenContainerdWorkspaceSandbox(w config.WorkspaceConfig) (shepherd.Sandbox, error) {
	// Root maps onto the adapter's Workdir: the containment root every
	// relative tool path resolves against inside the sandbox.
	return shepherd.OpenSandbox(config.BackendContainerd, containerdbackend.Config{
		Address:     w.Containerd.Address,
		Namespace:   w.Containerd.Namespace,
		Image:       w.Containerd.Image,
		Snapshotter: w.Containerd.Snapshotter,
		Workdir:     w.Root,
	})
}

// imageContractScript verifies the D5 image contract in one exec:
//
//   - the binaries the in-band workspace surface shells out to
//     (sh, stat, find, git, rg, and a Go toolchain),
//   - the GNU userland specifically: Stat uses `stat -c` and ReadDir uses
//     `find -printf` (workspace_sandbox.go), which BusyBox equivalents
//     reject — a present-but-BusyBox binary would pass `command -v` and
//     still break the workspace mid-task.
//
// Output convention: on failure, one line "missing: <space-separated>" with
// a ":-flag" annotation for the GNU-specific checks, and exit 1.
const imageContractScript = `missing=""
for t in sh stat find git rg go; do
  command -v "$t" >/dev/null 2>&1 || missing="$missing $t"
done
stat -c %a . >/dev/null 2>&1 || missing="$missing stat:-c"
find . -maxdepth 0 -printf . >/dev/null 2>&1 || missing="$missing find:-printf"
if [ -n "$missing" ]; then
  printf 'missing:%s\n' "$missing"
  exit 1
fi`

// ProbeImageContract boots a throwaway sandbox and verifies it satisfies the
// D5 image contract (plan §10, D5): probe at startup, so a wrong image fails
// the session before it consumes a task rather than at the first tool call
// mid-run.
//
// The probe owns the sandbox completely — Create, one exec, Destroy on every
// path including failure — because a probe that leaks a container on its own
// error paths would defeat the leak-freedom the lifecycle work is held to.
// The sandbox parameter is the seam: wiring passes the session backend; tests
// pass the in-memory fake.
func ProbeImageContract(ctx context.Context, sb shepherd.Sandbox) error {
	if err := sb.Create(ctx, shepherd.SandboxSpec{}); err != nil {
		return fmt.Errorf("workspace image probe: create: %w", err)
	}
	// Destroy must run even when the probe fails and even when the caller's
	// context expired mid-probe: an unprobed-but-running container is the
	// leak this function must never cause.
	defer sb.Destroy(context.WithoutCancel(ctx))

	res, err := sb.Exec(ctx, shepherd.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", imageContractScript},
	})
	if err != nil {
		return fmt.Errorf("workspace image probe: exec: %w", err)
	}
	if res.ExitCode == 0 {
		return nil
	}

	missing, ok := strings.CutPrefix(strings.TrimSpace(res.Stdout), "missing:")
	if !ok {
		// A nonzero exit without the convention line is an operational
		// failure, not a contract result; report both channels.
		return fmt.Errorf("workspace image probe: exit %d: %s", res.ExitCode,
			firstNonEmpty(strings.TrimSpace(res.Stderr), strings.TrimSpace(res.Stdout), "no output"))
	}
	items := strings.Fields(missing)
	if len(items) == 0 {
		return fmt.Errorf("workspace image probe: exit %d with no missing list: %s", res.ExitCode,
			firstNonEmpty(strings.TrimSpace(res.Stderr), "no output"))
	}
	return fmt.Errorf("workspace image does not satisfy the required tool contract (D5): missing %s — the image needs sh, GNU stat (stat -c), GNU find (find -printf), git, rg, and a Go toolchain",
		strings.Join(items, ", "))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
