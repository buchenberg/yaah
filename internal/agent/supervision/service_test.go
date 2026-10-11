package supervision

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

// newTestService assembles a real supervision stack for one fake
// sub-agent scope: a SQLite trace store whose bus carries the events
// the kernel supervisor evaluates, a scope manager holding the scope
// and a root, and a registry with the scope's steering channel. Rule
// events are published as the trace middleware would (TraceOwnerID =
// the raw owner; the intervention addresses "scope:<owner>", the same
// form the runner registers under).
func newTestService(t *testing.T, rules AdvisoryRules) (*Service, *shepherdtrace.SQLiteTraceStore, chan string, *shepherdtrace.EffectBus) {
	t.Helper()

	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)
	store.WithBus(bus)

	mgr := shepherd.NewScopeManager(store)
	if _, err := mgr.Create("sess-root", nil); err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	if _, err := mgr.Create("sub-worker", nil); err != nil {
		t.Fatalf("create scope: %v", err)
	}

	runs := NewRegistry()
	steer := make(chan string, SteerBuffer)
	rootSteer := make(chan string, SteerBuffer)
	// The runner registers under Scope.ID() — "scope:<owner>".
	runs.Register("scope:sess-root", Run{SteerCh: rootSteer})
	runs.Register("scope:sub-worker", Run{SteerCh: steer})

	svc := NewService(mgr, bus, runs, rules, false, "scope:sess-root")
	svc.Start("test-supervisor")
	t.Cleanup(svc.Close)
	return svc, store, steer, bus
}

// publish publishes one effect event the way the trace store does.
func publish(bus *shepherdtrace.EffectBus, e shepherdtrace.EffectEvent) {
	e.Timestamp = time.Now()
	bus.Publish(e)
}

// awaitSteer asserts guidance arrives on the scope's steering channel,
// or that none does within the bound.
func awaitSteer(t *testing.T, steer <-chan string, want bool) string {
	t.Helper()
	select {
	case g := <-steer:
		if !want {
			t.Fatalf("unexpected guidance delivered: %q", g)
		}
		return g
	case <-time.After(2 * time.Second):
		if want {
			t.Fatal("expected guidance on the steering channel within 2s")
		}
		return ""
	}
}

// assertInjectRecorded pins the A2 invariant the drain keeps: the trace
// fact is the record. The firing must appear in the target scope's
// trace as a supervisor inject.
func assertInjectRecorded(t *testing.T, store *shepherdtrace.SQLiteTraceStore, owner string) {
	t.Helper()
	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	for _, id := range slice.FactIDs() {
		if slice.FactsByID[id].GetEnvelope().SchemaRef == shepherd.SchemaSupervisorInject {
			return
		}
	}
	t.Error("rule firing not recorded in the trace (want a supervisor:inject fact)")
}

func allAdvisoryRules() AdvisoryRules {
	return AdvisoryRules{DestructiveTool: true, HighErrorRate: true, StuckDetection: true}
}

// TestService_DestructiveRuleFires pins the advisory destructive rule
// end to end: a destructive bash declaration on the bus becomes an
// inject intervention, drained into the registered run's steering
// channel, with the firing recorded in the target scope's trace.
func TestService_DestructiveRuleFires(t *testing.T) {
	_, store, steer, bus := newTestService(t, allAdvisoryRules())

	publish(bus, shepherdtrace.EffectEvent{
		TraceOwnerID: "sub-worker",
		Mode:         shepherdtrace.Declaration,
		SchemaRef:    "yaah.tool.bash.v1",
		KindLabel:    "bash",
		Payload:      map[string]any{"cmd": "rm -rf /tmp/probe"},
	})

	g := awaitSteer(t, steer, true)
	if want := "destructive"; !strings.Contains(g, want) {
		t.Errorf("guidance = %q, want it to name the destructive operation", g)
	}
	assertInjectRecorded(t, store, "sub-worker")
}

