package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
)

// BashTool runs a shell command and returns its stdout.
//
// Execution goes through the workspace, so the same tool runs on the host during
// a normal session and inside an isolated substrate (a git worktree, or a
// container) when a supervised run asks for one. The workspace supplies the
// working directory and the shell; the tool supplies the command. Containment is
// NOT enforced for shell commands.
type BashTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*BashTool)(nil)
	_ WorkspaceSetter     = (*BashTool)(nil)
)

func (t *BashTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *BashTool) SetWorkspace(ws Workspace)          { t.WS = ws }

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

	// The workspace owns shell selection and the working directory; the tool
	// supplies only the command text.
	ws := workspaceOf(t.WS, t.PV)
	res, err := ws.Exec(ctx, shellCommand(ws, params.Command))
	if ctx.Err() == context.DeadlineExceeded {
		return "", ToolTimeoutError{Tool: "bash", Timeout: timeout.String()}
	}
	output := truncateOutput([]byte(res.Stdout))
	if err != nil {
		return "", fmt.Errorf("bash: %w\n%s", err, string(output))
	}
	return string(output), nil
}
