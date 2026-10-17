package tools

// confinement_egress_test.go exercises the network axis of the jail (Plan 7
// Phase 3, agents.subagent.network). The write jail is tested elsewhere; this is
// the other half of "confined": a sub-agent that cannot be trusted with the
// network must not have it, and "broker" must mean *only* the broker.
//
// These are live tests — a real listener, a real bash /dev/tcp connect, and the
// real kernel deciding. They need Linux, a Landlock backend with ABI >= 4 (the
// network rules), and bash; they skip elsewhere rather than pretending.

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/egress"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
)

// egressTestEnv skips unless this host can actually enforce egress.
func egressTestEnv(t *testing.T) containment.Backend {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Landlock is Linux-only")
	}
	backend := landlock.Detect()
	if backend == nil || !backend.SupportsNetwork(containment.NetDenyAll) {
		t.Skip("no Landlock network support on this host (needs ABI >= 4)")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH (needed for a /dev/tcp connect)")
	}
	return backend
}

// confinedNetworkWorkspace is confinedTestWorkspace with an egress posture.
func confinedNetworkWorkspace(root string, backend containment.Backend, mode containment.NetMode, brokerPort int) *localWorkspace {
	pv := NewPathValidator(root, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	return NewConfinedLocalWorkspace(pv, backend, confinement.PlacementPaddock,
		containment.NetworkPolicy{Mode: mode, BrokerPort: brokerPort})
}

// liveListener accepts (and records) connections on a loopback port.
func liveListener(t *testing.T) (port int, accepted func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var count int
	done := make(chan struct{})
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			count++
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { close(done) })

	return ln.Addr().(*net.TCPAddr).Port, func() int { return count }
}

// connect runs a bare TCP connect from inside the jail and reports whether the
// kernel let it through.
func connect(t *testing.T, ws *localWorkspace, port int) (bool, ExecResult) {
	t.Helper()
	script := fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d", port)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, _ := ws.Exec(ctx, ExecRequest{Command: "bash", Args: []string{"-c", script}, SeparateStreams: true})
	return res.ExitCode == 0, res
}

// TestConfinedEgress_DenyAllRefusesConnect: under deny_all the kernel refuses
// the connect — and the listener never sees it, which is the assertion that
// matters (a refusal the peer never observes).
func TestConfinedEgress_DenyAllRefusesConnect(t *testing.T) {
	backend := egressTestEnv(t)
	port, accepted := liveListener(t)
	root := t.TempDir()

	// Control: the same command under allow_all reaches the listener, so a
	// failure below is the rule and not a broken harness.
	open := confinedNetworkWorkspace(root, backend, containment.NetAllowAll, 0)
	if ok, res := connect(t, open, port); !ok {
		t.Fatalf("the allow_all control must connect (exit=%d stderr=%s)", res.ExitCode, res.Stderr)
	}
	if got := accepted(); got == 0 {
		t.Fatal("the control connect must have been accepted")
	}

	denied := confinedNetworkWorkspace(t.TempDir(), backend, containment.NetDenyAll, 0)
	ok, res := connect(t, denied, port)
	if ok {
		t.Fatalf("deny_all must refuse the connect, got exit=0 (%+v)", res)
	}
	if res.ExitCode == -1 {
		t.Fatalf("the jail must establish, so the refusal is a denial and not a launch failure: %s", res.Stderr)
	}
	if got := accepted(); got != 1 {
		t.Errorf("the listener must see only the control connection, saw %d", got)
	}
}

// TestConfinedEgress_BrokerPinsToItsPort: "broker" means the broker and nothing
// else — a connect to the broker's port is permitted, and a connect to any other
// port is refused. (Landlock net rules are port-scoped, not address-scoped, so
// this is the guarantee the kernel can actually make; the broker then decides by
// host.)
func TestConfinedEgress_BrokerPinsToItsPort(t *testing.T) {
	backend := egressTestEnv(t)

	broker := egress.NewBroker(egress.AllowHosts("allowed.example"))
	brokerPort, err := broker.Start()
	if err != nil {
		t.Fatalf("start the broker: %v", err)
	}
	t.Cleanup(broker.Stop)

	otherPort, acceptedOther := liveListener(t)
	ws := confinedNetworkWorkspace(t.TempDir(), backend, containment.NetBroker, brokerPort)

	if ok, res := connect(t, ws, brokerPort); !ok {
		t.Fatalf("the pinned broker port must be reachable (exit=%d stderr=%s)", res.ExitCode, res.Stderr)
	}
	if ok, res := connect(t, ws, otherPort); ok {
		t.Fatalf("only the broker port may be reachable, got a connect to %d (%+v)", otherPort, res)
	}
	if got := acceptedOther(); got != 0 {
		t.Errorf("a non-broker port must see nothing, saw %d connection(s)", got)
	}
}
