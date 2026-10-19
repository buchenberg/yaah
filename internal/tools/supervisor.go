package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	shepherd "github.com/buchenberg/shepherd-runtime-go"

	"github.com/buchenberg/yaah/internal/agent/supervision"
)

// haltQuiesceTimeout bounds the wait for a halted run to stop before
// the kernel-side sandbox teardown proceeds anyway — the same
// cancel-then-join-bounded shape as the jobs manager's CloseAndWait.
const haltQuiesceTimeout = 10 * time.Second

// SupervisorTool lets the orchestrator supervise sub-agent execution:
// intervention (inject/halt) on running sub-agents, and scope inspection
// (list_scopes, status). Judgement of a sub-agent's work is not an action
// here — it is the orchestrator settling the proposal.
//
// The tool operates on the session's scope manager and trace store,
// injected by the wiring (plan 07, H4) — the same instances the
// shepherd_trace middleware and the sub-agent runner carry, so the
// supervisor, the runner, and the tracing share one registry.
type SupervisorTool struct {
	TraceDir string

	// RootScopeID is the session's root scope (adoption plan D1), set by
	// the wiring. Inject into it steers the session itself (its steering
	// channel, plan A2). Halt maps to cancelling the session's current
	// run — the ctrl-C equivalent — and carries the same consequences:
	// the root scope goes non-active, so later sub-agent scopes fall
	// back to flat creation and supervision degrades to scope-less
	// tracing for the rest of the process.
	RootScopeID string

	// ScopeManager is the session's scope registry (plan 07, H4):
	// session-owned and injected by the wiring, like the run registry.
	// Nil when tracing is disabled.
	ScopeManager *shepherd.ScopeManager

	// HaltRoot is the agents.default.supervisor_halt_root opt-in
	// (adoption plan A3): root halt needs it, and it defaults off — a
	// model or a rule halting the interactive session is a big hammer.
	HaltRoot bool

	// Runs is the session's supervision run registry (adoption plan A2),
	// injected by the wiring — session-owned, not a process global. Nil
	// when tracing is disabled: inject then degrades honestly to the
	// trace-only outcome.
	Runs *supervision.Registry

	// Svc is the session's advisory-rules service (adoption plan B4):
	// status lists its rule firings — which rules acted, on which
	// scopes, how often. Nil when the supervisor stanza is not live;
	// status then reports no firings rather than guessing.
	Svc *supervision.Service
}

func (*SupervisorTool) Name() string { return "supervisor" }
func (*SupervisorTool) Description() string {
	return "Supervise running sub-agents: list scopes, inject guidance, halt an agent, and report supervision status"
}

