package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// SubagentTraceTool lets the orchestrator inspect a sub-agent's execution
// trace during a run, so it can nudge stuck sub-agents, learn from past
// successes, or craft precise retry prompts from failures.
type SubagentTraceTool struct {
	TraceDir string
}

func (*SubagentTraceTool) Name() string        { return "subagent_trace" }
func (*SubagentTraceTool) Description() string { return "" }

func (*SubagentTraceTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "profile"],
				"description": "list: show all recent sub-agent trace sessions with fact counts; profile: show execution details for one session_id"
			},
			"session_id": {
				"type": "string",
				"description": "Trace owner session ID (required for profile, ignored for list). Use IDs from the list action."
			}
		},
		"required": ["action"]
	}`)
}

func (t *SubagentTraceTool) Execute(_ context.Context, args string) (string, error) {
	if t.TraceDir == "" {
		return "", fmt.Errorf("subagent_trace: shepherd tracing is not enabled (set shepherd_trace_dir in config)")
	}

	var raw struct {
		Action    string `json:"action"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("subagent_trace: invalid arguments: %w", err)
	}

	tracePath := filepath.Join(t.TraceDir, "trace.sqlite")
	store, err := shepherd.NewSQLiteTraceStore(tracePath)
	if err != nil {
		return "", fmt.Errorf("subagent_trace: cannot open trace store: %w", err)
	}
	defer store.Close()

	switch raw.Action {
	case "list":
		return t.executeList(store)
	case "profile":
		if raw.SessionID == "" {
			return "", fmt.Errorf("subagent_trace: session_id is required for profile")
		}
		return t.executeProfile(store, raw.SessionID)
	default:
		return "", fmt.Errorf("subagent_trace: unknown action %q (valid: list, profile)", raw.Action)
	}
}

func (t *SubagentTraceTool) executeList(store *shepherd.SQLiteTraceStore) (string, error) {
	slice, err := store.ReadOwnerPrefix(shepherd.TrustedReadContext, "", 99999, "declarations_only")
	if err != nil {
		return "", fmt.Errorf("subagent_trace: read store: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("Sub-agent trace sessions:\n")

	found := false
	for owner, paths := range slice.OwnerPaths {
		if !strings.HasPrefix(owner, "sub-") {
			continue
		}
		if !found {
			found = true
		}
		sb.WriteString(fmt.Sprintf("  %s  (%d facts)\n", owner, len(paths)))
	}
	if !found {
		sb.WriteString("  (no sub-agent sessions found)\n")
	}
	return sb.String(), nil
}

func (t *SubagentTraceTool) executeProfile(store *shepherd.SQLiteTraceStore, sessionID string) (string, error) {
	slice, err := store.ReadOwnerPrefix(shepherd.TrustedReadContext, sessionID, 99999, "both")
	if err != nil {
		return "", fmt.Errorf("subagent_trace: read session: %w", err)
	}
	if len(slice.FactIDs()) == 0 {
		return "", fmt.Errorf("subagent_trace: no facts found for session %q", sessionID)
	}

	captureByParent := make(map[string]struct{ success bool; errMsg string })
	for _, factID := range slice.FactIDs() {
		fact := slice.FactsByID[factID]
		if fact.GetEnvelope().Mode != shepherd.Capture {
			continue
		}
		causedBy := fact.GetEnvelope().CausedByIDs
		if len(causedBy) == 0 {
			continue
		}
		rec, ok := fact.(shepherd.Record)
		if !ok {
			continue
		}
		success, _ := rec.Body.Payload["success"].(bool)
		errMsg := ""
		if e, ok2 := rec.Body.Payload["error"].(string); ok2 {
			errMsg = e
		}
		captureByParent[causedBy[0]] = struct{ success bool; errMsg string }{success, errMsg}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Sub-agent trace: %s\n", sessionID)

	var total, okCount int
	seq := 0
	for _, factID := range slice.FactIDs() {
		fact := slice.FactsByID[factID]
		if fact.GetEnvelope().Mode != shepherd.Declaration {
			continue
		}

		kind := fact.GetView().KindLabel
		if kind == "turn:created" || kind == "turn:started" || kind == "turn:completed" || kind == "turn:failed" {
			continue
		}

		seq++
		total++
		cap := captureByParent[factID]
		status := "ok"
		if cap.errMsg != "" {
			status = "error: " + cap.errMsg
		}
		if status == "ok" {
			okCount++
		}

		args := ""
		if rec, ok2 := fact.(shepherd.Record); ok2 {
			if a, ok3 := rec.Body.Payload["args"]; ok3 {
				b, _ := json.Marshal(a)
				args = " " + truncateStr(string(b), 50)
			}
		}
		sb.WriteString(fmt.Sprintf("  T%d %s%s  %s\n", seq, kind, args, status))
	}

	rate := 0
	if total > 0 {
		rate = okCount * 100 / total
	}
	fmt.Fprintf(&sb, "\n%d tool calls, %d%% success\n", total, rate)
	return sb.String(), nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
