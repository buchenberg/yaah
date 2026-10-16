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
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/shepherd-runtime-go"
	shepherdsupervisor "github.com/buchenberg/shepherd-runtime-go/supervisor"
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

// Firing is one counted rule firing against one scope (adoption plan
// B4): the `supervisor status` surface lists these, so the
// orchestrator can see which rules acted this session without reading
// logs. Kind is the intervention type as fired (inject, halt, deny).
type Firing struct {
	Rule  string
	Scope string
	Kind  string
	Count int
}

// firingKey aggregates firings per rule, scope, and kind.
type firingKey struct{ rule, scope, kind string }

// Service owns the kernel supervisor for one session and the drain
// goroutine that turns its interventions into steering.
type Service struct {
	sup         *shepherdsupervisor.Supervisor
	guard       *shepherdsupervisor.Supervisor
	mgr         *shepherd.ScopeManager
	runs        *Registry
	bus         *shepherdtrace.EffectBus
	rootScopeID string
	haltRoot    bool

	// firings counts rule firings for `supervisor status` (adoption
	// plan B4). Recorded where the rule name is known — the advisory
	// wrapper for the engine's rules, the guard closure for its denies
	// — at the moment the rule acts, before any dispatch policy is
	// applied: a firing dropped against the root or refused by the
	// halt gate still fired, and status says so.
	firingsMu sync.Mutex
	firings   map[firingKey]int

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
		if iv == nil || iv.Type != shepherdsupervisor.InterventionDeny {
			return ""
		}
		s.recordFiring("destructive_shell_guard", iv)
		// Fail closed: a deny verdict denies regardless of whether the
		// payload type-asserts or carries text. A silent empty string
		// here would turn a non-string (or empty) payload into an
		// allow — fail-open in the one pre-dispatch net sub-agent
		// calls pass through.
		reason, ok := iv.Payload.(string)
		if !ok || reason == "" {
			return "Blocked destructive operation. Supervisor denied."
		}
		return reason
	}
}

