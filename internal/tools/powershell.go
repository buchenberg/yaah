package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
)

// PowerShellTool runs a PowerShell command and returns its stdout.
// It tries pwsh (PowerShell 7+, cross-platform) first, then falls back
// to powershell (Windows PowerShell 5.1).
//
// Execution goes through the workspace, so the command runs in the session's
// working directory. PowerShell is a host facility, so this tool refuses an
// isolated workspace rather than running the command elsewhere.
type PowerShellTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*PowerShellTool)(nil)
	_ WorkspaceSetter     = (*PowerShellTool)(nil)
)

func (t *PowerShellTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *PowerShellTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *PowerShellTool) Name() string { return "powershell" }
func (t *PowerShellTool) Description() string {
	return prompts.ToolDescription("powershell")
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

	ws := workspaceOf(t.WS, t.PV)
	// PowerShell is a host facility; there is no PowerShell inside a Linux
	// sandbox, so fail clearly rather than running the command somewhere else.
	if err := requireLocal(ws, "powershell"); err != nil {
		return "", err
	}

	res, err := ws.Exec(ctx, ExecRequest{
		Command: psExecutable(),
		Args:    []string{"-NoProfile", "-NonInteractive", "-Command", params.Command},
	})
	if ctx.Err() == context.DeadlineExceeded {
		return "", ToolTimeoutError{Tool: "powershell", Timeout: timeout.String()}
	}
	output := truncateOutput([]byte(res.Stdout))
	if err != nil {
		return "", fmt.Errorf("powershell: %w\n%s", err, string(output))
	}
	return string(output), nil
}
