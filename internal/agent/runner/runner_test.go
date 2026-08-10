package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
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

func seedSubagentTraceStore(t *testing.T, store *shepherd.SQLiteTraceStore, owner string, tools []string, failures []int) {
	t.Helper()
	for i, name := range tools {
		intentID := owner + ":tool:" + string(rune('a'+i))
		payload := map[string]any{"tool": name, "args": "{}"}
		mode := shepherd.Declaration
		if i%2 == 1 {
			mode = shepherd.Capture
		}
		store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: intentID,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID: owner,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      mode,
					SchemaRef: "yaah.tool." + name + ".v1",
					KindLabel: name,
					Payload:   payload,
				}},
			}},
		})
	}
}

func TestSubagentTraceProfile(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace.sqlite")
	store, err := shepherd.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer store.Close()

	owner := "sub-tester-sess-1"
	// Record declaration + capture pairs
	for i, name := range []string{"read", "edit", "bash"} {
		intent := owner + ":tool:" + string(rune('a'+i))
		declReceipt, _ := store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: intent,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID: owner,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      shepherd.Declaration,
					SchemaRef: "yaah.tool." + name + ".v1",
					KindLabel: name,
					Payload:   map[string]any{"tool": name, "args": `{"file":"test.go"}`},
				}},
			}},
		})
		captureIntent := owner + ":result:" + string(rune('a'+i))
		success := true
		errMsg := ""
		if name == "bash" {
			success = false
			errMsg = "permission denied"
		}
		store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: captureIntent,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID:  owner,
				CausalParents: declReceipt.FactIDs,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      shepherd.Capture,
					SchemaRef: "yaah.tool." + name + ".v1.applied",
					KindLabel: name + ":result",
					Payload:   map[string]any{"success": success, "error": errMsg},
				}},
			}},
		})
	}

	profile := subagentTraceProfile(tracePath, owner)
	if profile == "" {
		t.Fatal("profile should not be empty")
	}
	if !strings.Contains(profile, "read") {
		t.Error("profile should mention read")
	}
	if !strings.Contains(profile, "edit") {
		t.Error("profile should mention edit")
	}
	if !strings.Contains(profile, "bash") {
		t.Error("profile should mention bash")
	}
	if !strings.Contains(profile, "tool calls") {
		t.Error("profile should include tool count")
	}
	if !strings.Contains(profile, "error: permission denied") {
		t.Errorf("profile should include error message, got:\n%s", profile)
	}
}

func TestSubagentTraceProfile_NonexistentSession(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace.sqlite")
	store, err := shepherd.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	store.Close()

	profile := subagentTraceProfile(tracePath, "nonexistent")
	if profile != "" {
		t.Errorf("profile for nonexistent session should be empty, got %q", profile)
	}
}

func TestSubagentTraceProfile_MissingFile(t *testing.T) {
	profile := subagentTraceProfile("/nonexistent/path/trace.sqlite", "any")
	if profile != "" {
		t.Errorf("profile for missing file should be empty, got %q", profile)
	}
}
