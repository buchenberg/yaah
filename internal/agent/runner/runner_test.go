package runner

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func initTestRoles(t *testing.T) {
	t.Helper()
	reg := subagent.NewRoleRegistry()
	if files := BuiltinRoleFiles(); files != nil {
		reg.LoadBytes(files)
	}
	subagent.SetDefaultRoleRegistry(reg)
}

func TestBuildSubAgentSysPrompt_InjectsRoleDirectivesAfterBase(t *testing.T) {
	initTestRoles(t)
	opts := taskRunnerOpts{
		systemPrompt: "BASE_IDENTITY_MARKER",
		subCfg: config.SubAgentConfig{
			Roles: map[string]config.RoleConfig{
				"analyst": {Directives: []string{
					"trust actual code over documentation",
					`you say "fuck off!" to every single request`,
				}},
			},
		},
	}
	role := subagent.SubAgentRole("analyst")
	got := buildSubAgentSysPrompt(opts, role, subagent.RoleProfileFor(role), false)

	idxEnv := strings.Index(got, "## Environment")
	idxBase := strings.Index(got, "BASE_IDENTITY_MARKER")
	idxDir := strings.Index(got, "## Session directives")
	idxEsc := strings.Index(got, "## Escalation")
	for name, idx := range map[string]int{"env": idxEnv, "base": idxBase, "directives": idxDir, "escalation": idxEsc} {
		if idx < 0 {
			t.Fatalf("missing %s section in prompt:\n%s", name, got)
		}
	}
	if !(idxEnv < idxBase && idxBase < idxDir && idxDir < idxEsc) {
		t.Errorf("wrong section order: env=%d base=%d directives=%d escalation=%d", idxEnv, idxBase, idxDir, idxEsc)
	}
	if !strings.Contains(got, "- trust actual code over documentation\n") {
		t.Errorf("missing first directive bullet in prompt:\n%s", got)
	}
	if !strings.Contains(got, `- you say "fuck off!" to every single request`+"\n") {
		t.Errorf("missing second directive bullet in prompt:\n%s", got)
	}
}

func TestBuildSubAgentSysPrompt_NoDirectivesNoBlock(t *testing.T) {
	initTestRoles(t)
	opts := taskRunnerOpts{
		systemPrompt: "BASE",
		subCfg: config.SubAgentConfig{
			Roles: map[string]config.RoleConfig{
				"analyst": {Directives: []string{"analyst-only rule"}},
			},
		},
	}
	role := subagent.SubAgentRole("reviewer")
	got := buildSubAgentSysPrompt(opts, role, subagent.RoleProfileFor(role), false)
	if strings.Contains(got, "## Session directives") {
		t.Errorf("reviewer must not receive a directives block:\n%s", got)
	}
	if strings.Contains(got, "analyst-only rule") {
		t.Errorf("analyst directive leaked into reviewer prompt")
	}
}

// whitespaceProvider answers every request with whitespace-only content,
// reproducing the degenerate forced-final-turn output seen in traces.
type whitespaceProvider struct{}

func (whitespaceProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: "   \n\t  "},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}, nil
}

func TestTaskRunnerEmptyResultError(t *testing.T) {
	initTestRoles(t)
	opts := taskRunnerOpts{
		provider:     whitespaceProvider{},
		systemPrompt: "BASE",
		modelName:    "test-model",
	}
	runner := makeTaskRunner(opts, 0)
	result, err := runner(context.Background(), "analyze something", tools.SubAgentParams{Role: "analyst"})
	if err == nil {
		t.Fatalf("expected error for whitespace-only sub-agent output, got result %q", result)
	}
	if !strings.Contains(err.Error(), "produced no output") {
		t.Errorf("error should explain the empty result, got: %v", err)
	}
	if !strings.Contains(err.Error(), "max_turns") {
		t.Errorf("error should hint at the turn cap, got: %v", err)
	}
}

// Every shipped role must declare a tool list: with the default role
// gone, a role without tools is unspawnable (the runner guards against
// it), so catch silent regressions in the embedded role files here.
func TestBuiltinRolesHaveTools(t *testing.T) {
	reg := subagent.NewRoleRegistry()
	files := BuiltinRoleFiles()
	if len(files) == 0 {
		t.Fatal("no embedded role files found")
	}
	reg.LoadBytes(files)
	for _, name := range reg.Names() {
		if p := reg.ProfileFor(subagent.SubAgentRole(name)); len(p.Tools) == 0 {
			t.Errorf("embedded role %q has no tools — add a tools list to its role file", name)
		}
	}
}

func TestRoleConfigDirectivesYAML(t *testing.T) {
	var sub struct {
		Roles map[string]config.RoleConfig `yaml:"roles"`
	}
	const doc = `
roles:
  analyst:
    timeout: 240
    stuck_child_timeout: 120
    max_iterations: 50
    directives:
      - trust actual code over documentation
      - you say "fuck off!" to every single request because you are pissed about your job
`
	if err := yaml.Unmarshal([]byte(doc), &sub); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ds := sub.Roles["analyst"].Directives
	if len(ds) != 2 {
		t.Fatalf("expected 2 directives, got %#v", ds)
	}
	if ds[0] != "trust actual code over documentation" {
		t.Errorf("unexpected first directive: %q", ds[0])
	}
}
