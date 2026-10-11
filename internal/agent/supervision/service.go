// Service bridges the kernel's advisory supervision rules to the D2
// execution bridge (shepherd-adoption plan B1): the kernel supervisor
// watches the trace's effect bus, and this service drains its
// interventions into the run registry, so a rule firing becomes a
// delivered steering message in the running loop — not just a trace
// fact.
//
// The service is advisory-only by construction (B1): it consumes
// InterventionInject and drops every other intervention type — the
// blocking destructive guard is B2's synchronous path, and rule-driven
// halts are B3's.
package supervision

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
)

// AdvisoryRules carries the advisory rules' switches and tuning.
// Zero thresholds mean the documented defaults (0.5 error rate over a
// window of 10 — the window's zero-default lives in the kernel — and a
// repeat threshold of 3); this struct only transports configuration.
type AdvisoryRules struct {
	// DestructiveTool enables the advisory destructive-declaration
	// warning rule.
	DestructiveTool bool

	// HighErrorRate enables the error-rate guidance rule.
	HighErrorRate bool

	// ErrorRateThreshold and ErrorRateWindow carry the rule's tuning;
	// zero threshold means 0.5, zero window the kernel's own 10.
	ErrorRateThreshold float64
	ErrorRateWindow    int

	// StuckDetection enables the repeated-call guidance rule.
	StuckDetection bool

	// StuckRepeatThreshold carries the rule's tuning; zero means 3.
	StuckRepeatThreshold int
}

// Any reports whether at least one advisory rule is on — the wiring's
// cheap answer to "is there anything to supervise with".
func (r AdvisoryRules) Any() bool {
	return r.DestructiveTool || r.HighErrorRate || r.StuckDetection
}

// Service owns the kernel supervisor for one session and the drain
// goroutine that turns its interventions into steering.
type Service struct {
	sup         *shepherd.Supervisor
	guard       *shepherd.Supervisor
	mgr         *shepherd.ScopeManager
	runs        *Registry
	bus         *shepherdtrace.EffectBus
	rootScopeID string

	// mu guards the lifecycle transitions: wg.Add in Start must never
	// race wg.Wait in Close — the WaitGroup reuse rule — so both hold
	// the mutex through their transition, and closed makes Start a
	// no-op once Close has begun.
	mu      sync.Mutex
	closed  bool
	started bool
	wg      sync.WaitGroup
}

// GuardCheck returns the blocking pre-dispatch check for sub-agent
// loops (adoption plan B2): the sub-agent pipeline consults it before
// a tool call executes, and a non-empty reason denies the call. Nil
// when the shell guard is disabled in config — a nil check is the
// pass-through, so tracing-off sessions are byte-identical to today.
//
// The event mirrors the declaration the trace middleware records —
// schema ref, kind label, and the shell command under "cmd" — so the
// rule set evaluates pre-dispatch and post-hoc against one shape.
//
// Safe under concurrent sub-agent loops: the kernel's CheckCall copies
// the rule slice under a read lock, and the guard rule is stateless —
// both closures read only the event — so concurrent dispatches share
// one check without shared mutable state.
func (s *Service) GuardCheck() func(ownerID, toolName, argsJSON string) string {
	if s.guard == nil {
		return nil
	}
	return func(ownerID, toolName, argsJSON string) string {
		payload := map[string]any{
			"tool": toolName,
			"args": json.RawMessage(argsJSON),
		}
		if toolName == "bash" || toolName == "powershell" || toolName == "background_process" {
			var args struct {
				Command string `json:"command"`
			}
			if json.Unmarshal([]byte(argsJSON), &args) == nil && args.Command != "" {
				payload["cmd"] = args.Command
			}
		}
		iv := s.guard.CheckCall(shepherdtrace.EffectEvent{
			TraceOwnerID: ownerID,
			Mode:         shepherdtrace.Declaration,
			SchemaRef:    "yaah.tool." + toolName + ".v1",
			KindLabel:    toolName,
			Payload:      payload,
		})
		if iv == nil || iv.Type != shepherd.InterventionDeny {
			return ""
		}
		// Fail closed: a deny verdict denies regardless of whether the
		// payload type-asserts. A silent empty-string here would turn a
		// non-string payload into an allow — fail-open in the one
		// pre-dispatch net sub-agent calls pass through.
		reason, ok := iv.Payload.(string)
		if !ok {
			return "Blocked destructive operation. Supervisor denied."
		}
		return reason
	}
}