// advisoryRule adapts a kernel rule for the session's engine with the
// three invariants the plan holds:
//
//  1. Supervisor records never feed it — the drain records each firing
//     in the trace, the store publishes that append, and the stuck rule
//     matches ALL declarations: without the filter, one firing feeds
//     the next in an endless loop.
//  2. Its guidance names its source (adoption plan B3: the guidance
//     text includes the rule), so the loop being steered — and the
//     trace holding the fact — can tell an error-rate warning from a
//     stuck-loop warning apart.
//  3. Each firing is counted (adoption plan B4): onFiring observes the
//     intervention at the moment the rule acts — before dispatch
//     policy, so a root-dropped or gate-refused firing still counts —
//     keyed by the rule's name for `supervisor status`.
func advisoryRule(rule shepherdsupervisor.SupervisionRule, onFiring func(rule string, iv *shepherdsupervisor.Intervention)) shepherdsupervisor.SupervisionRule {
	innerMatch := rule.Match
	innerAction := rule.Action
	rule.Match = func(e shepherdtrace.EffectEvent) bool {
		if e.SchemaRef == shepherd.SchemaSupervisorInject {
			return false
		}
		return innerMatch(e)
	}
	rule.Action = func(e shepherdtrace.EffectEvent) *shepherdsupervisor.Intervention {
		iv := innerAction(e)
		if iv == nil {
			return nil
		}
		if onFiring != nil {
			onFiring(rule.Name, iv)
		}
		if payload, ok := iv.Payload.(string); ok {
			iv.Payload = fmt.Sprintf("[%s] %s (observed: %s)", rule.Name, payload, e.KindLabel)
		}
		return iv
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

// haltQuiesceTimeout bounds the drain's wait for a halted run to stop
// before the kernel-side halt lands (adoption plan B3), mirroring the
// A3 manual halt's own bound (internal/tools/supervisor.go — the value
// is duplicated rather than shared because tools imports this package,
// not the other way around).
const haltQuiesceTimeout = 10 * time.Second

// shellGuardRule composes the blocking shell guard from the kernel's
// DestructiveToolGuard (adoption plan B2, decision D3: "default-deny
// for bash"), reusing the kernel's deny action verbatim. The kernel's
// own matcher block-matches bash, write, and delete — the plan's
// settled scope is the shell only (both of yaah's: bash on POSIX,
// powershell on Windows, which the kernel's matcher has never heard
// of), because structured tools are the approved path for changes and
// their review is the merge gate's job (Phase C), not this net.
//
// background_process joins the shells for its command-running action
// only: it executes an arbitrary command through the host process
// manager — the same "a shell can execute anything a pattern matcher
// cannot see" hazard, in a second door. Its read-only actions
// (status, output) and stop pass: a command is the hazard, and only
// the start action carries one.
func shellGuardRule() shepherdsupervisor.SupervisionRule {
	guard := shepherdsupervisor.DestructiveToolGuard()
	return shepherdsupervisor.SupervisionRule{
		Name: "destructive_shell_guard",
		Match: func(e shepherdtrace.EffectEvent) bool {
			if e.Mode != shepherdtrace.Declaration {
				return false
			}
			switch e.KindLabel {
			case "bash", "powershell":
				return true
			case "background_process":
				cmd, _ := e.Payload["cmd"].(string)
				return cmd != ""
			}
			return false
		},
		Action: guard.Action,
	}
}

// NewService assembles the kernel supervisor with the enabled advisory
// rules, and — when shellGuard is set — a second, never-started
// supervisor carrying the blocking shell guard for synchronous
// pre-dispatch checks (B2). The thresholds default inside the kernel
// rules when zero, so a bare AdvisoryRules{DestructiveTool: true, ...}
// needs no tuning knowledge.
//
// rootScopeID is the session's root scope (adoption plan D1): advisory
// injections against it are dropped — the parent's own calls already
// passed the approval middleware — but a HALT against it is the B3
// path: dispatched only when haltRoot is set (the supervisor.halt_root
// opt-in, default off — a rule halting the interactive session is a
// big hammer). The manual supervisor tool's root inject (A2) is
// unaffected either way.
func NewService(mgr *shepherd.ScopeManager, bus *shepherdtrace.EffectBus, runs *Registry, rules AdvisoryRules, shellGuard, haltRoot bool, rootScopeID string) *Service {
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

	sup := shepherdsupervisor.NewSupervisor(mgr, bus)
	var guard *shepherdsupervisor.Supervisor
	svc := &Service{sup: sup, mgr: mgr, runs: runs, bus: bus, rootScopeID: rootScopeID, haltRoot: haltRoot}

	if rules.DestructiveTool {
		sup.AddRule(advisoryRule(shepherdsupervisor.DestructiveToolRule(), svc.recordFiring))
	}
	if rules.HighErrorRate {
		sup.AddRule(advisoryRule(shepherdsupervisor.HighErrorRateRule(errorThreshold, errorWindow), svc.recordFiring))
	}
	if rules.StuckDetection {
		sup.AddRule(advisoryRule(shepherdsupervisor.StuckDetectionRule(stuckRepeat), svc.recordFiring))
	}

	if shellGuard {
		// A second instance, never started: it must never subscribe to
		// the bus, or every deny would also surface as an async
		// intervention the advisory drain would have to drop. Its
		// CheckCall is the whole surface.
		guard = shepherdsupervisor.NewSupervisor(mgr, nil)
		guard.AddRule(shellGuardRule())
	}
	svc.guard = guard
	return svc
}

// recordFiring counts one rule firing for the status surface (adoption
// plan B4).
func (s *Service) recordFiring(rule string, iv *shepherdsupervisor.Intervention) {
	s.firingsMu.Lock()
	if s.firings == nil {
		s.firings = map[firingKey]int{}
	}
	s.firings[firingKey{rule: rule, scope: iv.ScopeID, kind: string(iv.Type)}]++
	s.firingsMu.Unlock()
}

// Firings returns the session's counted rule firings (adoption plan
// B4), sorted by rule then scope then kind — a deterministic snapshot
// for `supervisor status`. Empty when no rule has acted.
func (s *Service) Firings() []Firing {
	s.firingsMu.Lock()
	defer s.firingsMu.Unlock()
	out := make([]Firing, 0, len(s.firings))
	for k, n := range s.firings {
		out = append(out, Firing{Rule: k.rule, Scope: k.scope, Kind: k.kind, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Kind < out[j].Kind
	})
	return out
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

		switch iv.Type {
		case shepherdsupervisor.InterventionInject:
			if s.rootScopeID != "" && scopeID == s.rootScopeID {
				// The parent's own calls already passed the approval
				// middleware; rule-driven session steering is out of
				// B1's scope (and the plan's exit criterion is
				// explicit: the parent session stays untouched). The
				// firing is deliberately not recorded — the trace fact
				// is the record of a delivered intervention, and this
				// one is dropped by policy, not lost in transit.
				slog.Debug("supervisor: rule firing against the root scope dropped",
					"scope", scopeID)
				continue
			}
			s.deliverInject(scopeID, guidance)
		case shepherdsupervisor.InterventionHalt:
			s.deliverHalt(scopeID, guidance)
		default:
			// The enabled rules emit injects and (from B3) the drain
			// dispatches halts. A deny is B2's synchronous path and
			// cannot arrive here; a dropped unknown type must not be
			// silent.
			slog.Warn("supervisor: dropping non-advisory intervention",
				"type", string(iv.Type), "scope", scopeID)
		}
	}
}

// deliverHalt dispatches a rule-driven halt (adoption plan B3) with the
// A3 ordering: cancel the running loop FIRST, wait — bounded — for it
// to quiesce, and only then record the kernel-side halt, which tears
// down an owned sandbox. Cancelling alone is not quiescence: an
// in-flight tool can still be inside the very sandbox scope.Halt
// destroys next.
//
// The root scope is the opt-in gate: a rule halting the interactive
// session is a big hammer, dispatched only under supervisor.halt_root
// (default off). A root halt with the gate off is refused and logged —
// the session keeps running.
func (s *Service) deliverHalt(scopeID, reason string) {
	if s.rootScopeID != "" && scopeID == s.rootScopeID && !s.haltRoot {
		slog.Warn("supervisor: rule-driven halt of the root scope refused — set supervisor.halt_root to allow it",
			"scope", scopeID)
		return
	}

	var quiesced, waited bool
	if quiesce, err := s.runs.Cancel(scopeID); err == nil {
		if quiesce != nil {
			waited = true
			select {
			case <-quiesce:
				quiesced = true
			case <-time.After(haltQuiesceTimeout):
				// Bounded proceed, the A3 semantics: the kernel halt
				// still lands — halt-while-running is its meaning —
				// and the log names the unquiesced run.
			}
		}
	} else if errors.Is(err, ErrNotHaltable) {
		// A registered run that cannot be cancelled is still using
		// the scope's sandbox: the kernel-side halt would destroy it
		// underneath a live run — the exact teardown hazard the
		// cancel-first ordering exists to prevent. The manual halt
		// tolerates this (a human asked); a rule must not.
		slog.Warn("supervisor: halt refused — the run registered no cancellation", "scope", scopeID)
		return
	} else if !errors.Is(err, ErrNoRun) {
		// A delivery failure the registry did not classify is a
		// bridge bug; report it rather than half-halt.
		slog.Warn("supervisor: halt delivery failed", "scope", scopeID, "err", err)
		return
	}
	if scope, ok := s.mgr.Get(scopeID); ok {
		if err := scope.Halt(); err != nil {
			slog.Warn("supervisor: kernel halt failed", "scope", scopeID, "err", err)
			return
		}
	}
	switch {
	case waited && !quiesced:
		slog.Warn("supervisor: halted scope; the run was cancelled but had not stopped within the bound",
			"scope", scopeID, "reason", reason)
	default:
		slog.Info("supervisor: halted scope", "scope", scopeID, "reason", reason)
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
