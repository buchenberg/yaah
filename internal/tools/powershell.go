package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// PowerShellTool runs a PowerShell command and returns its stdout.
// It tries pwsh (PowerShell 7+, cross-platform) first, then falls back
// to powershell (Windows PowerShell 5.1).
type PowerShellTool struct{}

func (t *PowerShellTool) Name() string { return "powershell" }
func (t *PowerShellTool) Description() string {
	return "Executes a PowerShell command and returns its output."
}

func (t *PowerShellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The PowerShell command to execute"},
			"timeout": {"type": "integer", "description": "Timeout in seconds (default 30)"}
		},
		"required": ["command"]
	}`)
}

func (t *PowerShellTool) IsDangerous(argsJSON string) bool { return true }

// psExecutable returns the best available PowerShell executable.
func psExecutable() string {
	if _, err := exec.LookPath("pwsh"); err == nil {
		return "pwsh"
	}
	return "powershell"
}

func (t *PowerShellTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("powershell: invalid arguments: %w", err)
	}
	if params.Command == "" {
		return "", fmt.Errorf("powershell: command is required")
	}

	if isDangerous(params.Command) {
		return "", fmt.Errorf("powershell: command matches a dangerous pattern; refused")
	}

	timeout := bashDefaultTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	exe := psExecutable()
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", params.Command)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("powershell: timed out after %s", timeout)
	}
	output = truncateOutput(output)
	if err != nil {
		return "", fmt.Errorf("powershell: %w\n%s", err, string(output))
	}
	return string(output), nil
}
