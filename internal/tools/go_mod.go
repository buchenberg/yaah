package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// GoModTool performs Go module operations (tidy, verify, list, graph, etc.).
type GoModTool struct{}

func NewGoModTool() *GoModTool { return &GoModTool{} }

func (t *GoModTool) Name() string        { return "go_mod" }
func (t *GoModTool) Description() string { return prompts.ToolDescription("go_mod") }

func (t *GoModTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["tidy", "verify", "graph", "why", "list", "add", "remove", "upgrade"],
				"description": "The module operation to perform"
			},
			"module": {
				"type": "string",
				"description": "Module path (required for add, remove, upgrade, why)"
			},
			"flags": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Extra flags to pass to the go command"
			},
			"dry_run": {
				"type": "boolean",
				"description": "If true, show what would be done without modifying go.mod (applies to add, remove, upgrade, tidy)"
			}
		},
		"required": ["action"]
	}`)
}

type goModParams struct {
	Action string   `json:"action"`
	Module string   `json:"module"`
	Flags  []string `json:"flags"`
	DryRun bool     `json:"dry_run"`
}

type goModModule struct {
	Path      string `json:"path"`
	Version   string `json:"version"`
	Indirect  bool   `json:"indirect"`
	Main      bool   `json:"main"`
	Dir       string `json:"dir,omitempty"`
	GoVersion string `json:"go_version,omitempty"`
}

type goModResult struct {
	Action  string        `json:"action"`
	Success bool          `json:"success"`
	Output  string        `json:"output"`
	Stderr  string        `json:"stderr"`
	Modules []goModModule `json:"modules,omitempty"`
	Summary string        `json:"summary,omitempty"`
}

func (t *GoModTool) Execute(ctx context.Context, args string) (string, error) {
	var params goModParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("go_mod: invalid arguments: %w", err)
	}

	result := &goModResult{Action: params.Action}

	switch params.Action {
	case "tidy":
		t.runTidy(ctx, params, result)
	case "verify":
		t.runVerify(ctx, params, result)
	case "graph":
		t.runGraph(ctx, params, result)
	case "why":
		t.runWhy(ctx, params, result)
	case "list":
		t.runList(ctx, params, result)
	case "add":
		t.runAdd(ctx, params, result)
	case "remove":
		t.runRemove(ctx, params, result)
	case "upgrade":
		t.runUpgrade(ctx, params, result)
	default:
		return "", fmt.Errorf("go_mod: unknown action %q", params.Action)
	}

	if params.DryRun {
		result.Summary = "dry_run: no changes were made to go.mod"
	}

	outBytes, _ := json.MarshalIndent(result, "", "  ")
	return string(outBytes), nil
}

func (t *GoModTool) runTidy(ctx context.Context, params goModParams, result *goModResult) {
	cmdArgs := []string{"mod", "tidy", "-v"}
	if params.DryRun {
		cmdArgs = append(cmdArgs, "-diff")
	}
	cmdArgs = append(cmdArgs, params.Flags...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil
	if !result.Success {
		result.Stderr = err.Error()
	}
}

func (t *GoModTool) runVerify(ctx context.Context, params goModParams, result *goModResult) {
	cmdArgs := append([]string{"mod", "verify"}, params.Flags...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil

	// Parse: each line is "module-path version" (success) or "module-path version: error"
	for _, line := range strings.Split(result.Output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "all modules verified") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			m := goModModule{Path: parts[0], Version: strings.TrimSuffix(parts[1], ":")}
			result.Modules = append(result.Modules, m)
		}
	}
}

func (t *GoModTool) runGraph(ctx context.Context, params goModParams, result *goModResult) {
	cmdArgs := append([]string{"mod", "graph"}, params.Flags...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil

	// Parse: "module@version dependency@version"
	for _, line := range strings.Split(result.Output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		result.Modules = append(result.Modules, goModModule{Path: line})
	}
}

func (t *GoModTool) runWhy(ctx context.Context, params goModParams, result *goModResult) {
	if params.Module == "" {
		result.Success = false
		result.Stderr = "module is required for 'why' action"
		return
	}
	cmdArgs := append([]string{"mod", "why", "-m", "--", params.Module}, params.Flags...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil
	result.Summary = result.Output
}

func (t *GoModTool) runList(ctx context.Context, params goModParams, result *goModResult) {
	cmdArgs := append([]string{"list", "-m", "-json", "all"}, params.Flags...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil

	// go list -json all outputs concatenated JSON objects, not an array.
	raw := string(out)
	var modules []goModModule

	decoder := json.NewDecoder(strings.NewReader(raw))
	const maxModules = 200
	for decoder.More() && len(modules) < maxModules {
		var m goModModule
		if err := decoder.Decode(&m); err == nil {
			modules = append(modules, m)
		}
	}
	if len(modules) == maxModules && decoder.More() {
		result.Summary = fmt.Sprintf("showing first %d modules (truncated)", maxModules)
	}
	result.Modules = modules
}

func (t *GoModTool) runAdd(ctx context.Context, params goModParams, result *goModResult) {
	if params.Module == "" {
		result.Success = false
		result.Stderr = "module is required for 'add' action"
		return
	}
	if params.DryRun {
		result.Success = true
		result.Output = fmt.Sprintf("would run: go get %s@latest", params.Module)
		return
	}
	mod := params.Module
	if !strings.Contains(mod, "@") {
		mod = mod + "@latest"
	}
	cmdArgs := append([]string{"get", "--", mod}, params.Flags...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil
}

func (t *GoModTool) runRemove(ctx context.Context, params goModParams, result *goModResult) {
	if params.Module == "" {
		result.Success = false
		result.Stderr = "module is required for 'remove' action"
		return
	}
	if params.DryRun {
		result.Success = true
		result.Output = fmt.Sprintf("would run: go get %s@none && go mod tidy", params.Module)
		return
	}
	cmdArgs := append([]string{"get", "--", params.Module + "@none"}, params.Flags...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil

	// Follow up with tidy
	tidyArgs := []string{"mod", "tidy"}
	tidyCmd := exec.CommandContext(ctx, "go", tidyArgs...)
	tidyOut, tidyErr := tidyCmd.CombinedOutput()
	if tidyErr != nil {
		result.Stderr += fmt.Sprintf("\ntidy after remove: %s", string(tidyOut))
	}
}

func (t *GoModTool) runUpgrade(ctx context.Context, params goModParams, result *goModResult) {
	if params.DryRun {
		target := "./..."
		if params.Module != "" {
			target = params.Module
		}
		result.Success = true
		result.Output = fmt.Sprintf("would run: go get -u %s && go mod tidy", target)
		return
	}
	var cmdArgs []string
	if params.Module != "" {
		cmdArgs = append([]string{"get", "-u", "--", params.Module}, params.Flags...)
	} else {
		cmdArgs = append([]string{"get", "-u", "./..."}, params.Flags...)
	}
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	result.Output = string(out)
	result.Success = err == nil

	// Follow up with tidy
	tidyArgs := []string{"mod", "tidy"}
	tidyCmd := exec.CommandContext(ctx, "go", tidyArgs...)
	tidyOut, tidyErr := tidyCmd.CombinedOutput()
	if tidyErr != nil {
		result.Stderr += fmt.Sprintf("\ntidy after upgrade: %s", string(tidyOut))
	}
}
