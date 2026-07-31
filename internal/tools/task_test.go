package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExecute_RoleValidation(t *testing.T) {
	var gotRole string
	tt := &TaskTool{
		RoleNames: []string{"analyst", "developer"},
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			gotRole = params.Role
			return "ok", nil
		},
	}

	t.Run("empty role rejected with valid names", func(t *testing.T) {
		_, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p"}`)
		if err == nil || !strings.Contains(err.Error(), "role is required") {
			t.Fatalf("expected role-required error, got %v", err)
		}
		if !strings.Contains(err.Error(), "analyst, developer") {
			t.Errorf("error should list valid roles, got %v", err)
		}
	})

	t.Run("unknown role rejected", func(t *testing.T) {
		_, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"hacker"}`)
		if err == nil || !strings.Contains(err.Error(), `unknown role "hacker"`) {
			t.Fatalf("expected unknown-role error, got %v", err)
		}
	})

	t.Run("valid role dispatches", func(t *testing.T) {
		gotRole = ""
		if _, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"analyst"}`); err != nil {
			t.Fatalf("valid role: unexpected error %v", err)
		}
		if gotRole != "analyst" {
			t.Errorf("runner got role %q, want analyst", gotRole)
		}
	})
}

func TestExecute_RoleResolverLayersOverCached(t *testing.T) {
	var gotRole string
	tt := &TaskTool{
		RoleNames:        []string{"a", "b"},
		RoleResolver:     func() []string { return []string{"b", "c", "goat-jokes"} },
		RoleDescriptions: map[string]string{"goat-jokes": "tells a joke"},
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			gotRole = params.Role
			return "ok", nil
		},
	}

	t.Run("resolver-only role accepted", func(t *testing.T) {
		gotRole = ""
		if _, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"goat-jokes"}`); err != nil {
			t.Fatalf("resolver-only role: unexpected error %v", err)
		}
		if gotRole != "goat-jokes" {
			t.Errorf("runner got role %q, want goat-jokes", gotRole)
		}
	})

	t.Run("cached-only role accepted", func(t *testing.T) {
		gotRole = ""
		if _, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"a"}`); err != nil {
			t.Fatalf("cached-only role: unexpected error %v", err)
		}
		if gotRole != "a" {
			t.Errorf("runner got role %q, want a", gotRole)
		}
	})

	t.Run("unknown role rejected", func(t *testing.T) {
		_, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"unknown"}`)
		if err == nil || !strings.Contains(err.Error(), `unknown role "unknown"`) {
			t.Fatalf("expected unknown-role error, got %v", err)
		}
	})
}

func TestSchema_RoleRequired(t *testing.T) {
	requiredHasRole := func(t *testing.T, raw json.RawMessage) {
		t.Helper()
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("invalid schema JSON: %v", err)
		}
		req, _ := schema["required"].([]any)
		for _, r := range req {
			if r == "role" {
				return
			}
		}
		t.Errorf("schema required must include role, got %v", req)
	}

	// Enum schema (registry roles present).
	requiredHasRole(t, (&TaskTool{RoleNames: []string{"analyst"}}).Schema())
	// Fallback schema (no registry).
	requiredHasRole(t, (&TaskTool{}).Schema())
}

func TestParseSubAgentOutput_NoEscalation(t *testing.T) {
	out := ParseSubAgentOutput("task completed successfully", nil)
	if out.Escalation != nil {
		t.Errorf("expected no escalation, got %+v", out.Escalation)
	}
	if out.RawOutput != "task completed successfully" {
		t.Errorf("raw output mismatch: %q", out.RawOutput)
	}
	if out.Error != "" {
		t.Errorf("expected no error, got %q", out.Error)
	}
}

func TestParseSubAgentOutput_WithEscalation(t *testing.T) {
	output := `I tried to read the file but it doesn't exist.

` + "```escalation\n" +
		`{"severity":"blocker","summary":"file not found","detail":"src/main.go does not exist","suggestion":"check the path"}` + "\n```"

	out := ParseSubAgentOutput(output, nil)
	if out.Escalation == nil {
		t.Fatal("expected escalation, got nil")
	}
	if out.Escalation.Severity != EscalationBlocker {
		t.Errorf("severity = %q, want blocker", out.Escalation.Severity)
	}
	if out.Escalation.Summary != "file not found" {
		t.Errorf("summary = %q", out.Escalation.Summary)
	}
	if out.Escalation.Detail != "src/main.go does not exist" {
		t.Errorf("detail = %q", out.Escalation.Detail)
	}
	if out.Escalation.Suggestion != "check the path" {
		t.Errorf("suggestion = %q", out.Escalation.Suggestion)
	}
}

func TestParseSubAgentOutput_MalformedEscalation(t *testing.T) {
	output := "result\n\n```escalation\nnot json\n```"
	out := ParseSubAgentOutput(output, nil)
	if out.Escalation != nil {
		t.Errorf("malformed escalation should be ignored, got %+v", out.Escalation)
	}
}

func TestParseSubAgentOutput_WithError(t *testing.T) {
	out := ParseSubAgentOutput("", errors.New("timeout"))
	if out.Error != "timeout" {
		t.Errorf("error = %q, want timeout", out.Error)
	}
}
