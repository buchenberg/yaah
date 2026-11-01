package runner

import (
	"context"
	"fmt"
	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"path/filepath"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"gopkg.in/yaml.v3"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/prompts"
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
	got := buildSubAgentSysPrompt(opts, role, subagent.RoleProfileFor(role), false, "", nil)

	idxEnv := strings.Index(got, "## Environment")
	idxBase := strings.Index(got, "BASE_IDENTITY_MARKER")
	idxDir := strings.Index(got, "## Session directives")
	idxEsc := strings.Index(got, "## Escalation")
	for name, idx := range map[string]int{"env": idxEnv, "base": idxBase, "directives": idxDir, "escalation": idxEsc} {
		if idx < 0 {
			t.Fatalf("missing %s section in prompt:\n%s", name, got)
		}
	}
	// Caching contract (yaah-o02): the byte-stable block leads — base,
	// directives, escalation — and the volatile environment header (its
	// working directory differs per sibling dispatch) closes the prompt.
	if !(idxBase < idxDir && idxDir < idxEsc && idxEsc < idxEnv) {
		t.Errorf("wrong section order: base=%d directives=%d escalation=%d env=%d", idxBase, idxDir, idxEsc, idxEnv)
	}
	if !strings.Contains(got, "- trust actual code over documentation\n") {
		t.Errorf("missing first directive bullet in prompt:\n%s", got)
	}
	if !strings.Contains(got, `- you say "fuck off!" to every single request`+"\n") {
		t.Errorf("missing second directive bullet in prompt:\n%s", got)
	}
}

