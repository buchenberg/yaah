package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
)

// BashTool runs a shell command and returns its stdout.
type BashTool struct{}

func (t *BashTool) Name() string        { return "bash" }
func (t *BashTool) Description() string { return prompts.ToolDescription("bash") }

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

	shell, shellArg := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, shellArg = "pwsh", "-Command"
		if _, err := exec.LookPath("pwsh"); err != nil {
			shell, shellArg = "powershell", "-Command"
		}
	}
	cmd := exec.CommandContext(ctx, shell, shellArg, params.Command)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%w: bash timed out after %s", ErrToolTimeout, timeout)
	}
	output = truncateOutput(output)
	if err != nil {
		return "", fmt.Errorf("bash: %w\n%s", err, string(output))
	}
	return string(output), nil
}