func (*SupervisorTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list_scopes", "inject", "halt", "status"],
				"description": "list_scopes: show all scopes and their state; inject: push guidance into a scope; halt: force-stop a scope; status: show supervision system status"
			},
			"scope_id": {
				"type": "string",
				"description": "Scope ID (required for inject and halt)"
			},
			"guidance": {
				"type": "string",
				"description": "Guidance text to inject (required for inject)"
			}
		},
		"required": ["action"]
	}`)
}

// supervisorActionArgs is the parsed argument set for the supervisor tool:
// scope fields apply to inject/halt.
type supervisorActionArgs struct {
	Action   string `json:"action"`
	ScopeID  string `json:"scope_id"`
	Guidance string `json:"guidance"`
}

func (t *SupervisorTool) Execute(ctx context.Context, args string) (string, error) {
	var raw supervisorActionArgs
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("supervisor: invalid arguments: %w", err)
	}

	mgr := t.ScopeManager
	if mgr == nil {
		return "", fmt.Errorf("supervisor: shepherd scope manager unavailable (trace store failed to open)")
	}

	switch raw.Action {
	case "list_scopes":
		return executeListScopes(mgr)
	case "inject":
		if raw.ScopeID == "" || raw.Guidance == "" {
			return "", fmt.Errorf("supervisor: scope_id and guidance required for inject")
		}
		return executeInject(mgr, t.Runs, raw.ScopeID, raw.Guidance)
	case "halt":
		if raw.ScopeID == "" {
			return "", fmt.Errorf("supervisor: scope_id required for halt")
		}
		if raw.ScopeID == t.RootScopeID && !t.HaltRoot {
			return "", fmt.Errorf("supervisor: halting the session root requires the supervisor_halt_root opt-in (agents.default.supervisor_halt_root); it stops the session's current run and ends scope-tree supervision for this process. Halt a sub-agent scope instead (see list_scopes)")
		}
		return executeHalt(mgr, t.Runs, raw.ScopeID)
	case "status":
		return executeStatus(mgr, t.Runs, t.Svc)
	default:
		return "", fmt.Errorf("supervisor: unknown action %q (valid: list_scopes, inject, halt, status)", raw.Action)
	}
}

func executeListScopes(mgr *shepherd.ScopeManager) (string, error) {
	all := mgr.AllScopes()
	if len(all) == 0 {
		return "No scopes registered.", nil
	}

	var sb string
	sb = "Scopes:\n"
	for _, s := range all {
		parentID := "(root)"
		if s.Parent() != nil {
			parentID = s.Parent().ID()
		}
		sb += fmt.Sprintf("  %s  owner=%s  state=%s  parent=%s\n",
			s.ID(), s.OwnerID(), s.State(), parentID)
	}
	return sb, nil
}

func executeInject(mgr *shepherd.ScopeManager, runs *supervision.Registry, scopeID, guidance string) (string, error) {
	scope, ok := mgr.Get(scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", scopeID)
	}
	if err := scope.Inject(guidance); err != nil {
		return "", fmt.Errorf("supervisor: inject failed: %w", err)
	}
	// Supervision bridge (adoption plan A2): the trace fact above is the
	// record; the registry delivery is the intervention. Every outcome
	// below reports exactly what happened — the fact that the trace
	// already holds the intervention never turns a delivery failure
	// into a hard error the orchestrator would misread as "nothing was
	// recorded".
	if runs == nil {
		return fmt.Sprintf("Guidance recorded in the trace for %s; the supervision bridge is not active in this session.", scopeID), nil
	}
	switch err := runs.Inject(scopeID, guidance); {
	case err == nil:
		return fmt.Sprintf("Injected guidance into %s; delivered to the running loop.", scopeID), nil
	case errors.Is(err, supervision.ErrNoRun):
		return fmt.Sprintf("Guidance recorded in the trace for %s; no active run to deliver it to (the scope is not currently executing).", scopeID), nil
	case errors.Is(err, supervision.ErrSteerBusy):
		return fmt.Sprintf("Guidance recorded in the trace for %s, but the run's steering buffer is full — it is not consuming guidance; consider halting the scope instead.", scopeID), nil
	default:
		return "", fmt.Errorf("supervisor: inject delivery failed: %w", err)
	}
}

func executeHalt(mgr *shepherd.ScopeManager, runs *supervision.Registry, scopeID string) (string, error) {
	scope, ok := mgr.Get(scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", scopeID)
	}
	// Halt bridge (adoption plan A3): cancel the run FIRST, then WAIT —
	// bounded — for it to quiesce before recording the kernel halt,
	// which tears down an owned sandbox (an isolated child's worktree).
	// Cancelling alone is not quiescence: an in-flight tool can still be
	// inside the very sandbox scope.Halt destroys next, the teardown
	// hazard the jobs manager's CloseAndWait documents. Every outcome
	// reports what happened; a finished run's halt stays a trace-side
	// record, never a phantom cancellation.
	var cancelled, quiesced, waited bool
	if runs != nil {
		quiesce, err := runs.Cancel(scopeID)
		switch {
		case err == nil:
			cancelled = true
			if quiesce != nil {
				waited = true
				select {
				case <-quiesce:
					quiesced = true
				case <-time.After(haltQuiesceTimeout):
					// Bounded proceed: the kernel halt still lands —
					// halt-while-running is its semantics — and the
					// report names the unquiesced run.
				}
			}
		case errors.Is(err, supervision.ErrNoRun), errors.Is(err, supervision.ErrNotHaltable):
			// fall through to the kernel-side halt below
		default:
			return "", fmt.Errorf("supervisor: halt delivery failed: %w", err)
		}
	}
	if err := scope.Halt(); err != nil {
		return "", fmt.Errorf("supervisor: halt failed: %w", err)
	}
	switch {
	case cancelled && (!waited || quiesced):
		return fmt.Sprintf("Halted %s; the running loop was cancelled and has stopped.", scopeID), nil
	case cancelled:
		return fmt.Sprintf("Halted %s; the run was cancelled but had not stopped within %s — an in-flight tool may have been interrupted by the sandbox teardown.", scopeID, haltQuiesceTimeout), nil
	default:
		return fmt.Sprintf("Halted %s in the trace; no active run to cancel (the scope is not currently executing).", scopeID), nil
	}
}

func executeStatus(mgr *shepherd.ScopeManager, runs *supervision.Registry, svc *supervision.Service) (string, error) {
	active := mgr.ActiveScopes()
	all := mgr.AllScopes()
	out := fmt.Sprintf("Supervision status:\n  Scopes: %d active, %d total\n", len(active), len(all))
	if runs != nil {
		ids := runs.ScopeIDs()
		out += fmt.Sprintf("  Bridged runs: %d\n", len(ids))
		for _, id := range ids {
			out += fmt.Sprintf("    %s (injectable)\n", id)
		}
	} else {
		out += "  Bridged runs: 0 (supervision bridge not active)\n"
	}
	switch {
	case svc == nil:
		out += "  Rule firings: 0 (supervision service not active)\n"
	default:
		firings := svc.Firings()
		if len(firings) == 0 {
			out += "  Rule firings: 0\n"
			break
		}
		out += "  Rule firings:\n"
		for _, f := range firings {
			out += fmt.Sprintf("    %s  %s  %dx (%s)\n", f.Rule, f.Scope, f.Count, f.Kind)
		}
	}
	return out, nil
}
