package tools

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	croftadapter "github.com/buchenberg/goatherd-runtime/sandbox/croft"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// probeResult is the canned outcome the fake's ExecHook returns for the
// contract script: the exit code, stdout, and stderr the probe must classify.
func probeResult(exit int, stdout string) func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
	return func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
		return goatherdsandbox.ExecResult{ExitCode: exit, Stdout: stdout, Stderr: "stderr detail"}, nil
	}
}

func TestProbeImageContract_Satisfied(t *testing.T) {
	sb := sandboxfake.New()
	sb.ExecHook = probeResult(0, "")

	if err := ProbeImageContract(context.Background(), sb); err != nil {
		t.Fatalf("ProbeImageContract: %v", err)
	}
	if !sb.Destroyed() {
		t.Error("the probe must destroy the sandbox it created; a probe that leaks on the happy path leaks always")
	}
	if execs := sb.Execs(); len(execs) != 1 {
		t.Errorf("probe execs = %d, want exactly one (the whole contract check is one shell pass)", len(execs))
	}
}

func TestProbeImageContract_MissingToolsNamedInOrder(t *testing.T) {
	sb := sandboxfake.New()
	sb.ExecHook = probeResult(1, "missing: rg go\n")

	err := ProbeImageContract(context.Background(), sb)
	if err == nil {
		t.Fatal("a probe over an image without rg/go must fail")
	}
	if !strings.Contains(err.Error(), "missing rg, go") {
		t.Errorf("error = %v, want it to name the missing tools", err)
	}
	if !strings.Contains(err.Error(), "D5") {
		t.Errorf("error = %v, want it to name the image contract", err)
	}
	if !sb.Destroyed() {
		t.Error("the probe must destroy the sandbox even when the contract fails")
	}
}

func TestProbeImageContract_GNUUserlandAnnotated(t *testing.T) {
	sb := sandboxfake.New()
	// BusyBox stat/find pass `command -v` but reject the GNU flags the
	// workspace surface depends on; the annotation is what tells them apart.
	sb.ExecHook = probeResult(1, "missing: stat:-c find:-printf\n")

	err := ProbeImageContract(context.Background(), sb)
	if err == nil {
		t.Fatal("a probe over a BusyBox image must fail")
	}
	for _, want := range []string{"stat:-c", "find:-printf"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %s", err, want)
		}
	}
}

func TestProbeImageContract_OperationalFailureHasBothChannels(t *testing.T) {
	sb := sandboxfake.New()
	// A nonzero exit without the convention line is not a contract result.
	sb.ExecHook = probeResult(2, "")

	err := ProbeImageContract(context.Background(), sb)
	if err == nil {
		t.Fatal("a failed probe exec must fail the probe")
	}
	if !strings.Contains(err.Error(), "exit 2") || !strings.Contains(err.Error(), "stderr detail") {
		t.Errorf("error = %v, want the exit code and stderr surfaced", err)
	}
}

func TestProbeImageContract_ExecTransportError(t *testing.T) {
	sb := sandboxfake.New()
	sb.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
		return goatherdsandbox.ExecResult{}, context.DeadlineExceeded
	}

	err := ProbeImageContract(context.Background(), sb)
	if err == nil || !strings.Contains(err.Error(), "exec") {
		t.Fatalf("probe over a failing exec = %v, want an exec error", err)
	}
	if !sb.Destroyed() {
		t.Error("the probe must destroy the sandbox even when the exec fails")
	}
}