// skippingSupervisorRecords wraps a rule so it never matches the
// supervisor's own trace records. This is load-bearing, not polish:
// the drain records each firing in the target scope's trace, the store
// publishes that append to the bus like any other, and the stuck rule
// matches ALL declarations — including supervisor:inject ones with an
// identical payload. Without the filter, one firing feeds the next:
// record → event → repeat counter → fire → record, an endless loop a
// live session confirmed at hundreds of firings per second.
func skippingSupervisorRecords(rule shepherd.SupervisionRule) shepherd.SupervisionRule {
	inner := rule.Match
	rule.Match = func(e shepherdtrace.EffectEvent) bool {
		if e.SchemaRef == shepherd.SchemaSupervisorInject {
			return false
		}
		return inner(e)
	}
	return rule
}

// The advisory rules' documented defaults (plan §7). The kernel's rule
// constructors default only the error-rate window — a zero repeat
// threshold makes the stuck rule fire on every declaration, and a zero
// error-rate threshold on every half-window — so the zeros of an
// unconfigured stanza resolve here.
const (
	defaultErrorRateThreshold = 0.5
	defaultStuckRepeat        = 3
)

// shellGuardRule composes the blocking shell guard from the kernel's
// DestructiveToolGuard (adoption plan B2, decision D3: "default-deny
// for bash"), reusing the kernel's deny action verbatim. The kernel's
// own matcher block-matches bash, write, and delete — the plan's
// settled scope is the shell only (both of yaah's: bash on POSIX,
// powershell on Windows, which the kernel's matcher has never heard
// of), because structured tools are the approved path for changes and
// their review is the merge gate's job (Phase C), not this net.
//
// background_process joins the shells: it runs an arbitrary command
// through the host process manager — exactly the "a shell can execute
// anything a pattern matcher cannot see" hazard, in a second door.
func shellGuardRule() shepherd.SupervisionRule {
	guard := shepherd.DestructiveToolGuard()
	return shepherd.SupervisionRule{
		Name: "destructive_shell_guard",
		Match: func(e shepherdtrace.EffectEvent) bool {
			if e.Mode != shepherdtrace.Declaration {
				return false
			}
			return e.KindLabel == "bash" || e.KindLabel == "powershell" || e.KindLabel == "background_process"
		},
		Action: guard.Action,
	}
}

// NewService assembles the kernel supervisor with the enabled advisory
// rules, and — when shellGuard is set — a second, never-started
// supervisor carrying the blocking shell guard for synchronous
// pre-dispatch checks (B2). The thresholds default inside the kernel
// rules when zero, so a bare AdvisoryRules{DestructiveTool: true, ...}
// needs no tuning knowledge. rootScopeID is the session's root scope
// (adoption plan D1): rule-driven interventions against it are
// dropped — advisory rules exist to rescue sub-agents whose calls no
// policy gate sees, while the parent's own calls already passed the
// approval middleware, and steering the session mid-run is B3's gated
// territory. The manual supervisor tool's root inject (A2) is
// unaffected.
func NewService(mgr *shepherd.ScopeManager, bus *shepherdtrace.EffectBus, runs *Registry, rules AdvisoryRules, shellGuard bool, rootScopeID string) *Service {
	errorThreshold := rules.ErrorRateThreshold
	if errorThreshold <= 0 {
		errorThreshold = defaultErrorRateThreshold
	}
	stuckRepeat := rules.StuckRepeatThreshold
	if stuckRepeat <= 0 {
		stuckRepeat = defaultStuckRepeat
	}
	// A negative window would narrow the kernel's own <= 0 default
	// clause to exactly zero; zero keeps the kernel's default (10).
	errorWindow := rules.ErrorRateWindow
	if errorWindow < 0 {
		errorWindow = 0
	}

	sup := shepherd.NewSupervisor(mgr, bus)
	if rules.DestructiveTool {
		sup.AddRule(skippingSupervisorRecords(shepherd.DestructiveToolRule()))
	}
	if rules.HighErrorRate {
		sup.AddRule(skippingSupervisorRecords(shepherd.HighErrorRateRule(errorThreshold, errorWindow)))
	}
	if rules.StuckDetection {
		sup.AddRule(skippingSupervisorRecords(shepherd.StuckDetectionRule(stuckRepeat)))
	}

	var guard *shepherd.Supervisor
	if shellGuard {
		// A second instance, never started: it must never subscribe to
		// the bus, or every deny would also surface as an async
		// intervention the advisory drain would have to drop. Its
		// CheckCall is the whole surface.
		guard = shepherd.NewSupervisor(mgr, nil)
		guard.AddRule(shellGuardRule())
	}
	return &Service{sup: sup, guard: guard, mgr: mgr, runs: runs, bus: bus, rootScopeID: rootScopeID}
}

