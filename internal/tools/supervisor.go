package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// SupervisorTool lets the orchestrator supervise sub-agent execution.
// It exposes scope management (fork/merge/discard) and intervention
// operations (inject/halt) on running sub-agents.
//
// The tool opens the trace store directly and creates a scope manager
// on each call. This is simple and correct — the scope manager reads
// existing scopes from the trace store.
type SupervisorTool struct {
	TraceDir string
}

func (*SupervisorTool) Name() string { return "supervisor" }
func (*SupervisorTool) Description() string {
	return "Supervise sub-agent execution: list scopes, fork/merge/discard branches, inject guidance, halt agents"
}

func (*SupervisorTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list_scopes", "fork", "merge", "discard", "inject", "halt"],
				"description": "list_scopes: show all scopes; fork: create a branch; merge: merge child back; discard: abandon child; inject: push guidance; halt: force-stop"
			},
			"scope_id": {
				"type": "string",
				"description": "Scope ID (required for fork, merge, discard, inject, halt)"
			},
			"child_owner_id": {
				"type": "string",
				"description": "Trace owner ID for the new child scope (required for fork)"
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
		Action       string `json:"action"`
		ScopeID      string `json:"scope_id"`
		ChildOwnerID string `json:"child_owner_id"`
		Guidance     string `json:"guidance"`
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
		return t.executeListScopes(mgr)
	case "fork":
		if raw.ScopeID == "" || raw.ChildOwnerID == "" {
			return "", fmt.Errorf("supervisor: scope_id and child_owner_id required for fork")
		}
		return t.executeFork(mgr, raw.ScopeID, raw.ChildOwnerID)
	case "merge":
		if raw.ScopeID == "" {
			return "", fmt.Errorf("supervisor: scope_id required for merge")
		}
		return t.executeMerge(mgr, raw.ScopeID)
	case "discard":
		if raw.ScopeID == "" {
			return "", fmt.Errorf("supervisor: scope_id required for discard")
		}
		return t.executeDiscard(mgr, raw.ScopeID)
	case "inject":
		if raw.ScopeID == "" || raw.Guidance == "" {
			return "", fmt.Errorf("supervisor: scope_id and guidance required for inject")
		}
		return t.executeInject(mgr, raw.ScopeID, raw.Guidance)
	case "halt":
		if raw.ScopeID == "" {
			return "", fmt.Errorf("supervisor: scope_id required for halt")
		}
		return t.executeHalt(mgr, raw.ScopeID)
	default:
		return "", fmt.Errorf("supervisor: unknown action %q (valid: list_scopes, fork, merge, discard, inject, halt)", raw.Action)
	}
}

func (t *SupervisorTool) executeListScopes(mgr *shepherd.ScopeManager) (string, error) {
	all := mgr.AllScopes()
	if len(all) == 0 {
		return "No scopes registered.", nil
	}

	var sb strings.Builder
	sb.WriteString("Scopes:\n")
	for _, s := range all {
		parentID := "(root)"
		if s.Parent() != nil {
			parentID = s.Parent().ID()
		}
		snapInfo := ""
		if s.Snapshot() != nil {
			snapInfo = " [has snapshot]"
		}
		fmt.Fprintf(&sb, "  %s  owner=%s  state=%s  parent=%s%s\n",
			s.ID(), s.OwnerID(), s.State(), parentID, snapInfo)
	}
	return sb.String(), nil
}

func (t *SupervisorTool) executeFork(mgr *shepherd.ScopeManager, scopeID, childOwnerID string) (string, error) {
	child, err := mgr.Fork(scopeID, childOwnerID, nil)
	if err != nil {
		return "", fmt.Errorf("supervisor: fork failed: %w", err)
	}
	return fmt.Sprintf("Forked %s → %s (owner=%s)", scopeID, child.ID(), child.OwnerID()), nil
}

func (t *SupervisorTool) executeMerge(mgr *shepherd.ScopeManager, scopeID string) (string, error) {
	if err := mgr.Merge(scopeID); err != nil {
		return "", fmt.Errorf("supervisor: merge failed: %w", err)
	}
	return fmt.Sprintf("Merged %s into parent.", scopeID), nil
}

func (t *SupervisorTool) executeDiscard(mgr *shepherd.ScopeManager, scopeID string) (string, error) {
	if err := mgr.Discard(scopeID); err != nil {
		return "", fmt.Errorf("supervisor: discard failed: %w", err)
	}
	return fmt.Sprintf("Discarded %s.", scopeID), nil
}

func (t *SupervisorTool) executeInject(mgr *shepherd.ScopeManager, scopeID, guidance string) (string, error) {
	scope, ok := mgr.Get(scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", scopeID)
	}
	if err := scope.Inject(guidance); err != nil {
		return "", fmt.Errorf("supervisor: inject failed: %w", err)
	}
	return fmt.Sprintf("Injected guidance into %s: %s", scopeID, truncate(guidance, 100)), nil
}

func (t *SupervisorTool) executeHalt(mgr *shepherd.ScopeManager, scopeID string) (string, error) {
	scope, ok := mgr.Get(scopeID)
	if !ok {
		return "", fmt.Errorf("supervisor: scope %s not found", scopeID)
	}
	if err := scope.Halt(); err != nil {
		return "", fmt.Errorf("supervisor: halt failed: %w", err)
	}
	return fmt.Sprintf("Halted %s.", scopeID), nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
