package yaah

import (
	"slices"
	"strings"
	"testing"
)

func TestPersonaMCPOmissions(t *testing.T) {
	mcpNames := map[string]bool{
		"github_get_me":                    true,
		"github_pull_request_read":         true,
		"github_pull_request_review_write": true,
	}

	tests := []struct {
		name        string
		persona     []string
		mcp         map[string]bool
		want        []string
		wantWarning bool
	}{
		{"no persona tools narrows nothing", nil, mcpNames, nil, false},
		{"empty persona slice narrows nothing", []string{}, mcpNames, nil, false},
		{"no MCP tools registered", []string{"read", "bash"}, nil, nil, false},
		{"all MCP tools listed", []string{"read", "github_get_me", "github_pull_request_read", "github_pull_request_review_write"}, mcpNames, nil, false},
		{"partial list omits the rest", []string{"read", "github_get_me"}, mcpNames,
			[]string{"github_pull_request_read", "github_pull_request_review_write"}, true},
		{"blank entries and padding normalize", []string{"  read ", "", " github_get_me "}, map[string]bool{
			"github_get_me": true,
		}, nil, false},
		{"entries naming no MCP tool are not omissions", []string{"read", "write", "bash"}, map[string]bool{
			"github_get_me": true,
		}, []string{"github_get_me"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := personaMCPOmissions(tt.persona, tt.mcp)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("personaMCPOmissions(%q) = %q, want %q", tt.persona, got, tt.want)
			}
			warning := personaMCPOmissionWarning("code-reviewer", tt.persona, tt.mcp)
			if tt.wantWarning != (warning != "") {
				t.Fatalf("warning = %q; want non-empty = %v", warning, tt.wantWarning)
			}
		})
	}
}

func TestPersonaMCPOmissionWarning(t *testing.T) {
	got := personaMCPOmissionWarning("code-reviewer",
		[]string{"read", "github_get_me"},
		map[string]bool{
			"github_get_me":                    true,
			"github_pull_request_read":         true,
			"github_pull_request_review_write": true,
		})

	// The message must say who dropped what and how to undo it; the
	// names are sorted so the output is stable run to run.
	for _, want := range []string{
		`persona "code-reviewer"`,
		"2 MCP tool(s)",
		"github_pull_request_read, github_pull_request_review_write",
		"remove the list",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "github_get_me") {
		t.Errorf("warning names an allowed tool:\n%s", got)
	}
}
