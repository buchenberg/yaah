package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/wake"
)

// WaitForTool blocks inside the tool call until a predicate fires. Waiting
// therefore costs zero model turns: the turn stays open, but the model is not
// asked "is it done yet" every few seconds. For waits longer than the turn
// budget, use watch instead.
type WaitForTool struct {
	Manager *wake.Manager
}

func (*WaitForTool) Name() string        { return "wait_for" }
func (*WaitForTool) Description() string { return prompts.ToolDescription("wait_for") }

func (*WaitForTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"on": {
				"type": "string",
				"description": "The predicate to wait on: job:<id>, process:<id>, time:<duration>, command:<cmd>, http:<url>, or file:<glob>"
			},
			"until": {
				"type": "string",
				"description": "Optional condition. command: exit_zero (default), exit:<n>, regex:<re>, contains:<text>. http: status:<code>, status:<3xx>, regex:<re>, contains:<text> (default any 2xx). file: exists (default), contains:<text>, regex:<re>."
			},
			"timeout_seconds": {
				"type": "integer",
				"minimum": 1,
				"maximum": 86400,
				"description": "How long this call may block before returning a resumable handle (default 900, max 86400)"
			},
			"interval_seconds": {
				"type": "integer",
				"minimum": 1,
				"maximum": 60,
				"description": "Initial poll interval for polling predicates (default is source-specific; the interval backs off to at most 30s)"
			},
			"handle": {
				"type": "string",
				"description": "Resume a wait that timed out: pass the handle from the previous result instead of on/until"
			}
		},
		"required": []
	}`)
}

// IsDangerous gates command predicates: waiting on a command reruns it, so
// arming one is as consequential as bash. Every other source is read-only or
// observes an already-running entity.
func (t *WaitForTool) IsDangerous(argsJSON string) bool {
	var p struct {
		On     string `json:"on"`
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return true
	}
	on := p.On
	if on == "" && p.Handle != "" {
		// A handle-only resume must be classified from the predicate it
		// names. Otherwise a forged or replayed command handle would skip
		// the approval gate the original arm required.
		spec, err := wake.HandleSpec(p.Handle)
		if err != nil {
			return true
		}
		on = spec.On
	}
	return wake.Spec{On: on}.SourceOf() == wake.SourceCommand
}

func (t *WaitForTool) Execute(ctx context.Context, args string) (string, error) {
	if t.Manager == nil {
		return "", fmt.Errorf("wait_for: the wake engine is not configured in this session")
	}
	var params struct {
		On              string `json:"on"`
		Until           string `json:"until"`
		TimeoutSeconds  int    `json:"timeout_seconds"`
		IntervalSeconds int    `json:"interval_seconds"`
		Handle          string `json:"handle"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("wait_for: invalid arguments: %w", err)
	}
	if params.On == "" && params.Handle == "" {
		return "", fmt.Errorf("wait_for: on is required (or pass a handle from a timed-out wait)")
	}

	res, err := t.Manager.Wait(ctx, wake.WaitRequest{
		On:       params.On,
		Until:    params.Until,
		Timeout:  clampSeconds(params.TimeoutSeconds, wake.DefaultWaitTimeout),
		Interval: clampSeconds(params.IntervalSeconds, 0),
		Handle:   params.Handle,
	})
	if err != nil {
		return "", fmt.Errorf("wait_for: %w", err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("wait_for: encode result: %w", err)
	}
	return string(data), nil
}

// clampSeconds converts a positive integer number of seconds to a Duration,
// returning def when the value is zero or negative.
func clampSeconds(secs int, def time.Duration) time.Duration {
	if secs <= 0 {
		return def
	}
	return time.Duration(secs) * time.Second
}
