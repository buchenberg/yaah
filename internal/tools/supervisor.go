package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// SupervisorTool lets the orchestrator supervise sub-agent execution.
// It exposes intervention operations (inject/halt) on running sub-agents
// and scope inspection (list_scopes, status).
//
// Fork/merge/discard are NOT exposed because yaah sub-agents execute
// on the real filesystem without sandbox isolation. Those operations
// exist in shepherd-kernel-go for when sandboxed execution is added.
//
// The tool opens the trace store directly and creates a scope manager
// on each call.
type SupervisorTool struct {
	TraceDir string
}

func (*SupervisorTool) Name() string { return "supervisor" }
func (*SupervisorTool) Description() string {
	return "Supervise sub-agent execution: list scopes, inject guidance, halt agents"
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

func (t *SupervisorTool) Execute(_ context.Context, args string) (string, error) {
	if t.TraceDir == "" {
		return "", fmt.Errorf("supervisor: shepherd tracing not enabled (set shepherd_trace_dir in config)")
	}

	var raw struct {
		Action   string `json:"action"`
		ScopeID  string `json:"scope_id"`
		Guidance string `json:"guidance"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("supervisor: invalid arguments: %w", err)
	}

	store, err := shepherd.NewSQLiteTraceStore(filepath.Join(t.TraceDir, "trace.sqlite"))
	if err != nil {
		return "", fmt.Errorf("supervisor: cannot open trace store: %w", err)
	}
	defer store.Close()

	mgr := shepherd.NewScopeManager(store)

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
