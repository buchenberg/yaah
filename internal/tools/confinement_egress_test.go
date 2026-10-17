package tools

// confinement_egress_test.go exercises the network axis of the jail (Plan 7
// Phase 3, agents.subagent.network). The write jail is tested elsewhere; this is
// the other half of "confined": a sub-agent that cannot be trusted with the
// network must not have it, and "broker" must mean *only* the broker — including
// the broker's own host decision, which is the half a port pin cannot make.
//
// These are live tests — a real listener, a real socket from inside the jail, and
// the real kernel (or broker) deciding. They need Linux, a Landlock backend with
// network support (ABI >= 4), and bash for /dev/tcp; they skip elsewhere rather
// than pretending.

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
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
		t.Skip("bash not on PATH (needed for a /dev/tcp socket)")
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

// connSink accepts connections and announces each one on a channel, so a test
// waits for an accept instead of racing a counter. (Reading a counter from
// another goroutine is a data race, and a completed client connect does not
// prove the accept happened yet.)
type connSink struct {
	port int
	ch   chan struct{}
}

func newConnSink(t *testing.T, depth int) *connSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &connSink{port: ln.Addr().(*net.TCPAddr).Port, ch: make(chan struct{}, depth)}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = conn.Close()
			s.ch <- struct{}{}
		}
	}()
	return s
}

// accepted reports how many connections have been accepted within a short grace
// period: a refused connect never arrives, and waiting forever for one would
// hang the test rather than fail it.
func (s *connSink) accepted(t *testing.T) int {
	t.Helper()
	n := 0
	for {
		select {
		case <-s.ch:
			n++
		case <-time.After(200 * time.Millisecond):
			return n
		}
	}
}

// bashIn runs a bash one-liner inside the jail.
func bashIn(t *testing.T, ws *localWorkspace, script string) ExecResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, _ := ws.Exec(ctx, ExecRequest{Command: "bash", Args: []string{"-c", script}, SeparateStreams: true})
	return res
}

// connect opens a bare TCP connection to a port from inside the jail.
func connect(t *testing.T, ws *localWorkspace, port int) ExecResult {
	t.Helper()
	return bashIn(t, ws, fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d", port))
}

// TestConfinedEgress_DenyAllRefusesConnect: under deny_all the kernel refuses the
// connect — and the listener never sees it, which is the assertion that matters
// (a refusal the peer never observes).
func TestConfinedEgress_DenyAllRefusesConnect(t *testing.T) {
	backend := egressTestEnv(t)
	sink := newConnSink(t, 4)

	// Control: the same command under allow_all reaches the listener, so a
	// failure below is the rule and not a broken harness.
	open := confinedNetworkWorkspace(t.TempDir(), backend, containment.NetAllowAll, 0)
	if res := connect(t, open, sink.port); res.ExitCode != 0 {
		t.Fatalf("the allow_all control must connect (exit=%d stderr=%s)", res.ExitCode, res.Stderr)
	}
	if got := sink.accepted(t); got != 1 {
		t.Fatalf("the control connect must have been accepted, saw %d", got)
	}

	denied := confinedNetworkWorkspace(t.TempDir(), backend, containment.NetDenyAll, 0)
	res := connect(t, denied, sink.port)
	if res.ExitCode == 0 {
		t.Fatalf("deny_all must refuse the connect (%+v)", res)
	}
	if res.ExitCode == -1 {
		t.Fatalf("the jail must establish, so the refusal is a denial and not a launch failure: %s", res.Stderr)
	}
	if got := sink.accepted(t); got != 0 {
		t.Errorf("the listener must see nothing under deny_all, saw %d", got)
	}
}

// TestConfinedEgress_BrokerPinsToItsPort: "broker" means the broker and nothing
// else — a connect to the broker's port is permitted, and a connect to any other
// port is refused. (Landlock net rules are port-scoped, not address-scoped, so
// this is the guarantee the kernel can actually make; the broker then decides by
// host, which the next test covers.)
func TestConfinedEgress_BrokerPinsToItsPort(t *testing.T) {
	backend := egressTestEnv(t)

	broker := egress.NewBroker(egress.AllowHosts("allowed.example"))
	brokerPort, err := broker.Start()
	if err != nil {
		t.Fatalf("start the broker: %v", err)
	}
	t.Cleanup(broker.Stop)

	other := newConnSink(t, 1)
	ws := confinedNetworkWorkspace(t.TempDir(), backend, containment.NetBroker, brokerPort)

	if res := connect(t, ws, brokerPort); res.ExitCode != 0 {
		t.Fatalf("the pinned broker port must be reachable (exit=%d stderr=%s)", res.ExitCode, res.Stderr)
	}
	if res := connect(t, ws, other.port); res.ExitCode == 0 {
		t.Fatalf("only the broker port may be reachable, got a connect to %d", other.port)
	}
	if got := other.accepted(t); got != 0 {
		t.Errorf("a non-broker port must see nothing, saw %d connection(s)", got)
	}
}

// TestConfinedEgress_BrokerDecidesByHost is the half the port pin cannot make:
// from inside the jail, an allowlisted host tunnels through the broker and a host
// outside the allowlist is refused. It speaks CONNECT by hand so the test does
// not depend on a proxy-aware client being installed.
func TestConfinedEgress_BrokerDecidesByHost(t *testing.T) {
	backend := egressTestEnv(t)

	origin := newConnSink(t, 4) // stands in for the allowlisted host
	broker := egress.NewBroker(egress.AllowHosts("127.0.0.1"))
	brokerPort, err := broker.Start()
	if err != nil {
		t.Fatalf("start the broker: %v", err)
	}
	t.Cleanup(broker.Stop)

	ws := confinedNetworkWorkspace(t.TempDir(), backend, containment.NetBroker, brokerPort)
	connectTo := func(host string, port int) string {
		t.Helper()
		script := fmt.Sprintf(
			"exec 3<>/dev/tcp/127.0.0.1/%d || exit 9\n"+
				"printf 'CONNECT %s:%d HTTP/1.1\\r\\nHost: %s:%d\\r\\n\\r\\n' >&3\n"+
				"IFS= read -r -t 5 line <&3 && printf '%%s' \"$line\"\n", brokerPort, host, port, host, port)
		res := bashIn(t, ws, script)
		return strings.TrimSpace(res.Stdout)
	}

	if got := connectTo("127.0.0.1", origin.port); !strings.Contains(got, "200") {
		t.Fatalf("an allowlisted host must tunnel through the broker, got %q (broker decisions: %+v)",
			got, broker.Decisions())
	}
	if got := connectTo("blocked.example", 443); !strings.Contains(got, "403") {
		t.Fatalf("a host outside the allowlist must be refused, got %q (broker decisions: %+v)",
			got, broker.Decisions())
	}
	// The broker's own record is the audit trail the decision rests on.
	var allowed, denied bool
	for _, d := range broker.Decisions() {
		switch d.Verdict {
		case egress.VerdictAllow:
			allowed = true
		case egress.VerdictDeny:
			denied = true
		}
	}
	if !allowed || !denied {
		t.Errorf("the broker must record both decisions, got %+v", broker.Decisions())
	}
}
