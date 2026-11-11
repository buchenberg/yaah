package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// args_strict_test.go pins the strict-argument contract for the delegation
// tools: a typo must fail with the allowed list instead of silently changing
// the dispatch (was9.3).

func TestDelegate_RejectsUnknownArguments(t *testing.T) {
	tool := &DelegateTool{RoleNames: []string{"analyst"}}
	cases := []struct {
		name string
		args string
		key  string
	}{
		{"background typo", `{"description":"d","prompt":"p","role":"analyst","backgroud":true}`, "backgroud"},
		{"seed_from typo", `{"description":"d","prompt":"p","role":"analyst","seed_form":"prop-1"}`, "seed_form"},
		{"max_turns typo", `{"description":"d","prompt":"p","role":"analyst","max_turn":5}`, "max_turn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), tc.args)
			var unknown *UnknownArgumentError
			if !errors.As(err, &unknown) {
				t.Fatalf("error = %v, want UnknownArgumentError", err)
			}
			if len(unknown.Arguments) != 1 || unknown.Arguments[0] != tc.key {
				t.Errorf("unknown arguments = %v, want [%s]", unknown.Arguments, tc.key)
			}
			wantAllowed := []string{
				"background", "description", "json_mode", "max_iterations",
				"max_turns", "model", "output_limit", "prompt", "provider",
				"role", "seed_from", "seed_mode", "timeout_seconds",
			}
			if len(unknown.Allowed) != len(wantAllowed) {
				t.Fatalf("allowed keys = %v, want all %d", unknown.Allowed, len(wantAllowed))
			}
			for i, allowed := range wantAllowed {
				if unknown.Allowed[i] != allowed {
					t.Errorf("allowed[%d] = %q, want %q (sorted)", i, unknown.Allowed[i], allowed)
				}
				if !strings.Contains(err.Error(), allowed) {
					t.Errorf("error must list %q in the allowed set: %v", allowed, err)
				}
			}
		})
	}
}

func TestDelegate_RejectsAllUnknownArgumentsAtOnce(t *testing.T) {
	tool := &DelegateTool{RoleNames: []string{"analyst"}}
	_, err := tool.Execute(context.Background(), `{"description":"d","prompt":"p","role":"analyst","backgroud":true,"max_turn":5}`)
	var unknown *UnknownArgumentError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want UnknownArgumentError", err)
	}
	if len(unknown.Arguments) != 2 || unknown.Arguments[0] != "backgroud" || unknown.Arguments[1] != "max_turn" {
		t.Errorf("unknown arguments = %v, want [backgroud max_turn] (sorted)", unknown.Arguments)
	}
}

func TestDelegate_ValidatesDescriptionAndPromptSize(t *testing.T) {
	tool := &DelegateTool{
		RoleNames: []string{"analyst"},
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			return "ok", nil
		},
	}

	if _, err := tool.Execute(context.Background(), `{"prompt":"p","role":"analyst"}`); err == nil || !strings.Contains(err.Error(), "description is required") {
		t.Errorf("missing description = %v, want a required error", err)
	}
	if _, err := tool.Execute(context.Background(), `{"description":"  ","prompt":"p","role":"analyst"}`); err == nil || !strings.Contains(err.Error(), "description is required") {
		t.Errorf("blank description = %v, want a required error", err)
	}

	huge := strings.Repeat("x", maxDelegatePromptBytes+1)
	args, _ := json.Marshal(map[string]string{"description": "d", "prompt": huge, "role": "analyst"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Errorf("oversized prompt = %v, want a size error", err)
	}
}

func TestDelegate_NumericStringCoercionStillWorks(t *testing.T) {
	var got SubAgentParams
	tool := &DelegateTool{
		RoleNames: []string{"analyst"},
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			got = params
			return "ok", nil
		},
	}
	if _, err := tool.Execute(context.Background(), `{"description":"d","prompt":"p","role":"analyst","max_turns":"3","max_iterations":"7"}`); err != nil {
		t.Fatalf("numeric-string coercion: %v", err)
	}
	if got.MaxToolTurns != 3 || got.MaxLoopCycles != 7 {
		t.Errorf("coerced overrides = turns %d iterations %d, want 3/7", got.MaxToolTurns, got.MaxLoopCycles)
	}
}

func TestDelegateAndJobs_SchemasForbidUnknownArguments(t *testing.T) {
	for name, schema := range map[string]json.RawMessage{
		"delegate static": (&DelegateTool{}).Schema(),
		"delegate built":  BuildDelegateSchema([]string{"analyst"}, nil),
		"subagent_jobs":   (&SubAgentJobsTool{}).Schema(),
	} {
		var doc map[string]any
		if err := json.Unmarshal(schema, &doc); err != nil {
			t.Fatalf("%s: schema is not JSON: %v", name, err)
		}
		if doc["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties = %v, want false", name, doc["additionalProperties"])
		}
	}
}

func TestSubAgentJobs_RejectsUnknownArguments(t *testing.T) {
	tool := &SubAgentJobsTool{} // decode runs before the manager-availability check
	_, err := tool.Execute(context.Background(), `{"action":"list","job_i":"x"}`)
	var unknown *UnknownArgumentError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want UnknownArgumentError", err)
	}
	if !strings.Contains(err.Error(), "job_id") {
		t.Errorf("error must list the allowed key: %v", err)
	}
}
