package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// scriptedProvider replays responses in order; once exhausted it repeats
// the last one so the loop never sees an accidental final answer.
type scriptedProvider struct {
	responses []*types.ChatResponse
	index     int
}

func (p *scriptedProvider) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if p.index >= len(p.responses) {
		return p.responses[len(p.responses)-1], nil
	}
	resp := p.responses[p.index]
	p.index++
	return resp, nil
}

func writeToolCallResponse(id, path, content string) *types.ChatResponse {
	args, _ := json.Marshal(map[string]string{"filePath": path, "content": content})
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.Message{
				Role: "assistant",
				ToolCalls: []types.ToolCall{{
					ID:       id,
					Type:     "function",
					Function: types.ToolCallFn{Name: "write", Arguments: string(args)},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}
}

func finalAnswerResponse(content string) *types.ChatResponse {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message:      types.Message{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: types.Usage{TotalTokens: 5},
	}
}

// TestSupervisedTask_TurnRestoreIntegration drives the full stack:
// SupervisedTaskTool → TaskRunner → sub-agent Loop with turn
// checkpointing enabled. The sub-agent writes a file, then a second
// turn overwrites it and exhausts the iteration budget; the turn
// restore must rewind the second write but keep the first. The attempt
// then succeeds on the retried budget, so no attempt-level rollback
// happens.
func TestSupervisedTask_TurnRestoreIntegration(t *testing.T) {
	initTestRoles(t)

	store, err := shepherd.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	prevMgr := tools.SharedScopeManager
	prevStore := tools.SharedTraceStore
	tools.SharedScopeManager = shepherd.NewScopeManager(store)
	tools.SharedTraceStore = store
	t.Cleanup(func() {
		tools.SharedScopeManager = prevMgr
		tools.SharedTraceStore = prevStore
	})

	repo := newRunnerTestGitRepo(t)
	workFile := filepath.Join(repo, "work.txt")

	provider := &scriptedProvider{responses: []*types.ChatResponse{
		writeToolCallResponse("tc1", workFile, "v1"),
		writeToolCallResponse("tc2", workFile, "corrupted"),
		finalAnswerResponse("done"),
	}}

	opts := taskRunnerOpts{
		provider:     provider,
		systemPrompt: "BASE",
		modelName:    "test-model",
		subCfg: config.SubAgentConfig{
			Roles: map[string]config.RoleConfig{
				"developer": {TurnCheckpoints: true},
			},
		},
		defaults: config.Defaults{
			SupervisedRepoPath: repo,
		},
	}

	tool := &tools.SupervisedTaskTool{
		Runner:     makeTaskRunner(opts, 1),
		RoleNames:  []string{"developer"},
		RepoPath:   repo,
		MaxRetries: 0,
	}

	result, err := tool.Execute(context.Background(),
		`{"prompt":"do the work","role":"developer","max_iterations":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var out struct {
		Status       string `json:"status"`
		Attempts     int    `json:"attempts"`
		Result       string `json:"result"`
		Restores     int    `json:"restores"`
		RestoredFrom string `json:"restored_from"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, result)
	}

	if out.Status != "completed" {
		t.Errorf("status = %q, want completed\nfull result: %s", out.Status, result)
	}
	if out.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (turn restores happen inside the attempt)", out.Attempts)
	}
	if out.Restores < 1 {
		t.Errorf("restores = %d, want >= 1 (exhaustion should trigger a turn restore)", out.Restores)
	}
	if out.RestoredFrom == "" {
		t.Error("restored_from should name the checkpoint restored from")
	}

	// The turn restore rewound turn 2's corrupting write but kept turn
	// 1's file (the restored checkpoint was taken before turn 2).
	got, err := os.ReadFile(workFile)
	if err != nil {
		t.Fatalf("read work.txt after run: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("work.txt = %q, want %q (turn-2 write should have been rewound)", string(got), "v1")
	}
}

// TestSupervisedTask_TurnCheckpointsOffByDefault verifies the per-role
// gate: when the role does NOT set turn_checkpoints, the loop takes no
// turn checkpoints, so an exhausted attempt fails outright with zero
// turn restores (instead of rewinding and retrying). This is the
// default-off guarantee from the phase-9 benchmark decision.
func TestSupervisedTask_TurnCheckpointsOffByDefault(t *testing.T) {
	initTestRoles(t)

	store, err := shepherd.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	prevMgr := tools.SharedScopeManager
	prevStore := tools.SharedTraceStore
	tools.SharedScopeManager = shepherd.NewScopeManager(store)
	tools.SharedTraceStore = store
	t.Cleanup(func() {
		tools.SharedScopeManager = prevMgr
		tools.SharedTraceStore = prevStore
	})

	repo := newRunnerTestGitRepo(t)
	workFile := filepath.Join(repo, "work.txt")

	// Two write turns then exhaustion; with no turn checkpoints there is
	// nothing to restore, so the attempt fails.
	provider := &scriptedProvider{responses: []*types.ChatResponse{
		writeToolCallResponse("tc1", workFile, "v1"),
		writeToolCallResponse("tc2", workFile, "corrupted"),
	}}

	opts := taskRunnerOpts{
		provider:     provider,
		systemPrompt: "BASE",
		modelName:    "test-model",
		// No Roles["developer"].TurnCheckpoints — gate must stay closed.
		defaults: config.Defaults{SupervisedRepoPath: repo},
	}

	tool := &tools.SupervisedTaskTool{
		Runner:     makeTaskRunner(opts, 1),
		RoleNames:  []string{"developer"},
		RepoPath:   repo,
		MaxRetries: 0,
	}

	result, err := tool.Execute(context.Background(),
		`{"prompt":"do the work","role":"developer","max_iterations":2}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var out struct {
		Status   string `json:"status"`
		Attempts int    `json:"attempts"`
		Restores int    `json:"restores"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, result)
	}

	if out.Restores != 0 {
		t.Errorf("restores = %d, want 0 (turn_checkpoints not set for role)", out.Restores)
	}
	if out.Status != "failed" {
		t.Errorf("status = %q, want failed (exhaustion with no turn restore)", out.Status)
	}
}
