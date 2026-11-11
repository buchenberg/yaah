package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// invalid_args_test.go is the conformance matrix for yaah-was9.9. It pins two
// things:
//
//  1. every registered built-in tool rejects an unknown argument with the
//     strict decoder's typed error (no silent ignore);
//  2. the conditional and mutually-exclusive shapes the schemas advertise are
//     enforced as Go errors, not prose inside a successful result.
//
// A new tool that is added without a case here still inherits (1) through
// allBuiltinTools; a tool that stops using the strict decoder fails the first
// test.

func TestEveryBuiltinToolRejectsUnknownArguments(t *testing.T) {
	for _, tool := range allBuiltinTools() {
		name := tool.Name()
		t.Run(name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), `{"__unknown_argument__":true}`)
			var unknown *UnknownArgumentError
			if !errors.As(err, &unknown) {
				t.Fatalf("%s: unknown argument = %v, want UnknownArgumentError", name, err)
			}
			keys, kerr := SchemaPropertyNames(tool.Schema())
			if kerr != nil {
				t.Fatalf("%s: schema has no usable properties: %v", name, kerr)
			}
			if len(keys) > 0 && len(unknown.Allowed) == 0 {
				t.Errorf("%s: the error must list the allowed keys", name)
			}
		})
	}
}

func TestInvalidArgumentMatrix(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		args string
		want string
	}{
		{"read missing path", &ReadTool{}, `{}`, "filePath"},
		{"bash missing command", &BashTool{}, `{}`, "command"},
		{"write missing path", &WriteTool{}, `{"content":"x"}`, "filePath"},
		{"edit both shapes", &EditTool{}, `{"filePath":"x","oldString":"a","newString":"b","edits":[{"oldString":"c","newString":"d"}]}`, "not both"},
		{"question header too long", &QuestionTool{}, `{"questions":[{"question":"q","header":"1234567890123456789012345678901","options":[{"label":"a"}]}]}`, "maximum is 30"},
		{"plan show without name", &PlanTool{}, `{"action":"show"}`, "name is required for show"},
		{"background_process status without id", &BackgroundProcessTool{}, `{"action":"status"}`, "id is required for status"},
		{"background_process unknown action", &BackgroundProcessTool{}, `{"action":"explode"}`, "unknown action"},
		{"git unknown action", &GitTool{}, `{"action":"explode"}`, "unsupported"},
		{"go_mod add without module", NewGoModTool(), `{"action":"add"}`, "module"},
		{"proposal strategy outside apply", &ProposalTool{}, `{"action":"list","strategy":"path-disjoint"}`, "only valid with apply"},
		{"memory_add tags wrong type", &MemoryAddTool{}, `{"text":"x","tags":42}`, "tags"},
		{"go_test packages looks like an option", NewGoTestTool(), `{"packages":"-exec=/tmp/x"}`, "refused"},
		{"bisect missing action", NewBisectTool(), `{}`, "unknown action"},
		{"wait_for missing predicate", &WaitForTool{}, `{}`, "on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.tool.Execute(context.Background(), tc.args)
			if err == nil {
				t.Fatalf("%s: expected a Go error, got a successful result", tc.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Errorf("%s: error = %v, want substring %q", tc.name, err, tc.want)
			}
		})
	}
}