// Start subscribes the supervisor to the bus and launches the drain
// goroutine. Idempotent, and a no-op after Close: a service that is
// closing cannot gain a new drain goroutine.
func (s *Service) Start(subscriberID string) {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	if s.bus == nil {
		// The kernel subscribes to nothing without a bus, so its
		// interventions channel would never close and the drain would
		// wedge Close's join. Nothing to supervise either way.
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()

	s.sup.Start(subscriberID)
	go s.drain()
}

// Close stops the supervisor and waits for the drain goroutine to
// exit. The kernel closes the interventions channel on its way down,
// which is what ends the drain; Close is therefore also the join point
// tests use to assert no goroutine leaks. Idempotent, and makes any
// later Start a no-op.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.sup.Close()
	s.wg.Wait()
}

// drain bridges interventions to execution. For each inject, the
// A2 order holds: the trace fact is the record, the registry delivery
// is the intervention — so the fact is written first (scope.Inject),
// then the guidance is delivered (runs.Inject). Every delivery
// outcome is honest in the log: a scope with no running loop keeps
// its recorded firing, and a full steering buffer names the wedged
// run instead of pretending delivery happened.
func (s *Service) drain() {
	defer s.wg.Done()

	for iv := range s.sup.Interventions() {
		// The kernel rules address scopes as "scope:<owner>" — which is
		// exactly the form the runner registers under (Scope.ID()) and
		// the manager keys on, so the intervention's ScopeID addresses
		// both bridges unchanged.
		scopeID := iv.ScopeID
		guidance, ok := iv.Payload.(string)
		if !ok {
			slog.Warn("supervisor: dropping intervention with non-string payload",
				"type", string(iv.Type), "scope", scopeID)
			continue
		}

		if iv.Type != shepherd.InterventionInject {
			// Advisory-only by construction (B1): the enabled rules
			// emit injects. A deny is B2's synchronous path, a halt
			// is B3's opt-in; neither can arrive from this rule set,
			// but a dropped unknown type must not be silent.
			slog.Warn("supervisor: dropping non-advisory intervention",
				"type", string(iv.Type), "scope", scopeID)
			continue
		}
		if s.rootScopeID != "" && scopeID == s.rootScopeID {
			// The parent's own calls already passed the approval
			// middleware; rule-driven session steering is out of B1's
			// scope (and the plan's exit criterion is explicit: the
			// parent session stays untouched). The firing is
			// deliberately not recorded — the trace fact is the
			// record of a delivered intervention, and this one is
			// dropped by policy, not lost in transit.
			slog.Debug("supervisor: rule firing against the root scope dropped",
				"scope", scopeID)
			continue
		}
		s.deliverInject(scopeID, guidance)
	}
}

// deliverInject writes the firing into the scope's trace, then steers
// the run if one is registered under the scope. The fact is the record;
// the delivery is the intervention — so a firing that cannot be
// recorded is NOT delivered: a delivery with no trace fact would read
// as an intervention that never happened, the exact inversion of the
// A2 contract. The practical cases are benign races — a scope halted
// mid-firing refuses injects, and a scope missing from the manager
// cannot be addressed at all — and in both the run is over, so there
// is nothing left to steer anyway.
func (s *Service) deliverInject(scopeID, guidance string) {
	scope, ok := s.mgr.Get(scopeID)
	if !ok {
		slog.Warn("supervisor: firing not delivered — scope not addressable", "scope", scopeID)
		return
	}
	if err := scope.Inject(guidance); err != nil {
		slog.Warn("supervisor: firing not delivered — not recorded in the trace",
			"scope", scopeID, "err", err)
		return
	}
	switch err := s.runs.Inject(scopeID, guidance); {
	case err == nil:
		slog.Info("supervisor: guidance delivered", "scope", scopeID, "guidance", guidance)
	case errors.Is(err, ErrNoRun):
		// The firing is recorded in the trace; the scope simply has no
		// loop to steer right now (finished, or a root between runs).
		slog.Debug("supervisor: firing recorded, no active run", "scope", scopeID)
	case errors.Is(err, ErrSteerBusy):
		slog.Warn("supervisor: run not consuming guidance; consider halting the scope",
			"scope", scopeID)
	default:
		slog.Warn("supervisor: inject delivery failed", "scope", scopeID, "err", err)
	}
}
