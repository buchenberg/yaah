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

// probeResult is the canned outcome the fake's ExecHook returns for the
// contract script: the exit code, stdout, and stderr the probe must classify.
func probeResult(exit int, stdout string) func(shepherdsandbox.ExecRequest) (shepherdsandbox.ExecResult, error) {
	return func(shepherdsandbox.ExecRequest) (shepherdsandbox.ExecResult, error) {
		return shepherdsandbox.ExecResult{ExitCode: exit, Stdout: stdout, Stderr: "stderr detail"}, nil
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
	sb.ExecHook = func(shepherdsandbox.ExecRequest) (shepherdsandbox.ExecResult, error) {
		return shepherdsandbox.ExecResult{}, context.DeadlineExceeded
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
	if err := sb.Create(context.Background(), shepherdsandbox.SandboxSpec{}); err != nil {
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
type failCreateSandbox struct{ shepherdsandbox.Sandbox }

func (*failCreateSandbox) Create(context.Context, shepherdsandbox.SandboxSpec) error {
	return errCreateRefused
}

// TestOpenContainerdWorkspaceSandbox pins the registry resolution: the config
// surface reaches the backend through shepherdsandbox.OpenSandbox (both backends
// self-register), and the returned sandbox is unprovisioned.
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
	if err != nil {
		t.Fatalf("OpenContainerdWorkspaceSandbox: %v", err)
	}
	if sb.Backend() != config.BackendContainerd {
		t.Errorf("Backend() = %q, want %q through the registry", sb.Backend(), config.BackendContainerd)
	}
}

// destroyFailingSandbox wraps a sandbox and fails its Destroy, so a probe
// whose teardown fails can be asserted on without teaching the fake a mode
// it should not have.
type destroyFailingSandbox struct {
	shepherdsandbox.Sandbox
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
