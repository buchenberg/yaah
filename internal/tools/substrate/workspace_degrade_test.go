package substrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/goatherd-runtime/sandbox/containment"
)

// unconfinableBackend reports itself available and then refuses at probe time.
//
// This is what a host does when it can mint a restricted token but cannot make
// the write restriction grant-conformant: `winacl`'s per-root conformance check
// fails on the writable root itself ("DENIES writes (profile too strict)"), so
// every launch dies even though the availability check passed. A dispatch lane
// in this state loses every execution tool at once — the bug behind yaah-1tkp.2.
type unconfinableBackend struct{}

func (unconfinableBackend) Name() string              { return "unconfinable" }
func (unconfinableBackend) EnforcementTier() string   { return "paddock" }
func (unconfinableBackend) Available() (bool, string) { return true, "restricted token" }
func (unconfinableBackend) SupportsNetwork(confinement.NetMode) bool {
	return true
}
func (unconfinableBackend) ProfileFor([]string, confinement.NetworkPolicy) string {
	return "profile"
}
func (unconfinableBackend) Probe(string, string, []string) error {
	return fmt.Errorf("%w: writable root %s DENIES writes (profile too strict)",
		containment.ErrPaddockNotEstablished, "C:/world")
}
func (unconfinableBackend) Launch(string, containment.LaunchRequest) (containment.LaunchResult, error) {
	return containment.LaunchResult{}, nil
}

// echoWorkspaceHealth runs the workspace's own shell printing a marker, so a
// test asserts the fallback actually executed a command rather than a no-op.
func echoWorkspaceHealth(t *testing.T, ws *LocalWorkspace) ExecResult {
	t.Helper()
	shell, flag := ws.Shell()
	res, err := ws.Exec(context.Background(), ExecRequest{Command: shell, Args: []string{flag, "echo advisory"}})
	if err != nil {
		t.Fatalf("Exec returned an error, want the advisory fallback to run: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "advisory") {
		t.Fatalf("advisory fallback result = %+v, want exit 0 and the marker in stdout", res)
	}
	return res
}

// containment: auto is documented as "advisory, recorded" when enforcement
// cannot be established. A probe that fails *after* the availability check used
// to bypass that contract entirely and fail every command in the lane.
func TestConfinementDegrade_AutoFallsBackAndRecords(t *testing.T) {
	ws := confinedTestWorkspace(t.TempDir(), unconfinableBackend{}, containment.PlacementAuto)

	if reason, ok := WorkspaceDegraded(ws); ok || reason != "" {
		t.Fatalf("degraded before any command ran: %q", reason)
	}

	echoWorkspaceHealth(t, ws)

	reason, ok := WorkspaceDegraded(ws)
	if !ok {
		t.Fatal("auto placement lost its jail but reported no downgrade")
	}
	if !strings.Contains(reason, "DENIES writes") {
		t.Fatalf("recorded reason = %q, want the backend's own explanation", reason)
	}
	// The commands run on the host path, so WorkspaceConfined must not keep
	// claiming the body is jailed.
	if WorkspaceConfined(ws) {
		t.Fatal("a degraded workspace still reports itself confined")
	}
}

// The record is one-time and shared across the per-call clones of the
// workspace, so a lane does not re-log the same condition per tool call.
func TestConfinementDegrade_RecordedOnceAcrossClones(t *testing.T) {
	ws := confinedTestWorkspace(t.TempDir(), unconfinableBackend{}, containment.PlacementAuto)
	echoWorkspaceHealth(t, ws)

	first, _ := WorkspaceDegraded(ws)

	// WithEnv clones the struct but not the pointer's target, so the record is
	// shared rather than restarted.
	clone := ws.WithEnv([]string{"YAAH_TEST=1"})
	second, ok := WorkspaceDegraded(clone)
	if !ok || second != first {
		t.Fatalf("clone reason = %q ok=%v, want the original %q", second, ok, first)
	}
}

// paddock is the explicit "jail or refuse" placement: it must keep failing
// closed, and the message must name the fix.
func TestConfinementDegrade_PaddockStillFailsClosed(t *testing.T) {
	ws := confinedTestWorkspace(t.TempDir(), unconfinableBackend{}, containment.PlacementPaddock)

	_, err := ws.Exec(context.Background(), ExecRequest{Command: "echo", Args: []string{"nope"}})
	if err == nil {
		t.Fatal("paddock placement must refuse when confinement cannot be established")
	}
	if !strings.Contains(err.Error(), "agents.subagent.containment: auto") {
		t.Fatalf("error = %v, want it to name the remedy", err)
	}
	if _, ok := WorkspaceDegraded(ws); ok {
		t.Fatal("paddock must not record a downgrade")
	}
}

// A declared grant set is a promise the unpoliced host path cannot keep, so
// narrowing disables the fallback even under auto.
func TestConfinementDegrade_NarrowedGrantsStillFail(t *testing.T) {
	root := t.TempDir()
	ws := confinedTestWorkspace(root, unconfinableBackend{}, containment.PlacementAuto).
		WithWritableRoots([]string{root})

	_, err := ws.Exec(context.Background(), ExecRequest{Command: "echo", Args: []string{"nope"}})
	if err == nil {
		t.Fatal("a narrowed grant set must not silently run unconfined")
	}
	// The remedy must name grants, not offer "containment: auto" — auto is
	// already the placement here, so that advice would be a no-op.
	if !strings.Contains(err.Error(), "grants:") {
		t.Fatalf("error = %v, want it to name the declared grant set", err)
	}
	if _, ok := WorkspaceDegraded(ws); ok {
		t.Fatal("narrowed grants must not record a downgrade")
	}
}

// Egress rules live in the jail, so degrading would silently drop them.
func TestConfinementDegrade_NetworkRuleStillFails(t *testing.T) {
	cases := []struct {
		mode     confinement.NetMode
		wantBody string // the text that must explain the refusal
	}{
		// deny_all reaches the jail and is refused by degradeBlocker.
		{confinement.NetDenyAll, "network policy"},
		// broker cannot even be lowered on this host, so it is refused earlier;
		// it must still refuse, and must not degrade.
		{confinement.NetBroker, "NetBroker"},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			root := t.TempDir()
			pv := NewPathValidator(root, false, nil)
			pv.WorkDir = pv.WorkspaceRoot
			ws := NewConfinedLocalWorkspace(pv, unconfinableBackend{}, containment.PlacementAuto,
				confinement.NetworkPolicy{Mode: tc.mode})

			_, err := ws.Exec(context.Background(), ExecRequest{Command: "echo", Args: []string{"nope"}})
			if err == nil {
				t.Fatalf("network mode %q must not degrade to an unpoliced host path", tc.mode)
			}
			if !strings.Contains(err.Error(), tc.wantBody) {
				t.Fatalf("error = %v, want it to name %q", err, tc.wantBody)
			}
			if _, ok := WorkspaceDegraded(ws); ok {
				t.Fatalf("network mode %q must not record a downgrade", tc.mode)
			}
		})
	}
}

