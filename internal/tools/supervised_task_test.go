package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// runnerResponse configures one fake sub-agent attempt.
type runnerResponse struct {
	result string
	err    error
}

// fakeRunner is a jobs.TaskRunner stand-in that replays configured
// responses and records every call.
type fakeRunner struct {
	mu        sync.Mutex
	responses []runnerResponse
	calls     int
	prompts   []string
	// sideEffect, when set, runs during each attempt (before the
	// response is returned) so tests can mutate the workspace like a
	// real sub-agent would.
	sideEffect func(call int, repoPath string)
	repoPath   string
}

func (f *fakeRunner) run() TaskRunner {
	return func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
		f.mu.Lock()
		idx := f.calls
		f.calls++
		f.prompts = append(f.prompts, prompt)
		f.mu.Unlock()
		if f.sideEffect != nil {
			f.sideEffect(idx, f.repoPath)
		}
		if idx >= len(f.responses) {
			return "default", nil
		}
		return f.responses[idx].result, f.responses[idx].err
	}
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// decodeSupervisedResult unmarshals the uniform tool envelope and fails the
// test if the result is not valid JSON.
func decodeSupervisedResult(t *testing.T, result string) struct {
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Result   string `json:"result"`
	Error    string `json:"error"`
	Partial  string `json:"partial"`
} {
	t.Helper()
	var out struct {
		Status   string `json:"status"`
		Attempts int    `json:"attempts"`
		Result   string `json:"result"`
		Error    string `json:"error"`
		Partial  string `json:"partial"`
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, result)
	}
	return out
}

func (f *fakeRunner) promptFor(t *testing.T, call int) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if call >= len(f.prompts) {
		t.Fatalf("no prompt recorded for call %d (have %d)", call, len(f.prompts))
	}
	return f.prompts[call]
}

// newSupervisedTestEnv builds a SupervisedTaskTool backed by a real
// ScopeManager (in-memory store) and a real temp git repo, swapping the
// SharedScopeManager global for the duration of the test.
func newSupervisedTestEnv(t *testing.T) (*SupervisedTaskTool, string) {
	t.Helper()

	store, err := shepherd.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	prev := SharedScopeManager
	SharedScopeManager = shepherd.NewScopeManager(store)
	t.Cleanup(func() { SharedScopeManager = prev })

	repo := newTestGitRepo(t)

	return &SupervisedTaskTool{
		RoleNames:  []string{"worker"},
		RepoPath:   repo,
		MaxRetries: 1,
	}, repo
}

func newTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	dir := t.TempDir()

	// Isolate from the developer's global/system git config so inherited
	// settings (gpgsign, hooksPath, templates) cannot break the test.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))

	mustRunGit(t, dir, "init")
	mustRunGit(t, dir, "config", "user.email", "test@test.com")
	mustRunGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustRunGit(t, dir, "add", "-A")
	mustRunGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", args[0], dir, err, out)
	}
}