// TestService_HighErrorRateFires pins the error-rate rule: failing
// tool captures past the threshold produce guidance, with the kernel's
// defaults when the config carries no tuning.
func TestService_HighErrorRateFires(t *testing.T) {
	_, store, steer, bus := newTestService(t, allAdvisoryRules())

	// The kernel's default window is 10 and it evaluates from
	// window/2 results on, so five failures suffice.
	for i := 0; i < 5; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sub-worker",
			Mode:         shepherdtrace.Capture,
			SchemaRef:    "yaah.tool.bash.v1.applied",
			KindLabel:    "bash:result",
			Payload:      map[string]any{"success": false},
		})
	}

	g := awaitSteer(t, steer, true)
	if want := "error rate"; !strings.Contains(g, want) {
		t.Errorf("guidance = %q, want it to name the error rate", g)
	}
	assertInjectRecorded(t, store, "sub-worker")
}

// TestService_ErrorThresholdFromConfig pins that the threshold is
// configuration, not a constant: at threshold 1.0 the same 80%-failure
// stream that fires at the 0.5 default stays silent.
func TestService_ErrorThresholdFromConfig(t *testing.T) {
	_, _, steer, bus := newTestService(t, AdvisoryRules{
		HighErrorRate:      true,
		ErrorRateThreshold: 1.0,
		ErrorRateWindow:    10,
	})

	for i := 0; i < 4; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sub-worker",
			Mode:         shepherdtrace.Capture,
			KindLabel:    "bash:result",
			Payload:      map[string]any{"success": false},
		})
	}
	publish(bus, shepherdtrace.EffectEvent{
		TraceOwnerID: "sub-worker",
		Mode:         shepherdtrace.Capture,
		KindLabel:    "bash:result",
		Payload:      map[string]any{"success": true},
	})
	awaitSteer(t, steer, false)
}

// TestService_StuckDetectionFires pins the stuck rule: the same tool
// declaration repeated to the threshold becomes guidance on the last
// repetition.
func TestService_StuckDetectionFires(t *testing.T) {
	_, store, steer, bus := newTestService(t, allAdvisoryRules())

	// The kernel default threshold is 3.
	for i := 0; i < 3; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sub-worker",
			Mode:         shepherdtrace.Declaration,
			SchemaRef:    "yaah.tool.read.v1",
			KindLabel:    "read",
			Payload:      map[string]any{"path": "/same/file"},
		})
	}

	g := awaitSteer(t, steer, true)
	if want := "repeated"; !strings.Contains(g, want) {
		t.Errorf("guidance = %q, want it to name the repetition", g)
	}
	assertInjectRecorded(t, store, "sub-worker")
}

// TestService_RuleDisabledIsSilent pins the per-rule switches: with
// only the destructive rule enabled, neither failing captures nor
// repetitions produce guidance.
func TestService_RuleDisabledIsSilent(t *testing.T) {
	_, _, steer, bus := newTestService(t, AdvisoryRules{DestructiveTool: true})

	for i := 0; i < 5; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sub-worker",
			Mode:         shepherdtrace.Capture,
			KindLabel:    "bash:result",
			Payload:      map[string]any{"success": false},
		})
	}
	for i := 0; i < 3; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sub-worker",
			Mode:         shepherdtrace.Declaration,
			KindLabel:    "read",
			Payload:      map[string]any{"path": "/same/file"},
		})
	}
	awaitSteer(t, steer, false)
}

// TestService_RootFiringsDropped pins the plan's exit criterion — the
// parent session stays untouched: the root scope's own repeated
// declarations (the exact stream a parent emits when it retries a
// dispatch) produce no steering and no recorded firing, while the same
// stream from a sub-agent scope does. The manual supervisor tool's
// root inject (A2) is a different path and is unaffected.
func TestService_RootFiringsDropped(t *testing.T) {
	_, store, steer, bus := newTestService(t, allAdvisoryRules())

	// A parent repeating its own tool call trips stuck-detection; the
	// events carry the ROOT's trace owner.
	for i := 0; i < 3; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sess-root",
			Mode:         shepherdtrace.Declaration,
			SchemaRef:    "yaah.tool.spawn_subagent.v1",
			KindLabel:    "spawn_subagent",
			Payload:      map[string]any{"role": "worker"},
		})
	}
	awaitSteer(t, steer, false)

	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "sess-root", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	for _, id := range slice.FactIDs() {
		if slice.FactsByID[id].GetEnvelope().SchemaRef == shepherd.SchemaSupervisorInject {
			t.Error("a root firing must not be recorded — the fact is the record of a delivered intervention, and this one is dropped by policy")
		}
	}
}

