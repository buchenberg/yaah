package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// BashTool runs a shell command and returns its stdout.
type BashTool struct{}

func (t *BashTool) Name() string        { return "bash" }
func (t *BashTool) Description() string { return "Executes a shell command and returns its output." }

func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The shell command to execute"},
			"timeout": {"type": "integer", "description": "Timeout in seconds (default 30)"}
		},
		"required": ["command"]
	}`)
}

func (t *BashTool) IsDangerous(argsJSON string) bool { return true }

func (t *BashTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("bash: invalid arguments: %w", err)
	}
	if params.Command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	// Best-effort dangerous-command guard (NOT a security boundary).
	if isDangerous(params.Command) {
		return "", fmt.Errorf("bash: command matches a dangerous pattern; refused (enable approval gating for real protection)")
	}

	timeout := bashDefaultTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", params.Command)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("bash: timed out after %s", timeout)
	}
	output = truncateOutput(output)
	if err != nil {
		return "", fmt.Errorf("bash: %w\n%s", err, string(output))
	}
	return string(output), nil
}