func TestSupervisedTask_Success(t *testing.T) {
	tool, _ := newSupervisedTestEnv(t)
	runner := &fakeRunner{responses: []runnerResponse{{result: "all done"}}}
	tool.Runner = runner.run()

	result, err := tool.Execute(context.Background(), `{"prompt":"fix the bug","role":"worker"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := decodeSupervisedResult(t, result)
	if out.Status != "completed" {
		t.Errorf("status = %q, want completed", out.Status)
	}
	if out.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", out.Attempts)
	}
	if out.Result != "all done" {
		t.Errorf("result = %q, want %q (no retry suffix on first-attempt success)", out.Result, "all done")
	}
	if runner.callCount() != 1 {
		t.Errorf("runner called %d times, want 1 (no rollback on success)", runner.callCount())
	}
}

func TestSupervisedTask_RetrySucceeds(t *testing.T) {
	tool, _ := newSupervisedTestEnv(t)
	runner := &fakeRunner{responses: []runnerResponse{
		{result: "", err: errors.New("first attempt blew up")},
		{result: "fixed it"},
	}}
	tool.Runner = runner.run()

	result, err := tool.Execute(context.Background(), `{"prompt":"fix the bug","role":"worker"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := decodeSupervisedResult(t, result)
	if out.Status != "completed" {
		t.Errorf("status = %q, want completed", out.Status)
	}
	if out.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", out.Attempts)
	}
	if !strings.HasPrefix(out.Result, "fixed it") {
		t.Errorf("result = %q, want prefix %q", out.Result, "fixed it")
	}
	if !strings.Contains(out.Result, "attempt 2") {
		t.Errorf("result should mention the retry attempt, got %q", out.Result)
	}
	if runner.callCount() != 2 {
		t.Errorf("runner called %d times, want 2", runner.callCount())
	}

	// The retry prompt carries the failure guidance.
	retryPrompt := runner.promptFor(t, 1)
	if !strings.Contains(retryPrompt, "first attempt blew up") {
		t.Errorf("retry prompt should carry the failure error, got %q", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "rolled back") {
		t.Errorf("retry prompt should mention the rollback, got %q", retryPrompt)
	}
}

func TestSupervisedTask_AllRetriesFail(t *testing.T) {
	tool, _ := newSupervisedTestEnv(t)
	runner := &fakeRunner{responses: []runnerResponse{
		{result: "partial work", err: errors.New("boom 1")},
		{result: "", err: errors.New("boom 2")},
	}}
	tool.Runner = runner.run()

	result, err := tool.Execute(context.Background(), `{"prompt":"fix the bug","role":"worker"}`)
	if err != nil {
		t.Fatalf("Execute should return a structured result, not a Go error: %v", err)
	}

	out := decodeSupervisedResult(t, result)
	if out.Status != "failed" {
		t.Errorf("status = %q, want failed", out.Status)
	}
	if out.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (1 + MaxRetries)", out.Attempts)
	}
	if out.Error == "" {
		t.Error("error should carry the last failure")
	}
	if out.Partial == "" {
		t.Errorf("partial output should carry %q, got empty", "partial work")
	}
	if runner.callCount() != 2 {
		t.Errorf("runner called %d times, want 2", runner.callCount())
	}
}

func TestSupervisedTask_RollbackRestoresWorkspace(t *testing.T) {
	tool, repo := newSupervisedTestEnv(t)
	badFile := filepath.Join(repo, "bad.txt")

	runner := &fakeRunner{
		responses: []runnerResponse{
			{result: "", err: errors.New("made a mess")},
			{result: "clean success"},
		},
	}
	runner.repoPath = repo
	runner.sideEffect = func(call int, repoPath string) {
		if call == 0 {
			// First attempt pollutes the workspace.
			if err := os.WriteFile(badFile, []byte("junk"), 0o644); err != nil {
				t.Errorf("write bad file: %v", err)
			}
		}
	}
	tool.Runner = runner.run()

	result, err := tool.Execute(context.Background(), `{"prompt":"fix the bug","role":"worker"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := decodeSupervisedResult(t, result)
	if out.Status != "completed" {
		t.Errorf("status = %q, want completed", out.Status)
	}
	if out.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", out.Attempts)
	}
	if !strings.HasPrefix(out.Result, "clean success") {
		t.Errorf("result = %q, want prefix %q", out.Result, "clean success")
	}

	// The rollback must have removed the first attempt's file before the
	// second attempt ran — and it must still be gone after success.
	if _, err := os.Stat(badFile); !os.IsNotExist(err) {
		t.Errorf("bad.txt should have been rolled back, stat err = %v", err)
	}

	// The committed README from before the checkpoint must survive.
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Errorf("README.md lost after rollback: %v", err)
	}
}

func TestSupervisedTask_NoScopeManager(t *testing.T) {
	prev := SharedScopeManager
	SharedScopeManager = nil
	t.Cleanup(func() { SharedScopeManager = prev })

	tool := &SupervisedTaskTool{
		Runner:    (&fakeRunner{}).run(),
		RoleNames: []string{"worker"},
	}

	_, err := tool.Execute(context.Background(), `{"prompt":"fix the bug","role":"worker"}`)
	if err == nil {
		t.Fatal("expected an error when no scope manager is configured")
	}
	if !strings.Contains(err.Error(), "shepherd tracing not enabled") {
		t.Errorf("error = %q, want mention of shepherd tracing", err.Error())
	}
}

func TestSupervisedTask_RoleValidation(t *testing.T) {
	tool, _ := newSupervisedTestEnv(t)
	tool.Runner = (&fakeRunner{}).run()

	t.Run("empty role", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), `{"prompt":"fix the bug","role":""}`)
		if err == nil {
			t.Fatal("expected an error for an empty role")
		}
		if !strings.Contains(err.Error(), "role is required") {
			t.Errorf("error = %q, want role-required message", err.Error())
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), `{"prompt":"fix the bug","role":"doesnotexist"}`)
		if err == nil {
			t.Fatal("expected an error for an unknown role")
		}
		if !strings.Contains(err.Error(), "worker") {
			t.Errorf("error = %q, want the list of valid roles", err.Error())
		}
	})
}

func TestSupervisedTask_MissingPrompt(t *testing.T) {
	tool, _ := newSupervisedTestEnv(t)
	tool.Runner = (&fakeRunner{}).run()

	_, err := tool.Execute(context.Background(), `{"role":"worker"}`)
	if err == nil {
		t.Fatal("expected an error for a missing prompt")
	}
	if !strings.Contains(err.Error(), "prompt is required") {
		t.Errorf("error = %q, want prompt-required message", err.Error())
	}
}

func TestSupervisedTask_BuildRetryPrompt(t *testing.T) {
	prompt := buildRetryPrompt("fix the auth bug", 0, "half-finished output", errors.New("tests failed"))

	for _, want := range []string{
		"fix the auth bug",
		"attempt 1 failed",
		"tests failed",
		"half-finished output",
		"rolled back",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("retry prompt missing %q:\n%s", want, prompt)
		}
	}

	// Long partial output is truncated.
	long := strings.Repeat("x", 5000)
	prompt = buildRetryPrompt("task", 0, long, nil)
	if strings.Count(prompt, "x") > 2100 {
		t.Error("retry prompt should truncate long partial output")
	}
	if !strings.Contains(prompt, "...[truncated]") {
		t.Error("truncated output should carry a truncation marker")
	}
}

func TestSupervisedTask_SchemaRoleEnum(t *testing.T) {
	tool := &SupervisedTaskTool{
		RoleNames:        []string{"worker", "reviewer"},
		RoleDescriptions: map[string]string{"worker": "does work"},
	}

	var schema struct {
		Properties struct {
			Role struct {
				Enum []string `json:"enum"`
			} `json:"role"`
			Background any `json:"background"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("Schema() is not valid JSON: %v", err)
	}

	if len(schema.Properties.Role.Enum) != 2 {
		t.Errorf("role enum = %v, want worker + reviewer", schema.Properties.Role.Enum)
	}
	if schema.Properties.Background != nil {
		t.Error("supervised_task schema must not expose a background option")
	}
	if len(schema.Required) != 2 || schema.Required[0] != "prompt" || schema.Required[1] != "role" {
		t.Errorf("required = %v, want [prompt role]", schema.Required)
	}
}
