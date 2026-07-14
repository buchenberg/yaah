package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// TaskRunner runs a sub-agent for a given prompt and returns the response.
type TaskRunner func(ctx context.Context, prompt string) (string, error)

// TaskTool spawns a sub-agent with a restricted tool set to complete a task.
type TaskTool struct {
	Runner TaskRunner
}

func (t *TaskTool) Name() string { return "task" }
func (t *TaskTool) Description() string {
	return "Launches a sub-agent with restricted tools to research or complete a subtask."
}

func (t *TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "3-5 word description of the subtask"},
			"prompt": {"type": "string", "description": "The task for the sub-agent to perform autonomously"}
		},
		"required": ["description", "prompt"]
	}`)
}

func (t *TaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("task: invalid arguments: %w", err)
	}
	if params.Prompt == "" {
		return "", fmt.Errorf("task: prompt is required")
	}

	if t.Runner == nil {
		return "", fmt.Errorf("task: sub-agent runner not configured")
	}

	return t.Runner(ctx, params.Prompt)
}