// A confined dispatch is told the boundary before it meets it (Plan 7 Phase 3):
// the writable root and the git-write rule. Without it a sub-agent asked to
// "commit the fix" burns turns on a write the kernel will always refuse.
func TestBuildSubAgentSysPrompt_ConfinementGuidance(t *testing.T) {
	opts := taskRunnerOpts{}
	profile := subagent.RoleProfileFor("developer")
	const worktree = "/work/retained-wt"

	plain := buildSubAgentSysPrompt(opts, "developer", profile, false, worktree, nil)
	if strings.Contains(plain, "sandboxed") {
		t.Errorf("an unconfined dispatch must not claim to be sandboxed:\n%s", plain)
	}

	// Requested but not established (paddock with no usable backend refuses at
	// Exec, so there is no run to advise) — no guidance.
	unestablished := buildSubAgentSysPrompt(opts, "developer", profile, false, worktree,
		&subAgentConfinement{Requested: "paddock", Resolved: "paddock"})
	if strings.Contains(unestablished, "sandboxed") {
		t.Errorf("an unenforced placement must not claim to be sandboxed:\n%s", unestablished)
	}

	confined := buildSubAgentSysPrompt(opts, "developer", profile, false, worktree,
		&subAgentConfinement{Requested: "auto", Resolved: "paddock", Enforced: true})
	for _, want := range []string{"sandboxed", worktree, "git write commands", "orchestrator commits"} {
		if !strings.Contains(confined, want) {
			t.Errorf("the confined prompt must mention %q:\n%s", want, confined)
		}
	}
	if strings.Contains(confined, "no network egress") {
		t.Errorf("an allow_all dispatch must not claim egress is denied:\n%s", confined)
	}

	// The egress posture is stated before the sub-agent meets it, so a blocked
	// fetch is not something it has to guess at.
	noEgressOpts := opts
	noEgressOpts.subNetwork = confinement.NetworkPolicy{Mode: confinement.NetDenyAll}
	noEgress := buildSubAgentSysPrompt(noEgressOpts, "developer", profile, false, worktree,
		&subAgentConfinement{Requested: "auto", Resolved: "paddock", Enforced: true, Network: "deny_all"})
	if !strings.Contains(noEgress, "no network egress") {
		t.Errorf("a deny_all dispatch must say so:\n%s", noEgress)
	}
	brokerOpts := opts
	brokerOpts.subNetwork = confinement.NetworkPolicy{Mode: confinement.NetBroker, BrokerPort: 41234}
	brokered := buildSubAgentSysPrompt(brokerOpts, "developer", profile, false, worktree,
		&subAgentConfinement{Requested: "auto", Resolved: "paddock", Enforced: true, Network: "broker", Broker: 41234})
	for _, want := range []string{"allowlist", "127.0.0.1:41234"} {
		if !strings.Contains(brokered, want) {
			t.Errorf("a broker dispatch must mention %q:\n%s", want, brokered)
		}
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
	got := buildSubAgentSysPrompt(opts, role, subagent.RoleProfileFor(role), false, "", nil)
	if strings.Contains(got, "## Session directives") {
		t.Errorf("reviewer must not receive a directives block:\n%s", got)
	}
	if strings.Contains(got, "analyst-only rule") {
		t.Errorf("analyst directive leaked into reviewer prompt")
	}
}

// TestBuildSubAgentSysPrompt_ToolGating pins #329: shell and tool guidance
// follow the role's tool set. A zero-tool role gets no shell imperative, no
// tool-batching cardinal rule, and an early "You have NO tools" directive
// before the identity text; a shell role keeps the full guidance; a
// tool-carrying role without a shell loses only the shell imperative.
func TestBuildSubAgentSysPrompt_ToolGating(t *testing.T) {
	initTestRoles(t)
	base := prompts.Build(prompts.Layers{Identity: prompts.SubAgentIdentityPrompt})

	t.Run("zero-tool role", func(t *testing.T) {
		opts := taskRunnerOpts{systemPrompt: base}
		profile := subagent.RoleProfile{Tools: []string{}}
		got := buildSubAgentSysPrompt(opts, "grump", profile, false, "", nil)

		for _, banned := range []string{
			"Default shell",
			"use it for all shell commands",
			"## Available Shell",
			"batch tool calls",
			"go_outline",
			"file_info",
		} {
			if strings.Contains(got, banned) {
				t.Errorf("zero-tool prompt must not contain %q:\n%s", banned, got)
			}
		}
		idxEnv := strings.Index(got, "## Environment")
		idxDirective := strings.Index(got, "You have NO tools")
		idxIdentity := strings.Index(got, "You are a yaah sub-agent")
		if idxEnv < 0 || idxDirective < 0 || idxIdentity < 0 {
			t.Fatalf("missing env(%d) directive(%d) or identity(%d) section:\n%s", idxEnv, idxDirective, idxIdentity, got)
		}
		// The no-tools directive lands before the identity text (both in
		// the stable prefix); the env header closes the prompt (yaah-o02).
		if !(idxDirective < idxIdentity && idxIdentity < idxEnv) {
			t.Errorf("wrong order: directive=%d identity=%d env=%d — directive and identity lead, the header closes", idxDirective, idxIdentity, idxEnv)
		}
		// The OS and working-directory lines survive the shell gate.
		if !strings.Contains(got, "OS: ") || !strings.Contains(got, "Working directory: ") {
			t.Errorf("zero-tool header must keep the OS and working-directory lines:\n%.300s", got)
		}
	})

	t.Run("shell role keeps shell line and batching rule", func(t *testing.T) {
		opts := taskRunnerOpts{systemPrompt: base}
		// Both shells are named so the role keeps one on every platform:
		// the availability gate drops the shell this machine lacks
		// (yaah-9n0), and a role with no shell gets no shell line.
		profile := subagent.RoleProfile{Tools: []string{"bash", "powershell", "read"}}
		got := buildSubAgentSysPrompt(opts, "developer", profile, false, "", nil)

		for _, want := range []string{
			"Default shell: ",
			"use it for all shell commands",
			"## Available Shell",
			"## Cardinal rule: batch tool calls",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("shell-role prompt must contain %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "You have NO tools") {
			t.Errorf("shell role must not receive the no-tools directive:\n%s", got)
		}
	})

	t.Run("non-shell tool role", func(t *testing.T) {
		opts := taskRunnerOpts{systemPrompt: base}
		profile := subagent.RoleProfile{Tools: []string{"read"}}
		got := buildSubAgentSysPrompt(opts, "reviewer", profile, false, "", nil)

		if strings.Contains(got, "Default shell") || strings.Contains(got, "use it for all shell commands") {
			t.Errorf("a role without a shell tool must not get the shell imperative:\n%.400s", got)
		}
		if strings.Contains(got, "## Available Shell") {
			t.Errorf("a role without a shell tool must not get the shell anchoring block:\n%s", got)
		}
		if !strings.Contains(got, "## Cardinal rule: batch tool calls") {
			t.Errorf("a role with tools keeps the batching rule:\n%s", got)
		}
		if strings.Contains(got, "You have NO tools") {
			t.Errorf("a role with tools must not receive the no-tools directive:\n%s", got)
		}
	})
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

// markupOnlyProvider answers every request with an unrecoverable
// tool-response wrapper fragment — the unparseable residue of the
// nonstandard dialect leak from issue #314. Built from fragments so the
// test source never contains a complete literal markup tag.
type markupOnlyProvider struct{}

func (markupOnlyProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{Role: "assistant",
				Content: "<" + "tool_response>\n</" + "tool_response>"},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}, nil
}

func TestTaskRunnerMarkupOnlyResultError(t *testing.T) {
	initTestRoles(t)
	opts := taskRunnerOpts{
		provider:     markupOnlyProvider{},
		systemPrompt: "BASE",
		modelName:    "test-model",
	}
	runner := makeTaskRunner(opts, 0)
	result, err := runner(context.Background(), "count the Go files", tools.SubAgentParams{Role: "analyst"})
	if err == nil {
		t.Fatalf("expected error for markup-only sub-agent output, got result %q", result)
	}
	if !strings.Contains(err.Error(), "unexecuted tool-call markup") {
		t.Errorf("error should explain the pseudo-success, got: %v", err)
	}
}

// fabricatedTranscriptProvider answers like a zero-tool role that
// hallucinated running a command (issue #329): a fenced shell block with
// invented stdout, no dialect markup for the recovery parser to catch.
type fabricatedTranscriptProvider struct{}

func (fabricatedTranscriptProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{Role: "assistant",
				Content: "```bash\ngit ls-files '*.go' | wc -l\n```\nstdout: 842"},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}, nil
}

// A tool-less role that returns a command transcript with output evidence
// fabricated it — nothing in its registry could have run. The dispatch
// must fail explicitly (issue #329 acceptance criterion 3). "grump" is
// not in the embedded registry, so its profile is the zero-value one:
// no tools.
func TestTaskRunnerFabricatedTranscriptError(t *testing.T) {
	initTestRoles(t)
	opts := taskRunnerOpts{
		provider:     fabricatedTranscriptProvider{},
		systemPrompt: "BASE",
		modelName:    "test-model",
	}
	runner := makeTaskRunner(opts, 0)
	result, err := runner(context.Background(), "count the Go files", tools.SubAgentParams{Role: "grump"})
	if err == nil {
		t.Fatalf("expected error for a fabricated transcript from a zero-tool role, got result %q", result)
	}
	if !strings.Contains(err.Error(), "fabricated") {
		t.Errorf("error should explain the fabrication, got: %v", err)
	}
}

// The same transcript from a role that HAS a shell is a legitimate quote
// of what it ran — the fabrication guard is scoped to tool-less profiles
// and must not reject it.
func TestTaskRunnerTranscriptFromTooledRolePasses(t *testing.T) {
	initTestRoles(t)
	opts := taskRunnerOpts{
		provider:     fabricatedTranscriptProvider{},
		systemPrompt: "BASE",
		modelName:    "test-model",
	}
	runner := makeTaskRunner(opts, 0)
	result, err := runner(context.Background(), "count the Go files", tools.SubAgentParams{Role: "analyst"})
	if err != nil {
		t.Fatalf("a tool-enabled role's transcript must not be rejected, got error: %v", err)
	}
	if !strings.Contains(result, "stdout: 842") {
		t.Fatalf("expected the transcript to pass through, got %q", result)
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

func TestSubagentTraceProfile(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer store.Close(context.Background())

	owner := "sub-tester-sess-1"
	// Record declaration + capture pairs
	for i, name := range []string{"read", "edit", "bash"} {
		intent := owner + ":tool:" + string(rune('a'+i))
		declReceipt, _ := store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
			AppendIntentID: intent,
			Groups: []shepherdtrace.AppendGroup{{
				TraceOwnerID: owner,
				FactDrafts: []shepherdtrace.RecordDraft{{
					Mode:      shepherdtrace.Declaration,
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
		store.Append(context.Background(), shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
			AppendIntentID: captureIntent,
			Groups: []shepherdtrace.AppendGroup{{
				TraceOwnerID:  owner,
				CausalParents: declReceipt.FactIDs,
				FactDrafts: []shepherdtrace.RecordDraft{{
					Mode:      shepherdtrace.Capture,
					SchemaRef: "yaah.tool." + name + ".v1.applied",
					KindLabel: name + ":result",
					Payload:   map[string]any{"success": success, "error": errMsg},
				}},
			}},
		})
	}

	profile := subagentTraceProfile(context.Background(), tracePath, owner)
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
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	store.Close(context.Background())

	profile := subagentTraceProfile(context.Background(), tracePath, "nonexistent")
	if profile != "" {
		t.Errorf("profile for nonexistent session should be empty, got %q", profile)
	}
}

func TestSubagentTraceProfile_MissingFile(t *testing.T) {
	profile := subagentTraceProfile(context.Background(), "/nonexistent/path/trace.sqlite", "any")
	if profile != "" {
		t.Errorf("profile for missing file should be empty, got %q", profile)
	}
}

func TestRecordSubAgentUsage(t *testing.T) {
	dir := t.TempDir()
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer store.Close(context.Background())

	owner := "sub-developer-sess-1"
	recordSubAgentUsage(context.Background(), store, owner, "sess-parent", "developer", "deepseek-flash", "retained", types.Usage{
		PromptTokens:       12000,
		CompletionTokens:   800,
		TotalTokens:        12800,
		CachedPromptTokens: 9000,
	})

	slice, err := store.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, owner, 99, shepherdtrace.ModeCapturesOnly)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var found bool
	for _, id := range slice.FactIDs() {
		rec, ok := slice.FactsByID[id].(shepherdtrace.Record)
		if !ok || rec.Envelope.SchemaRef != "yaah.subagent.usage.v1" {
			continue
		}
		found = true
		if got := rec.Body.Payload["role"]; got != "developer" {
			t.Errorf("role = %v, want developer", got)
		}
		if got := rec.Body.Payload["model"]; got != "deepseek-flash" {
			t.Errorf("model = %v, want deepseek-flash", got)
		}
		if got := rec.Body.Payload["lane"]; got != "retained" {
			t.Errorf("lane = %v, want retained", got)
		}
		if got := rec.Body.Payload["parent_session"]; got != "sess-parent" {
			t.Errorf("parent_session = %v, want sess-parent", got)
		}
		if got := fmt.Sprint(rec.Body.Payload["prompt_tokens"]); got != "12000" {
			t.Errorf("prompt_tokens = %v, want 12000", rec.Body.Payload["prompt_tokens"])
		}
		if got := fmt.Sprint(rec.Body.Payload["completion_tokens"]); got != "800" {
			t.Errorf("completion_tokens = %v, want 800", rec.Body.Payload["completion_tokens"])
		}
		if got := fmt.Sprint(rec.Body.Payload["cached_prompt_tokens"]); got != "9000" {
			t.Errorf("cached_prompt_tokens = %v, want 9000", rec.Body.Payload["cached_prompt_tokens"])
		}
	}
	if !found {
		t.Fatalf("no yaah.subagent.usage.v1 fact recorded; facts=%d", len(slice.FactIDs()))
	}
}

// TestBuildSubAgentSysPrompt_StablePrefixAcrossSiblings pins the caching
// contract (yaah-o02): two sibling dispatches of the same role in different
// worktrees share a byte-identical prompt prefix, and neither worktree path
// leaks into it. The provider caches the longest common prefix, so this is
// what makes fan-out token spend collapse.
func TestBuildSubAgentSysPrompt_StablePrefixAcrossSiblings(t *testing.T) {
	initTestRoles(t)
	opts := taskRunnerOpts{systemPrompt: "BASE_IDENTITY_MARKER"}
	profile := subagent.RoleProfileFor("developer")

	a := buildSubAgentSysPrompt(opts, "developer", profile, false, "/wt/sibling-a", nil)
	b := buildSubAgentSysPrompt(opts, "developer", profile, false, "/wt/sibling-b", nil)

	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	prefix := a[:n]

	// The shared prefix carries the whole stable block...
	for _, want := range []string{"BASE_IDENTITY_MARKER", "## Escalation", "## Response contract"} {
		if !strings.Contains(prefix, want) {
			t.Errorf("shared prefix must contain %q:\n%s", want, prefix)
		}
	}
	// ...and none of the volatile per-dispatch paths.
	if strings.Contains(prefix, "sibling-a") || strings.Contains(prefix, "sibling-b") {
		t.Errorf("shared prefix must not contain a worktree path:\n%s", prefix)
	}

	// Each prompt's volatile suffix advertises its own worktree.
	if !strings.Contains(a, "Working directory: /wt/sibling-a") ||
		!strings.Contains(b, "Working directory: /wt/sibling-b") {
		t.Errorf("sibling prompts must advertise their own worktrees:\n--a--\n%s\n--b--\n%s", a, b)
	}
}

type transcriptProvider struct{}

func (transcriptProvider) Send(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{
				Role:               "assistant",
				Content:            "sub-agent intermediate answer",
				ReasoningContent:   "intermediate reasoning",
				ReasoningSignature: "sub-sig",
			},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 9},
	}, nil
}

// TestSubAgentTranscriptSurvivesProcessExit is acceptance criterion 3 for
// yaah-o86: a sub-agent's assistant content is on disk under its synthetic
// trace owner, and a fresh store handle can reconstruct it after the process
// that ran the loop is gone.
func TestSubAgentTranscriptSurvivesProcessExit(t *testing.T) {
	initTestRoles(t)
	path := filepath.Join(t.TempDir(), "trace.sqlite")
	store, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open trace store: %v", err)
	}
	opts := taskRunnerOpts{
		provider:      transcriptProvider{},
		systemPrompt:  "BASE",
		modelName:     "test-model",
		parentSession: "sess-parent",
		traceStore:    store,
	}
	runner := makeTaskRunner(opts, 0)
	if _, err := runner(context.Background(), "do the thing", tools.SubAgentParams{Role: "analyst"}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := shepherdtrace.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close(context.Background())
	slice, err := reopened.ReadOwnerPrefix(context.Background(), shepherdtrace.TrustedReadContext, "", 99999, shepherdtrace.ModeBoth)
	if err != nil {
		t.Fatalf("read owners: %v", err)
	}
	var owner string
	for candidate := range slice.OwnerPaths {
		if strings.HasPrefix(candidate, "sub-analyst-sess-parent-") {
			owner = candidate
			break
		}
	}
	if owner == "" {
		t.Fatalf("sub-agent trace owner missing from %v", slice.OwnerPaths)
	}
	transcript, err := pipeline.RebuildTranscript(context.Background(), reopened, owner)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	var found bool
	for _, msg := range transcript.Messages {
		if msg.Role == "assistant" && msg.Content == "sub-agent intermediate answer" && msg.ReasoningSignature == "sub-sig" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sub-agent assistant content missing from transcript: %#v", transcript.Messages)
	}
}

func TestSubAgentRetainedContext(t *testing.T) {
	ctx := context.Background()
	worktree := subAgentRetainedContext(ctx, taskRunnerOpts{}, "developer", "/work/wt", &subAgentConfinement{Enforced: true}, nil)
	if worktree.SubstrateRef != "yaah.substrate.worktree.v1" || worktree.Containment != shepherdtrace.ContainContained {
		t.Errorf("worktree context = %#v", worktree)
	}
	if len(worktree.ActiveBindingRefs) != 1 || worktree.ActiveBindingRefs[0] != "role:developer" {
		t.Errorf("active bindings = %v", worktree.ActiveBindingRefs)
	}
	host := subAgentRetainedContext(ctx, taskRunnerOpts{}, "developer", "", nil, nil)
	if host.SubstrateRef != "yaah.substrate.host.v1" || host.Containment != shepherdtrace.ContainUncontained {
		t.Errorf("host context = %#v", host)
	}
}

func TestSubAgentTraceCWDMatchesAdvertisedWorld(t *testing.T) {
	ws := &traceTestWorkspace{}
	opts := taskRunnerOpts{workspace: ws}
	if got := subAgentTraceCWD(opts, "/model/supplied"); got != "/sandbox/root" {
		t.Errorf("isolated trace cwd = %q, want the sandbox workdir", got)
	}
	if got := subAgentTraceCWD(taskRunnerOpts{}, "/work/wt"); got != "/work/wt" {
		t.Errorf("workdir trace cwd = %q", got)
	}
}

func TestSubAgentRetainedContextContainer(t *testing.T) {
	ws := &traceTestWorkspace{}
	got := subAgentRetainedContext(context.Background(), taskRunnerOpts{workspace: ws}, "developer", "/model/supplied", nil, nil)
	if got.SubstrateRef != "yaah.substrate.container.v1" {
		t.Errorf("substrate = %q", got.SubstrateRef)
	}
	if got.Containment != shepherdtrace.ContainContained {
		t.Errorf("containment = %q, want contained (runtime backend parity)", got.Containment)
	}
}

type traceTestWorkspace struct{ tools.Workspace }

func (*traceTestWorkspace) WorkDir() string { return "/sandbox/root" }