// TestService_NoSelfFeedback pins the feedback loop closed: the drain
// records each firing in the trace, and the store publishes that append
// back to the bus — where the stuck rule would match the
// supervisor:inject declarations (identical payload) and fire again,
// forever. After one real firing there must be exactly one recorded
// inject and no cascade of further guidance.
func TestService_NoSelfFeedback(t *testing.T) {
	_, store, steer, bus := newTestService(t, AdvisoryRules{StuckDetection: true})

	for i := 0; i < 3; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sub-worker",
			Mode:         shepherdtrace.Declaration,
			KindLabel:    "read",
			Payload:      map[string]any{"path": "/same/file"},
		})
	}
	awaitSteer(t, steer, true)

	// Without the supervisor-record filter this window fills with
	// self-generated guidance; with it, the one firing is the last.
	time.Sleep(500 * time.Millisecond)
	select {
	case g := <-steer:
		t.Fatalf("self-feedback: unexpected further guidance %q", g)
	default:
	}

	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "sub-worker", 999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	injects := 0
	for _, id := range slice.FactIDs() {
		if slice.FactsByID[id].GetEnvelope().SchemaRef == shepherd.SchemaSupervisorInject {
			injects++
		}
	}
	if injects != 1 {
		t.Errorf("recorded inject facts = %d, want exactly 1 (the real firing, no cascade)", injects)
	}
}

// TestService_CloseJoinsDrain pins the teardown contract the session's
// close relies on: Close stops the engine and returns only after the
// drain goroutine has exited, and stays idempotent.
func TestService_CloseJoinsDrain(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)
	store.WithBus(bus)

	mgr := shepherd.NewScopeManager(store)
	if _, err := mgr.Create("sub-worker", nil); err != nil {
		t.Fatalf("create scope: %v", err)
	}
	runs := NewRegistry()
	steer := make(chan string, SteerBuffer)
	runs.Register("scope:sub-worker", Run{SteerCh: steer})

	svc := NewService(mgr, bus, runs, allAdvisoryRules(), false, "")
	svc.Start("close-test")

	publish(bus, shepherdtrace.EffectEvent{
		TraceOwnerID: "sub-worker",
		Mode:         shepherdtrace.Declaration,
		SchemaRef:    "yaah.tool.bash.v1",
		KindLabel:    "bash",
		Payload:      map[string]any{"cmd": "rm -rf /tmp/probe"},
	})
	awaitSteer(t, steer, true)

	done := make(chan struct{})
	go func() {
		svc.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return; the drain goroutine is stuck")
	}
	svc.Close() // idempotent: a second close must not panic or hang
}

// TestService_NoRecordNoDelivery pins the fact-before-intervention
// contract: a firing whose trace fact cannot be written is NOT
// delivered. A halted scope refuses injects (the scope is over, its
// sandbox torn down), so the stuck rule's firing lands nowhere — no
// steering message may claim an intervention that was never recorded.
func TestService_NoRecordNoDelivery(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)
	store.WithBus(bus)

	mgr := shepherd.NewScopeManager(store)
	scope, err := mgr.Create("sub-worker", nil)
	if err != nil {
		t.Fatalf("create scope: %v", err)
	}
	if err := scope.Halt(); err != nil {
		t.Fatalf("halt scope: %v", err)
	}
	runs := NewRegistry()
	steer := make(chan string, SteerBuffer)
	runs.Register("scope:sub-worker", Run{SteerCh: steer})

	svc := NewService(mgr, bus, runs, allAdvisoryRules(), false, "")
	svc.Start("no-record-test")
	t.Cleanup(svc.Close)

	for i := 0; i < 3; i++ {
		publish(bus, shepherdtrace.EffectEvent{
			TraceOwnerID: "sub-worker",
			Mode:         shepherdtrace.Declaration,
			KindLabel:    "read",
			Payload:      map[string]any{"path": "/same/file"},
		})
	}
	awaitSteer(t, steer, false)
}

