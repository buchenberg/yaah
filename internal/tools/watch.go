package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/wake"
)

// WatchTool arms a predicate that outlives the turn. The session goes idle
// normally; when the predicate fires the harness injects a wake message with
// the predicate's evidence, so a long CI/review wait needs no polling turns.
type WatchTool struct {
	Manager *wake.Manager
}

func (*WatchTool) Name() string        { return "watch" }
func (*WatchTool) Description() string { return prompts.ToolDescription("watch") }

func (*WatchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["arm", "list", "cancel"],
				"description": "arm a new watch, list the session's watches, or cancel one by id"
			},
			"on": {
				"type": "string",
				"description": "Predicate to arm: job:<id>, process:<id>, time:<duration>, command:<cmd>, http:<url>, or file:<glob>"
			},
			"until": {
				"type": "string",
				"description": "Optional condition, same vocabulary as wait_for: command exit_zero/exit:<n>/regex:<re>/contains:<text>; http status:<code>/status:<n>xx/regex:<re>/contains:<text>; file exists/contains:<text>/regex:<re>"
			},
			"id": {
				"type": "string",
				"description": "Optional stable watch id for arm, or the id to cancel (required for cancel)"
			},
			"expires_in_seconds": {
				"type": "integer",
				"minimum": 1,
				"maximum": 604800,
				"description": "How long the watch stays armed before expiring (default 86400, max 604800 = 7 days)"
			},
			"interval_seconds": {
				"type": "integer",
				"minimum": 1,
				"maximum": 60,
				"description": "Initial poll interval (default is source-specific; backs off to at most 30s)"
			}
		},
		"required": ["action"]
	}`)
}

// IsDangerous gates a command watch at arm time. The command runs later,
// outside the turn, so the approval the user gives here covers every poll.
func (t *WatchTool) IsDangerous(argsJSON string) bool {
	var p struct {
		Action string `json:"action"`
		On     string `json:"on"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return true
	}
	return p.Action == "arm" && wake.Spec{On: p.On}.SourceOf() == wake.SourceCommand
}

func (t *WatchTool) Execute(ctx context.Context, args string) (string, error) {
	if t.Manager == nil {
		return "", fmt.Errorf("watch: the wake engine is not configured in this session")
	}
	var params struct {
		Action           string `json:"action"`
		On               string `json:"on"`
		Until            string `json:"until"`
		ID               string `json:"id"`
		ExpiresInSeconds int    `json:"expires_in_seconds"`
		IntervalSeconds  int    `json:"interval_seconds"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("watch: invalid arguments: %w", err)
	}

	switch params.Action {
	case "arm":
		if params.On == "" {
			return "", fmt.Errorf("watch: on is required for arm")
		}
		info, err := t.Manager.Arm(wake.ArmRequest{
			On:        params.On,
			Until:     params.Until,
			ID:        params.ID,
			ExpiresIn: clampSeconds(params.ExpiresInSeconds, 0),
			Interval:  clampSeconds(params.IntervalSeconds, 0),
		})
		if err != nil {
			return "", fmt.Errorf("watch: %w", err)
		}
		data, err := json.Marshal(info)
		if err != nil {
			return "", fmt.Errorf("watch: encode watch: %w", err)
		}
		return string(data), nil

	case "list":
		list := t.Manager.List()
		armed := 0
		for _, w := range list {
			if w.State == wake.StateArmed {
				armed++
			}
		}
		data, err := json.Marshal(struct {
			Watches []wake.WatchInfo `json:"watches"`
			Armed   int              `json:"armed"`
		}{Watches: list, Armed: armed})
		if err != nil {
			return "", fmt.Errorf("watch: encode list: %w", err)
		}
		return string(data), nil

	case "cancel":
		if params.ID == "" {
			return "", fmt.Errorf("watch: id is required for cancel")
		}
		cancelled := t.Manager.Cancel(params.ID)
		data, err := json.Marshal(struct {
			ID        string `json:"id"`
			Cancelled bool   `json:"cancelled"`
		}{ID: params.ID, Cancelled: cancelled})
		if err != nil {
			return "", fmt.Errorf("watch: encode cancel: %w", err)
		}
		return string(data), nil

	default:
		return "", fmt.Errorf("watch: unknown action %q (valid: arm, list, cancel)", params.Action)
	}
}
