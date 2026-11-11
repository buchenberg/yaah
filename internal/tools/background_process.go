package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/prompts"
)

// BackgroundProcessTool manages long-running background processes.
type BackgroundProcessTool struct {
	Manager *process.Manager
}

func (t *BackgroundProcessTool) Name() string { return "background_process" }
func (t *BackgroundProcessTool) Description() string {
	return prompts.ToolDescription("background_process")
}

// IsDangerous classifies the command-running actions. `start` and `restart`
// execute an arbitrary shell command, exactly like `bash`, so they must reach
// the approval gate; `list`, `status`, `logs` and `stop` only read or signal an
// existing process and stay ungated. Unknown or missing actions fail closed
// (dangerous) even though Execute rejects them, so a malformed call can never
// slip past the gate on its way to an error.
func (t *BackgroundProcessTool) IsDangerous(argsJSON string) bool {
	var params struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
		return true
	}
	switch params.Action {
	case "list", "status", "logs", "stop":
		return false
	default:
		return true
	}
}

func (t *BackgroundProcessTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["start", "list", "status", "logs", "stop", "restart"], "description": "The action to perform"},
			"command": {"type": "string", "description": "Shell command to run (required for start)"},
			"description": {"type": "string", "description": "Short description of the process (for start)"},
			"id": {"type": "string", "description": "Process ID (required for status, logs, stop, restart)"}
		},
		"required": ["action"]
	}`)
}

func (t *BackgroundProcessTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action      string `json:"action"`
		Command     string `json:"command"`
		Description string `json:"description"`
		ID          string `json:"id"`
	}
	if err := StrictToolArgs("background_process", t.Schema(), args, &params); err != nil {
		return "", err
	}

	switch params.Action {
	case "start":
		if params.Command == "" {
			return "", fmt.Errorf("background_process: command is required for start")
		}
		// Same best-effort dangerous-pattern guard as bash (NOT a security
		// boundary). Approval gating is the real control; this catches the
		// blatant commands even under approval: allow.
		if isDangerous(params.Command) {
			return "", fmt.Errorf("background_process: command matches a dangerous pattern; refused (enable approval gating for real protection)")
		}
		info, err := t.Manager.Start(params.Command, params.Description)
		if err != nil {
			return "", fmt.Errorf("background_process: start %q: %w", params.Command, err)
		}
		return fmt.Sprintf("Started process %s: %s", info.ID, info.Command), nil

	case "list":
		procs := t.Manager.List()
		if len(procs) == 0 {
			return "No background processes running.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%d background process(es):\n", len(procs)))
		for _, p := range procs {
			sb.WriteString(fmt.Sprintf("  [%s] %s — %s\n", p.ID, p.Status(), truncateCmd(p.Command, 60)))
		}
		return sb.String(), nil

	case "status":
		if params.ID == "" {
			return "", fmt.Errorf("background_process: id is required for status")
		}
		info := t.Manager.Get(params.ID)
		if info == nil {
			return "", fmt.Errorf("background_process: process %s not found", params.ID)
		}
		return fmt.Sprintf("Process %s: %s — started at %s", info.ID, info.Status(), info.StartedAt.Format("15:04:05")), nil

	case "logs":
		if params.ID == "" {
			return "", fmt.Errorf("background_process: id is required for logs")
		}
		info := t.Manager.Get(params.ID)
		if info == nil {
			return "", fmt.Errorf("background_process: process %s not found", params.ID)
		}
		logs := info.Logs()
		if logs == "" {
			return "(no output yet)", nil
		}
		return logs, nil
	case "stop":
		if params.ID == "" {
			return "", fmt.Errorf("background_process: id is required for stop")
		}
		if err := t.Manager.Stop(params.ID); err != nil {
			return "", fmt.Errorf("background_process: %w", err)
		}
		return fmt.Sprintf("Process %s stopped.", params.ID), nil

	case "restart":
		if params.ID == "" {
			return "", fmt.Errorf("background_process: id is required for restart")
		}
		info := t.Manager.Get(params.ID)
		if info == nil {
			return "", fmt.Errorf("background_process: process %s not found", params.ID)
		}
		// Check the stored command BEFORE stopping anything: a refused
		// restart must leave the running process untouched, and the fresh
		// approval decision must cover the command that will actually run.
		if isDangerous(info.Command) {
			return "", fmt.Errorf("background_process: stored command matches a dangerous pattern; refused (start a new process with an approved command instead)")
		}
		if info.Status() == "running" {
			if err := t.Manager.Stop(params.ID); err != nil {
				return "", fmt.Errorf("background_process: restart stop: %w", err)
			}
		}
		newInfo, err := t.Manager.Start(info.Command, info.Description)
		if err != nil {
			return "", fmt.Errorf("background_process: restart start: %w", err)
		}
		return fmt.Sprintf("Process restarted as %s: %s", newInfo.ID, newInfo.Command), nil

	default:
		return "", fmt.Errorf("background_process: unknown action %q", params.Action)
	}
}

func truncateCmd(cmd string, maxLen int) string {
	runes := []rune(cmd)
	if len(runes) <= maxLen {
		return cmd
	}
	return string(runes[:maxLen-3]) + "..."
}