// countingBackend is unconfinableBackend plus attempt counters, so a test can
// prove a recorded downgrade stops the workspace re-attempting — and re-probing
// — confinement on every subsequent command.
type countingBackend struct {
	mu       sync.Mutex
	probes   int
	launches int
}

func (b *countingBackend) Name() string              { return "counting" }
func (b *countingBackend) EnforcementTier() string   { return "paddock" }
func (b *countingBackend) Available() (bool, string) { return true, "restricted token" }
func (b *countingBackend) SupportsNetwork(confinement.NetMode) bool {
	return true
}
func (b *countingBackend) ProfileFor([]string, confinement.NetworkPolicy) string {
	return "profile"
}
func (b *countingBackend) Probe(string, string, []string) error {
	b.mu.Lock()
	b.probes++
	b.mu.Unlock()
	return fmt.Errorf("%w: writable root %s DENIES writes (profile too strict)",
		containment.ErrPaddockNotEstablished, "C:/world")
}
func (b *countingBackend) Launch(string, containment.LaunchRequest) (containment.LaunchResult, error) {
	b.mu.Lock()
	b.launches++
	b.mu.Unlock()
	return containment.LaunchResult{}, nil
}

// attempts reports how many times the workspace asked the backend to establish
// confinement.
func (b *countingBackend) attempts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.probes + b.launches
}

// A recorded downgrade is permanent — the probe that failed once fails on every
// call — so later commands must not pay for the failing probe again. Re-running
// it per command turned a single unenforceable host into a per-command stall
// across a long dispatch lane.
func TestConfinementDegrade_SkipsRepeatedConfineAttempts(t *testing.T) {
	backend := &countingBackend{}
	ws := confinedTestWorkspace(t.TempDir(), backend, containment.PlacementAuto)

	echoWorkspaceHealth(t, ws)
	first := backend.attempts()
	if first == 0 {
		t.Fatal("the first command never asked the backend to confine; test cannot distinguish a short-circuit from a no-op")
	}
	if reason, ok := WorkspaceDegraded(ws); !ok || reason == "" {
		t.Fatalf("downgrade not recorded after the first command: reason=%q ok=%v", reason, ok)
	}

	echoWorkspaceHealth(t, ws)
	echoWorkspaceHealth(t, ws)

	if got := backend.attempts(); got != first {
		t.Fatalf("backend attempts rose from %d to %d across two later commands; a recorded downgrade must skip the confine attempt", first, got)
	}
}

// A nil record is what a workspace built without confinement carries; recording
// into it must be a no-op rather than a panic.
func TestConfinementDowngrade_NilRecordIsSafe(t *testing.T) {
	var d *confinementDowngrade
	if got := d.record("boom"); got != "" {
		t.Fatalf("nil record returned %q, want the empty string", got)
	}
	if _, ok := d.get(); ok {
		t.Fatal("nil record reported a downgrade")
	}
}

// A workspace that never asked for confinement reports no downgrade, so callers
// can distinguish "advisory by design" from "lost its jail".
func TestConfinementDegrade_UnconfinedWorkspaceReportsNothing(t *testing.T) {
	ws := newLocalWorkspace(NewPathValidator(t.TempDir(), false, nil))
	if reason, ok := WorkspaceDegraded(ws); ok || reason != "" {
		t.Fatalf("unconfined workspace reported a downgrade: %q", reason)
	}
}
