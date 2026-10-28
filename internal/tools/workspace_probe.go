package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	croftadapter "github.com/buchenberg/goatherd-runtime/sandbox/croft"
	"github.com/buchenberg/yaah/internal/config"
)

// OpenContainerdWorkspaceSandbox resolves the container workspace through
// goatherd-runtime's sandbox registry rather than constructing a backend
// directly (the isolated-workspace plan §1.1).
//
// Going through the seam is the point: the container lane now gets the same
// registry, the same Sandbox contract, and the same optional capabilities
// (DeltaApplier, and Changes for effect extraction) as the git-worktree lane,
// instead of a private construction path that bypasses them. Importing
// croftadapter is what registers "containerd" — config can name a backend,
// only an import can provide one.
//
// Unlike the old direct croft.New call, this can fail, and the failures are
// worth having: an image left unset is refused here naming the field rather
// than surfacing as a daemon error mid-session, and a platform that cannot
// provide the backend says so. Config validation already refuses
// workspace.backend: containerd off-Linux, so the unknown-backend case is
// unreachable in a validly loaded config; it is still reported rather than
// papered over.
//
// The returned sandbox is NOT provisioned: Create remains the caller's
// explicit lifecycle step, per the Sandbox contract.
func OpenContainerdWorkspaceSandbox(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
	// FIFODir passes through so the client's task/exec stdio FIFOs land in a
	// directory yaah's uid can write — containerd's default is root-only.
	//
	// Network is deliberately left unset, which selects the adapter's isolated
	// default: the container gets its own unshared network namespace, so it is
	// loopback-only with no egress. That netns — not a paddock — is this lane's
	// egress boundary, which is why yaah's config surface does not expose it
	// yet. Sharing the host network would need an explicit config field and a
	// decision about how it interacts with the egress posture vocabulary.
	sb, err := goatherdsandbox.OpenSandbox(croftadapter.BackendName, containerdAdapterConfig(w))
	if err != nil {
		return nil, fmt.Errorf("workspace: open containerd sandbox: %w", err)
	}
	if sb == nil {
		return nil, fmt.Errorf("workspace: the registry returned no sandbox for config %+v", w.Containerd)
	}
	return sb, nil
}

// containerdAdapterConfig maps yaah's workspace config onto the adapter's
// Config.
//
// It is its own function so the mapping is testable on every platform. The
// adapter registers no backend off-Linux, so a test that resolves one through
// the registry can only run on Linux — but the Config type is platform-neutral,
// and a field silently dropped between yaah's config surface and the backend is
// exactly the regression worth catching everywhere. Testing the mapping
// directly also retires the reflection the old passthrough test needed to read
// an unexported struct field out of a resolved backend.
func containerdAdapterConfig(w config.WorkspaceConfig) croftadapter.Config {
	return croftadapter.Config{
		Address:     w.Containerd.Address,
		Namespace:   w.Containerd.Namespace,
		Image:       w.Containerd.Image,
		Snapshotter: w.Containerd.Snapshotter,
		FIFODir:     w.Containerd.FIFODir,
		// Root maps onto Workdir: the containment root every relative tool
		// path resolves against inside the sandbox.
		Workdir: w.Root,
	}
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
func ProbeImageContract(ctx context.Context, sb goatherdsandbox.Sandbox) (err error) {
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

	if cerr := sb.Create(ctx, goatherdsandbox.SandboxSpec{}); cerr != nil {
		return fmt.Errorf("workspace image probe: create: %w", cerr)
	}

	res, xerr := sb.Exec(ctx, goatherdsandbox.ExecRequest{
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