// TestService_StartAfterCloseIsNoop pins the lifecycle guard: a
// Close-then-Start sequence must not resurrect the drain goroutine
// (whose wg.Add would race the completed wg.Wait — the WaitGroup reuse
// rule), and Close stays safe to call repeatedly.
func TestService_StartAfterCloseIsNoop(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)
	mgr := shepherd.NewScopeManager(store)
	runs := NewRegistry()

	svc := NewService(mgr, bus, runs, allAdvisoryRules(), false, "")
	svc.Start("lifecycle-test")
	svc.Close()
	svc.Start("lifecycle-test") // must be a no-op, not a resurrected drain

	done := make(chan struct{})
	go func() {
		svc.Close() // idempotent even after the no-op Start
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("second Close did not return; the no-op Start resurrected a drain goroutine")
	}
}

// newGuardService assembles a service whose blocking shell guard is
// enabled — the synchronous pre-dispatch check (adoption plan B2) —
// with no advisory rules, so a deny can only come from the guard.
func newGuardService(t *testing.T) (*Service, func(ownerID, toolName, argsJSON string) string) {
	t.Helper()
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)
	store.WithBus(bus)
	mgr := shepherd.NewScopeManager(store)
	runs := NewRegistry()

	svc := NewService(mgr, bus, runs, AdvisoryRules{}, true, "scope:sess-root")
	t.Cleanup(svc.Close)
	return svc, svc.GuardCheck()
}

// TestGuardCheck_DeniesShellBeforeDispatch pins the blocking guard's
// verdicts (adoption plan B2, decision D3): every shell call — the
// command does not matter, a shell can execute anything a pattern
// matcher cannot see — is denied with a reason; the structured tools
// pass, because their review is the merge gate's job (Phase C), not
// this net.
func TestGuardCheck_DeniesShellBeforeDispatch(t *testing.T) {
	_, check := newGuardService(t)
	if check == nil {
		t.Fatal("GuardCheck must be non-nil when the guard is enabled")
	}

	for _, tc := range []struct {
		tool string
		args string
		deny bool
	}{
		{tool: "bash", args: `{"command":"rm -rf /tmp/probe"}`, deny: true},
		{tool: "bash", args: `{"command":"ls -la"}`, deny: true},
		{tool: "powershell", args: `{"command":"Get-ChildItem"}`, deny: true},
		{tool: "read", args: `{"path":"/workspace/file"}`, deny: false},
		{tool: "write", args: `{"path":"/workspace/file","content":"x"}`, deny: false},
		{tool: "edit", args: `{"path":"/workspace/file"}`, deny: false},
	} {
		reason := check("sub-worker", tc.tool, tc.args)
		if tc.deny && reason == "" {
			t.Errorf("%s call must be denied before dispatch", tc.tool)
		}
		if !tc.deny && reason != "" {
			t.Errorf("%s call must pass (structured tools are the approved path), got %q", tc.tool, reason)
		}
	}
}

// TestGuardCheck_DisabledIsNil pins the off-switch: a service built
// without the guard exposes no check, and a nil check is the
// pipeline's pass-through — tracing-off sessions dispatch unchanged.
func TestGuardCheck_DisabledIsNil(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	bus := shepherdtrace.NewEffectBus(64)
	t.Cleanup(bus.Close)

	svc := NewService(shepherd.NewScopeManager(store), bus, NewRegistry(), allAdvisoryRules(), false, "")
	if svc.GuardCheck() != nil {
		t.Error("GuardCheck must be nil when the shell guard is disabled")
	}
}
