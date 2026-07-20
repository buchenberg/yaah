package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/buchenberg/yaah/internal/plans"
)

// PlanTool creates, lists, shows, approves, edits, and deletes plans stored
// as PLAN.md files in .agents/plans/ directories. Plans use the same YAML
// frontmatter + markdown convention as skills, with a "status" field
// (draft → approved → in_progress → completed) to track workflow.
type PlanTool struct {
	Dirs []string
}

func (t *PlanTool) Name() string { return "plan" }
func (t *PlanTool) Description() string {
	return "Create, review, and manage plans stored as PLAN.md files in .agents/plans/. Plans have a status workflow: draft → approved → in_progress → completed."
}

func (t *PlanTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["create", "list", "show", "approve", "edit", "delete"], "description": "Action to perform"},
			"name": {"type": "string", "description": "Plan name (required for create, show, approve, edit, delete)"},
			"description": {"type": "string", "description": "One-line plan description (required for create, optional for edit)"},
			"body": {"type": "string", "description": "Plan body in markdown — steps, details, notes (required for create, optional for edit)"},
			"status": {"type": "string", "enum": ["draft", "approved", "in_progress", "completed", "cancelled"], "description": "New status (optional for edit)"}
		},
		"required": ["action"]
	}`)
}

func (t *PlanTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action      string `json:"action"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("plan: invalid arguments: %w", err)
	}

	switch params.Action {
	case "create":
		return t.create(params)
	case "list":
		return t.list()
	case "show":
		return t.show(params)
	case "approve":
		return t.approve(params)
	case "edit":
		return t.edit(params)
	case "delete":
		return t.delete(params)
	default:
		return "", fmt.Errorf("plan: unknown action %q (valid: create, list, show, approve, edit, delete)", params.Action)
	}
}

// IsDangerous returns true for delete (removes a directory). Creating and
// editing plans write to .agents/plans/ but are not destructive.
func (t *PlanTool) IsDangerous(argsJSON string) bool {
	var params struct {
		Action string `json:"action"`
	}
	json.Unmarshal([]byte(argsJSON), &params)
	return params.Action == "delete"
}

func (t *PlanTool) create(params struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Status      string `json:"status"`
}) (string, error) {
	if params.Name == "" {
		return "", fmt.Errorf("plan: name is required for create")
	}
	if params.Description == "" {
		return "", fmt.Errorf("plan: description is required for create")
	}
	if params.Body == "" {
		return "", fmt.Errorf("plan: body is required for create")
	}
	if len(t.Dirs) == 0 {
		return "", fmt.Errorf("plan: no search paths configured")
	}
	path, err := plans.Create(t.Dirs[0], params.Name, params.Description, params.Body)
	if err != nil {
		return "", fmt.Errorf("plan create: %w", err)
	}
	return fmt.Sprintf("Plan %q created at %s (status: draft)", params.Name, path), nil
}

func (t *PlanTool) list() (string, error) {
	found := plans.Discover(t.Dirs)
	if len(found) == 0 {
		return "No plans found.", nil
	}
	out := fmt.Sprintf("Found %d plan(s):\n", len(found))
	for _, p := range found {
		out += fmt.Sprintf("  - %s [%s]: %s\n", p.Name, p.Status, p.Description)
	}
	return out, nil
}

func (t *PlanTool) show(params struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Status      string `json:"status"`
}) (string, error) {
	if params.Name == "" {
		return "", fmt.Errorf("plan: name is required for show")
	}
	p := plans.FindPlan(t.Dirs, params.Name)
	if p == nil {
		return "", fmt.Errorf("plan %q not found", params.Name)
	}
	return plans.FormatPlanForAgent(p), nil
}

func (t *PlanTool) approve(params struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Status      string `json:"status"`
}) (string, error) {
	if params.Name == "" {
		return "", fmt.Errorf("plan: name is required for approve")
	}
	p := plans.FindPlan(t.Dirs, params.Name)
	if p == nil {
		return "", fmt.Errorf("plan %q not found", params.Name)
	}
	if p.Status == "approved" {
		return fmt.Sprintf("Plan %q is already approved.", params.Name), nil
	}
	path, err := plans.SetStatus(p, "approved")
	if err != nil {
		return "", fmt.Errorf("plan approve: %w", err)
	}
	return fmt.Sprintf("Plan %q approved at %s. You may now implement the steps.", params.Name, path), nil
}

func (t *PlanTool) edit(params struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Status      string `json:"status"`
}) (string, error) {
	if params.Name == "" {
		return "", fmt.Errorf("plan: name is required for edit")
	}
	if params.Description == "" && params.Body == "" && params.Status == "" {
		return "", fmt.Errorf("plan: at least one of description, body, or status is required for edit")
	}
	if params.Status != "" && !slices.Contains(plans.ValidStatuses, params.Status) {
		return "", fmt.Errorf("plan: invalid status %q (valid: %v)", params.Status, plans.ValidStatuses)
	}
	p := plans.FindPlan(t.Dirs, params.Name)
	if p == nil {
		return "", fmt.Errorf("plan %q not found", params.Name)
	}
	path, err := plans.Edit(p, params.Description, params.Status, params.Body)
	if err != nil {
		return "", fmt.Errorf("plan edit: %w", err)
	}
	fields := []string{}
	if params.Description != "" {
		fields = append(fields, "description")
	}
	if params.Body != "" {
		fields = append(fields, "body")
	}
	if params.Status != "" {
		fields = append(fields, fmt.Sprintf("status → %s", params.Status))
	}
	return fmt.Sprintf("Plan %q updated (%s) at %s", params.Name, joinFields(fields), path), nil
}

func (t *PlanTool) delete(params struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Status      string `json:"status"`
}) (string, error) {
	if params.Name == "" {
		return "", fmt.Errorf("plan: name is required for delete")
	}
	p := plans.FindPlan(t.Dirs, params.Name)
	if p == nil {
		return "", fmt.Errorf("plan %q not found", params.Name)
	}
	if err := plans.Delete(p); err != nil {
		return "", fmt.Errorf("plan delete: %w", err)
	}
	return fmt.Sprintf("Plan %q deleted (directory %s removed).", params.Name, p.Dir), nil
}

func joinFields(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	if len(fields) == 1 {
		return fields[0]
	}
	if len(fields) == 2 {
		return fields[0] + " and " + fields[1]
	}
	out := ""
	for i, f := range fields {
		if i == len(fields)-1 {
			out += "and " + f
		} else {
			out += f + ", "
		}
	}
	return out
}
