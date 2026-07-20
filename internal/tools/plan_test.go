package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPlanTool_Schema(t *testing.T) {
	tool := &PlanTool{}
	schema := tool.Schema()
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("invalid JSON schema: %v", err)
	}

	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}

	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatal("schema missing action property")
	}

	enum, ok := action["enum"].([]any)
	if !ok {
		t.Fatal("action missing enum")
	}

	actions := make(map[string]bool)
	for _, v := range enum {
		actions[v.(string)] = true
	}
	expected := []string{"create", "list", "show", "approve", "edit", "delete"}
	for _, e := range expected {
		if !actions[e] {
			t.Errorf("action enum missing %q", e)
		}
	}
}

func TestPlanTool_Create_Validation(t *testing.T) {
	ctx := context.Background()

	t.Run("missing name", func(t *testing.T) {
		tool := &PlanTool{Dirs: []string{t.TempDir()}}
		_, err := tool.Execute(ctx, `{"action":"create","description":"desc","body":"body"}`)
		if err == nil || err.Error() != "plan: name is required for create" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing description", func(t *testing.T) {
		tool := &PlanTool{Dirs: []string{t.TempDir()}}
		_, err := tool.Execute(ctx, `{"action":"create","name":"test","body":"body"}`)
		if err == nil || err.Error() != "plan: description is required for create" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing body", func(t *testing.T) {
		tool := &PlanTool{Dirs: []string{t.TempDir()}}
		_, err := tool.Execute(ctx, `{"action":"create","name":"test","description":"desc"}`)
		if err == nil || err.Error() != "plan: body is required for create" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("no search paths", func(t *testing.T) {
		tool := &PlanTool{}
		_, err := tool.Execute(ctx, `{"action":"create","name":"test","description":"desc","body":"body"}`)
		if err == nil || err.Error() != "plan: no search paths configured" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestPlanTool_Create_Success(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	tool := &PlanTool{Dirs: []string{tmp}}

	result, err := tool.Execute(ctx, `{"action":"create","name":"my-plan","description":"A test plan","body":"## Steps\n\n1. Do it."}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "my-plan") {
		t.Errorf("result missing plan name: %s", result)
	}
	if !contains(result, "draft") {
		t.Errorf("result missing status: %s", result)
	}
}

func TestPlanTool_List(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	tool := &PlanTool{Dirs: []string{tmp}}

	// Empty list
	result, err := tool.Execute(ctx, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "No plans found") {
		t.Errorf("unexpected result: %s", result)
	}

	// Create a plan then list
	tool.Execute(ctx, `{"action":"create","name":"alpha","description":"First","body":"body"}`)
	tool.Execute(ctx, `{"action":"create","name":"beta","description":"Second","body":"body"}`)

	result, err = tool.Execute(ctx, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "2 plan") {
		t.Errorf("expected 2 plans: %s", result)
	}
	if !contains(result, "alpha") || !contains(result, "beta") {
		t.Errorf("missing plan names: %s", result)
	}
}

func TestPlanTool_Show(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	tool := &PlanTool{Dirs: []string{tmp}}

	_, err := tool.Execute(ctx, `{"action":"create","name":"show-me","description":"desc","body":"## Steps\n\n1. Step one."}`)
	if err != nil {
		t.Fatalf("create error: %v", err)
	}

	result, err := tool.Execute(ctx, `{"action":"show","name":"show-me"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "show-me") {
		t.Error("missing plan name")
	}
	if !contains(result, "Step one") {
		t.Error("missing body content")
	}

	// Show non-existent
	_, err = tool.Execute(ctx, `{"action":"show","name":"nope"}`)
	if err == nil {
		t.Error("expected error for missing plan")
	}
}

func TestPlanTool_Approve(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	tool := &PlanTool{Dirs: []string{tmp}}

	tool.Execute(ctx, `{"action":"create","name":"approve-me","description":"desc","body":"body"}`)

	result, err := tool.Execute(ctx, `{"action":"approve","name":"approve-me"}`)
	if err != nil {
		t.Fatalf("approve error: %v", err)
	}
	if !contains(result, "approved") {
		t.Errorf("expected approved in result: %s", result)
	}

	// Double-approve is a no-op
	result, err = tool.Execute(ctx, `{"action":"approve","name":"approve-me"}`)
	if err != nil {
		t.Fatalf("second approve error: %v", err)
	}
	if !contains(result, "already approved") {
		t.Errorf("expected 'already approved': %s", result)
	}
}

func TestPlanTool_Edit(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	tool := &PlanTool{Dirs: []string{tmp}}

	tool.Execute(ctx, `{"action":"create","name":"edit-me","description":"old desc","body":"old body"}`)

	t.Run("update description", func(t *testing.T) {
		result, err := tool.Execute(ctx, `{"action":"edit","name":"edit-me","description":"new desc"}`)
		if err != nil {
			t.Fatalf("edit error: %v", err)
		}
		if !contains(result, "description") {
			t.Errorf("missing 'description' in result: %s", result)
		}
	})

	t.Run("update status", func(t *testing.T) {
		result, err := tool.Execute(ctx, `{"action":"edit","name":"edit-me","status":"in_progress"}`)
		if err != nil {
			t.Fatalf("edit error: %v", err)
		}
		if !contains(result, "in_progress") {
			t.Errorf("missing 'in_progress' in result: %s", result)
		}
	})

	t.Run("update body", func(t *testing.T) {
		result, err := tool.Execute(ctx, `{"action":"edit","name":"edit-me","body":"new body"}`)
		if err != nil {
			t.Fatalf("edit error: %v", err)
		}
		if !contains(result, "body") {
			t.Errorf("missing 'body' in result: %s", result)
		}
	})

	t.Run("reject invalid status", func(t *testing.T) {
		_, err := tool.Execute(ctx, `{"action":"edit","name":"edit-me","status":"bogus"}`)
		if err == nil {
			t.Error("expected error for invalid status")
		}
	})

	t.Run("reject empty edit", func(t *testing.T) {
		_, err := tool.Execute(ctx, `{"action":"edit","name":"edit-me"}`)
		if err == nil {
			t.Error("expected error for empty edit")
		}
	})
}

func TestPlanTool_Delete(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	tool := &PlanTool{Dirs: []string{tmp}}

	tool.Execute(ctx, `{"action":"create","name":"delete-me","description":"desc","body":"body"}`)

	result, err := tool.Execute(ctx, `{"action":"delete","name":"delete-me"}`)
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if !contains(result, "deleted") {
		t.Errorf("expected 'deleted' in result: %s", result)
	}

	// Deleting non-existent plan
	_, err = tool.Execute(ctx, `{"action":"delete","name":"delete-me"}`)
	if err == nil {
		t.Error("expected error for missing plan")
	}
}

func TestPlanTool_UnknownAction(t *testing.T) {
	ctx := context.Background()
	tool := &PlanTool{Dirs: []string{t.TempDir()}}
	_, err := tool.Execute(ctx, `{"action":"bogus"}`)
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestPlanTool_CannotReuseName(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	tool := &PlanTool{Dirs: []string{tmp}}

	_, err := tool.Execute(ctx, `{"action":"create","name":"dupe","description":"first","body":"body"}`)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err = tool.Execute(ctx, `{"action":"create","name":"dupe","description":"second","body":"body"}`)
	if err == nil {
		t.Error("expected error for duplicate plan name")
	}
}