func TestProbeImageContract_CreateFailureStillDestroys(t *testing.T) {
	// A pre-created sandbox refuses the probe's Create (the probe owns the
	// lifecycle), and a real backend's Create can fail after partially
	// provisioning resources — so the cleanup attempt is mandatory on this
	// path too. The destroy is observable here because it succeeds on a
	// sandbox that was, in fact, created.
	sb := sandboxfake.New()
	if err := sb.Create(context.Background(), goatherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	sb.ExecHook = probeResult(0, "")

	err := ProbeImageContract(context.Background(), sb)
	if err == nil {
		t.Fatal("probing an already-created sandbox must fail (the probe owns the lifecycle)")
	}
	if !errors.Is(err, sandboxfake.ErrAlreadyCreated) {
		t.Errorf("error = %v, want it to carry the create failure", err)
	}
	if !sb.Destroyed() {
		t.Error("a failed Create must still attempt Destroy: a partially-provisioned container is a container")
	}
}

func TestProbeImageContract_CreateFailureDestroysEvenUncreated(t *testing.T) {
	// The destroy attempt must happen even when Create failed before any
	// resource existed. failCreate wraps a fresh (never-created) fake and
	// fails Create unconditionally; the probe's cleanup is proven by the
	// fake's own refusal joining the error — it means Destroy ran.
	sb := sandboxfake.New()
	failing := &failCreateSandbox{Sandbox: sb}

	err := ProbeImageContract(context.Background(), failing)
	if err == nil {
		t.Fatal("a failed Create must fail the probe")
	}
	if !errors.Is(err, errCreateRefused) {
		t.Errorf("error = %v, want it to carry the create failure", err)
	}
	if !errors.Is(err, sandboxfake.ErrNotCreated) {
		t.Errorf("error = %v, want the cleanup attempt to reach the backend (its refusal proves it ran)", err)
	}
}

// errCreateRefused is the canned Create failure failCreateSandbox returns.
var errCreateRefused = errors.New("no image")

// failCreateSandbox fails Create but delegates everything else — the probe
// must still attempt Destroy through it.
type failCreateSandbox struct{ goatherdsandbox.Sandbox }

func (*failCreateSandbox) Create(context.Context, goatherdsandbox.SandboxSpec) error {
	return errCreateRefused
}

// TestOpenContainerdWorkspaceSandbox pins the opener's two platform outcomes.
//
// On Linux it resolves through goatherd-runtime's sandbox registry — importing
// the adapter is what registers "containerd", so this also proves the
// self-registration wiring yaah depends on — and hands back an unprovisioned
// sandbox, Create still being the caller's explicit lifecycle step.
//
// Everywhere else it must refuse. The adapter's implementation is gated to
// Linux (goatherd-croft does not compile elsewhere and the substrate it drives
// is Linux-shaped), so no backend registers and the registry reports an
// unknown backend rather than returning something that cannot work. That is
// the invariant config validation relies on when it refuses
// workspace.backend: containerd off-Linux at load: the refusal is real at the
// seam too, not only in the config layer.
func TestOpenContainerdWorkspaceSandbox(t *testing.T) {
	w := config.WorkspaceConfig{
		Backend: config.BackendContainerd,
		Root:    "/sandbox",
		Containerd: config.ContainerdWorkspaceConfig{
			Address:   "/run/containerd/containerd.sock",
			Namespace: "prod",
			Image:     "example/dev:latest",
		},
	}
	sb, err := OpenContainerdWorkspaceSandbox(w)

	if runtime.GOOS != "linux" {
		if err == nil {
			t.Fatalf("on %s the containerd backend must not open, got %v", runtime.GOOS, sb)
		}
		// Assert the cause, not the message. OpenContainerdWorkspaceSandbox
		// wraps every failure as "workspace: open containerd sandbox: %w", so
		// the string "containerd" appears in any error from it — a substring
		// check would pass on a config failure, a bad address, anything, and
		// prove nothing about why the open was refused. The registry's typed
		// error is what distinguishes "this platform provides no such backend"
		// from every other way this can fail.
		var unknown *goatherdsandbox.UnknownSandboxBackendError
		if !errors.As(err, &unknown) {
			t.Errorf("off-Linux the refusal must come from the registry reporting an unknown backend, got %T: %v", err, err)
		}
		return
	}

	if err != nil {
		t.Fatalf("OpenContainerdWorkspaceSandbox: %v", err)
	}
	if sb.Backend() != config.BackendContainerd {
		t.Errorf("Backend() = %q, want %q", sb.Backend(), config.BackendContainerd)
	}
}

// TestContainerdAdapterConfig_Passthrough pins that every containerd field
// yaah's config surface exposes — FIFODir included — reaches the adapter's
// Config, and that Root lands on Workdir.
//
// It tests the mapping function rather than a backend resolved through the
// registry, so it runs on every platform: the adapter registers no backend
// off-Linux, but its Config type is platform-neutral, and a field silently
// dropped between yaah's config surface and the backend is worth catching
// everywhere. This also retires the reflection the previous version needed to
// read an unexported field out of a resolved backend — the adapter's Config is
// exported and comparable, so DeepEqual asserts the whole struct at once and
// would also catch a field being set that should not be.
//
// Registry resolution itself, including its refusal off-Linux, is covered by
// TestOpenContainerdWorkspaceSandbox.
func TestContainerdAdapterConfig_Passthrough(t *testing.T) {
	w := config.WorkspaceConfig{
		Backend: config.BackendContainerd,
		Root:    "/sandbox",
		Containerd: config.ContainerdWorkspaceConfig{
			Address:     "/run/containerd/containerd.sock",
			Namespace:   "prod",
			Image:       "example/dev:latest",
			Snapshotter: "overlayfs",
			FIFODir:     "/var/tmp/yaah-fifo",
		},
	}
	want := croftadapter.Config{
		Address:     "/run/containerd/containerd.sock",
		Namespace:   "prod",
		Image:       "example/dev:latest",
		Snapshotter: "overlayfs",
		FIFODir:     "/var/tmp/yaah-fifo",
		Workdir:     "/sandbox",
	}
	if got := containerdAdapterConfig(w); !reflect.DeepEqual(got, want) {
		t.Errorf("containerdAdapterConfig() =\n\t%+v\nwant\n\t%+v", got, want)
	}
}

// destroyFailingSandbox wraps a sandbox and fails its Destroy, so a probe
// whose teardown fails can be asserted on without teaching the fake a mode
// it should not have.
type destroyFailingSandbox struct {
	goatherdsandbox.Sandbox
}

func (destroyFailingSandbox) Destroy(context.Context) error {
	return errors.New("teardown exploded")
}

// TestProbeImageContract_DestroyFailureIsNotSilent pins the lifecycle
// guarantee: a probe whose teardown fails reports it — on the happy path too,
// because a "successful" probe that leaked its container is not a success.
func TestProbeImageContract_DestroyFailureIsNotSilent(t *testing.T) {
	sb := sandboxfake.New()
	sb.ExecHook = probeResult(0, "")

	err := ProbeImageContract(context.Background(), destroyFailingSandbox{sb})
	if err == nil {
		t.Fatal("a probe whose Destroy fails must report the failure")
	}
	if !strings.Contains(err.Error(), "destroy") || !strings.Contains(err.Error(), "teardown exploded") {
		t.Errorf("error = %v, want the destroy failure surfaced", err)
	}
}

// TestProbeImageContract_DestroyFailureJoinsContractError pins that both
// signals survive: the image contract failure AND the teardown failure.
func TestProbeImageContract_DestroyFailureJoinsContractError(t *testing.T) {
	sb := sandboxfake.New()
	sb.ExecHook = probeResult(1, "missing: rg\n")

	err := ProbeImageContract(context.Background(), destroyFailingSandbox{sb})
	if err == nil {
		t.Fatal("expected both the contract and the teardown failure")
	}
	if !strings.Contains(err.Error(), "missing rg") {
		t.Errorf("error = %v, want the contract failure preserved", err)
	}
	if !strings.Contains(err.Error(), "teardown exploded") {
		t.Errorf("error = %v, want the teardown failure joined", err)
	}
}
