package tools

import (
	"context"
	"encoding/json"
	"fmt"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// SupervisorTool lets the orchestrator supervise sub-agent execution.
// It exposes intervention operations (inject/halt) on running sub-agents,
// scope inspection (list_scopes, status), and the supervised review
// session verdict cycle: continue, rollback, review_diff, fork, choose,
// accept, abort.
//
// The review-session actions operate on git checkpoints and tree states
// (shepherd-kernel-go); sessions are started via supervised_task with
// review:true.
//
// The tool uses SharedScopeManager (set by the shepherd_trace pipeline
// builder) so it shares the same store connection and in-memory scope
// registry as the trace middleware.
type SupervisorTool struct {
	TraceDir string
}

func (*SupervisorTool) Name() string { return "supervisor" }
func (*SupervisorTool) Description() string {
	return "Supervise sub-agent execution: list scopes, inject guidance, halt agents, and drive supervised review sessions (continue, rollback, fork, choose, review_diff, accept, abort)"
}

func (*SupervisorTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list_scopes", "inject", "halt", "status", "continue", "rollback", "review_diff", "fork", "choose", "accept", "abort"],
				"description": "list_scopes: show all scopes and their state; inject: push guidance into a scope; halt: force-stop a scope; status: show supervision system status; review-session verdicts (session started via supervised_task review:true): continue: accept the last unit and run the next one; rollback: revert the last unit (files+conversation) and rerun with more specific guidance; review_diff: re-fetch the current diff/report; fork: rewind and run two prompt variants from the same checkpoint; choose: apply the winning fork variant; accept: keep the work and close the session; abort: rewind the unaccepted unit and close the session"
			},
			"scope_id": {
				"type": "string",
				"description": "Scope ID (required for inject and halt)"
			},
			"guidance": {
				"type": "string",
				"description": "Guidance text (required for inject and rollback; optional for continue)"
			},
			"session_id": {
				"type": "string",
				"description": "Supervised review session ID from the supervised_task review envelope (required for continue, rollback, review_diff, fork, choose, accept, abort)"
			},
			"prompt_a": {
				"type": "string",
				"description": "First variant prompt (required for fork)"
			},
			"prompt_b": {
				"type": "string",
				"description": "Second variant prompt (required for fork)"
			},
			"winner": {
				"type": "string",
				"enum": ["a", "b"],
				"description": "Winning fork variant (required for choose)"
			},
			"restore": {
				"type": "boolean",
				"default": true,
				"description": "abort only: rewind the unaccepted unit (default true)"
			}
		},
		"required": ["action"]
	}`)
}

// supervisorActionArgs is the parsed argument set for the supervisor
// tool. Session fields apply to the review verdict actions; scope
// fields to inject/halt.
type supervisorActionArgs struct {
	Action    string `json:"action"`
	ScopeID   string `json:"scope_id"`
	Guidance  string `json:"guidance"`
	SessionID string `json:"session_id"`
	PromptA   string `json:"prompt_a"`
	PromptB   string `json:"prompt_b"`
	Winner    string `json:"winner"`
	Restore   *bool  `json:"restore"`
}

func (t *SupervisorTool) Execute(ctx context.Context, args string) (string, error) {
	var raw supervisorActionArgs
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("supervisor: invalid arguments: %w", err)
	}

	mgr := SharedScopeManager
	if mgr == nil {
		return "", fmt.Errorf("supervisor: shepherd tracing not enabled (set shepherd_trace_dir in config)")
	}

	switch raw.Action {
	case "list_scopes":
		return executeListScopes(mgr)
	case "inject":
		if raw.ScopeID == "" || raw.Guidance == "" {
			return "", fmt.Errorf("supervisor: scope_id and guidance required for inject")
		}
		return executeInject(mgr, raw.ScopeID, raw.Guidance)
	case "halt":
		if raw.ScopeID == "" {
			return "", fmt.Errorf("supervisor: scope_id required for halt")
		}
		return executeHalt(mgr, raw.ScopeID)
	case "status":
		return executeStatus(mgr)
	case "continue", "rollback", "review_diff", "fork", "choose", "accept", "abort":
		if raw.Action == "continue" && raw.Guidance == "" {
			raw.Guidance = "Proceed with the next work unit."
		}
		return executeReviewAction(ctx, raw)
	default:
		return "", fmt.Errorf("supervisor: unknown action %q (valid: list_scopes, inject, halt, status, continue, rollback, review_diff, fork, choose, accept, abort)", raw.Action)
	}
}

// executeReviewAction dispatches a supervised review session verdict.
func executeReviewAction(ctx context.Context, raw supervisorActionArgs) (string, error) {
	if raw.SessionID == "" {
		return "", fmt.Errorf("supervisor: session_id required for %s (start a review session with supervised_task review:true)", raw.Action)
	}
	session, err := getReviewSession(raw.SessionID)
	if err != nil {
		return "", err
	}
	switch raw.Action {
	case "continue":
		return session.continueUnit(ctx, raw.Guidance)
	case "rollback":
		return session.rollbackUnit(ctx, raw.Guidance)
	case "review_diff":
		return session.reviewDiff()
	case "fork":
		return session.forkVariants(ctx, raw.PromptA, raw.PromptB)
	case "choose":
		return session.chooseVariant(raw.Winner)
	case "accept":
		return session.accept()
	case "abort":
		restore := true
		if raw.Restore != nil {
			restore = *raw.Restore
		}
		return session.abort(restore)
	default:
		return "", fmt.Errorf("supervisor: unknown review action %q", raw.Action)
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

func executeInject(mgr *shepherd.ScopeManager, scopeID, guidance string) (string, error) {
	scope, ok := mgr.Get(scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", scopeID)
	}
	if err := scope.Inject(guidance); err != nil {
		return "", fmt.Errorf("supervisor: inject failed: %w", err)
	}
	return fmt.Sprintf("Injected guidance into %s.", scopeID), nil
}

func executeHalt(mgr *shepherd.ScopeManager, scopeID string) (string, error) {
	scope, ok := mgr.Get(scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", scopeID)
	}
	if err := scope.Halt(); err != nil {
		return "", fmt.Errorf("supervisor: halt failed: %w", err)
	}
	return fmt.Sprintf("Halted %s.", scopeID), nil
}

func executeStatus(mgr *shepherd.ScopeManager) (string, error) {
	active := mgr.ActiveScopes()
	all := mgr.AllScopes()
	return fmt.Sprintf("Supervision status:\n  Scopes: %d active, %d total\n", len(active), len(all)), nil
}
