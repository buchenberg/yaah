package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	containerdbackend "github.com/buchenberg/shepherd-kernel-go/sandbox/containerd"

	"github.com/buchenberg/yaah/internal/config"
)

// OpenContainerdWorkspaceSandbox resolves the configured containerd backend
// through the kernel's sandbox registry — the config-driven seam (the
// isolated-workspace plan §1.1/step 4). The adapter package this file names
// for its Config type self-registers the "containerd" backend on import
// (kernel v0.1.5+), so no host-side factory is wired here, and adding one
// would panic by the registry's design.
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
//     (sh, stat, find, git, rg, base64, and a Go toolchain),
//   - the flags the surface actually invokes: Stat uses `stat -c` and
//     ReadDir uses `find -printf` (workspace_sandbox.go), and WriteFile
//     decodes its base64 payload with `base64 -d` — a dependency the
//     kernel's Sandbox.WriteFile already had, now stated in the contract
//     instead of discovered at the first write. The find check is the
//     genuine GNU-vs-BusyBox discriminator — BusyBox find has no
//     -printf. BusyBox stat, by contrast, accepts `-c` (and `%a`), so the
//     stat check proves the flag exists, not that the userland is GNU.
//
// Output convention: on failure, one line "missing: <space-separated>" with
// a ":-flag" annotation for the flag checks, and exit 1.
const imageContractScript = `missing=""
for t in sh stat find git rg go base64; do
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
// path including a failed Create — because a probe that leaks a container on
// its own error paths would defeat the leak-freedom the lifecycle work is
// held to. A real backend's Create can fail after partially provisioning
// resources, so the cleanup attempt is mandatory even though the kernel's
// fakes report "nothing was created" for an unstarted sandbox. The sandbox
// parameter is the seam: wiring passes the session backend; tests pass the
// in-memory fake.
func ProbeImageContract(ctx context.Context, sb shepherd.Sandbox) (err error) {
	// Destroy must run even when the probe fails, even when the caller's
	// context expired mid-probe, and even when Create itself failed: an
	// unprobed-but-running container is the leak this function must never
	// cause. Its error joins the probe result, because a teardown failure
	// means the container may still be running — a "successful" probe
	// that leaked is not a success.
	defer func() {
		if derr := sb.Destroy(context.WithoutCancel(ctx)); derr != nil {
			err = errors.Join(err, fmt.Errorf("workspace image probe: destroy: %w", derr))
		}
	}()

	if cerr := sb.Create(ctx, shepherd.SandboxSpec{}); cerr != nil {
		return fmt.Errorf("workspace image probe: create: %w", cerr)
	}

	res, xerr := sb.Exec(ctx, shepherd.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", imageContractScript},
	})
	if xerr != nil {
		return fmt.Errorf("workspace image probe: exec: %w", xerr)
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
	return fmt.Errorf("workspace image does not satisfy the required tool contract (D5): missing %s — the image needs sh, stat (stat -c), GNU find (find -printf), git, rg, base64, and a Go toolchain",
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
