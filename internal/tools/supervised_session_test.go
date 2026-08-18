package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// sessionRunner is a jobs.TaskRunner stand-in for review-session tests.
// Beyond replaying responses it mimics the real sub-agent runner's
// conversation contract: it seeds from params.SeedMessages, appends the
// prompt as a user message plus its reply as an assistant message, and
// writes the full history to the context's capture pointer.
type sessionRunner struct {
	mu        sync.Mutex
	responses []runnerResponse
	prompts   []string
	seeds     [][]types.Message
	// sideEffect runs before each response is returned so tests can
	// mutate the workspace like a real sub-agent would.
	sideEffect func(call int, repoPath string)
	repoPath   string
}

func (f *sessionRunner) run() TaskRunner {
	return func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
		f.mu.Lock()
		idx := len(f.prompts)
		f.prompts = append(f.prompts, prompt)
		f.seeds = append(f.seeds, params.SeedMessages)
		resp := runnerResponse{result: "default"}
		if idx < len(f.responses) {
			resp = f.responses[idx]
		}
		f.mu.Unlock()

		if f.sideEffect != nil {
			f.sideEffect(idx, f.repoPath)
		}

		conv := append([]types.Message(nil), params.SeedMessages...)
		conv = append(conv, types.UserMsg(prompt))
		if resp.err == nil && strings.TrimSpace(resp.result) != "" {
			conv = append(conv, types.Message{Role: "assistant", Content: resp.result})
		}
		WriteConversationCapture(ctx, conv)

		return resp.result, resp.err
	}
}

func (f *sessionRunner) promptFor(t *testing.T, call int) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if call >= len(f.prompts) {
		t.Fatalf("no prompt recorded for call %d (have %d)", call, len(f.prompts))
	}
	return f.prompts[call]
}

func (f *sessionRunner) seedFor(t *testing.T, call int) []types.Message {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if call >= len(f.seeds) {
		t.Fatalf("no seed recorded for call %d (have %d)", call, len(f.seeds))
	}
	return f.seeds[call]
}

func (f *sessionRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

// decodeReviewEnvelope unmarshals a review envelope and fails the test
// on invalid JSON.
func decodeReviewEnvelope(t *testing.T, result string) reviewEnvelope {
	t.Helper()
	var env reviewEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("review envelope is not JSON: %v\n%s", err, result)
	}
	return env
}

// startTestReviewSession starts a review session via the tool and
// returns the first envelope's session ID.
func startTestReviewSession(t *testing.T, tool *SupervisedTaskTool, prompt string) string {
	t.Helper()
	result, err := tool.Execute(context.Background(), `{"prompt":`+jsonString(prompt)+`,"role":"worker","review":true}`)
	if err != nil {
		t.Fatalf("start review session: %v", err)
	}
	env := decodeReviewEnvelope(t, result)
	if env.Status != "review" {
		t.Fatalf("start status = %q, want review (env: %s)", env.Status, result)
	}
	return env.SessionID
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustSupervisorExecute(t *testing.T, tool *SupervisorTool, args string) reviewEnvelope {
	t.Helper()
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("supervisor %s: %v", args, err)
	}
	return decodeReviewEnvelope(t, result)
}

func newReviewTestEnv(t *testing.T) (*SupervisedTaskTool, *SupervisorTool, string) {
	t.Helper()
	// The session registry is package-global; isolate every test.
	resetReviewSessions()
	t.Cleanup(resetReviewSessions)
	tool, repo := newSupervisedTestEnv(t)
	return tool, &SupervisorTool{TraceDir: "unused"}, repo
}

func TestReviewSession_StartEnvelopeAndDiff(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "implemented the feature"},
		},
	}
	runner.sideEffect = func(call int, repoPath string) {
		if call == 0 {
			writeTestFile(t, repoPath, "feature.go", "package main\n")
		}
	}
	tool.Runner = runner.run()

	result, err := tool.Execute(context.Background(), `{"prompt":"add the feature","role":"worker","review":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeReviewEnvelope(t, result)

	if env.Status != "review" {
		t.Errorf("status = %q, want review", env.Status)
	}
	if env.SessionID == "" {
		t.Fatal("session_id missing")
	}
	if env.Unit != 1 {
		t.Errorf("unit = %d, want 1", env.Unit)
	}
	if env.Result != "implemented the feature" {
		t.Errorf("result = %q", env.Result)
	}
	if !strings.Contains(env.Diff, "feature.go") {
		t.Errorf("diff should mention feature.go:\n%s", env.Diff)
	}
	if len(env.Files) != 1 || env.Files[0] != "feature.go" {
		t.Errorf("files = %v, want [feature.go]", env.Files)
	}
	for _, want := range []string{"continue", "rollback", "fork", "accept", "abort"} {
		if !containsString(env.Next, want) {
			t.Errorf("next = %v, want %s", env.Next, want)
		}
	}

	// The session is registered: review_diff works.
	diffEnv := mustSupervisorExecute(t, sup, `{"action":"review_diff","session_id":`+jsonString(env.SessionID)+`}`)
	if diffEnv.Status != "review_diff" || !strings.Contains(diffEnv.Diff, "feature.go") {
		t.Errorf("review_diff env = %+v", diffEnv)
	}
}

func TestReviewSession_ContinueSeedsConversation(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one done"},
			{result: "unit two done"},
		},
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "start the work")

	env := mustSupervisorExecute(t, sup, `{"action":"continue","session_id":`+jsonString(sid)+`,"guidance":"now add tests"}`)
	if env.Status != "review" {
		t.Errorf("status = %q, want review", env.Status)
	}
	if env.Unit != 2 {
		t.Errorf("unit = %d, want 2", env.Unit)
	}

	// The second dispatch must be seeded with the first unit's
	// conversation and carry the guidance as its user message.
	prompt2 := runner.promptFor(t, 1)
	if !strings.Contains(prompt2, "now add tests") {
		t.Errorf("prompt 2 = %q, want guidance included", prompt2)
	}
	seed2 := runner.seedFor(t, 1)
	if len(seed2) == 0 {
		t.Fatal("continue dispatch must seed prior conversation, got empty seed")
	}
	last := seed2[len(seed2)-1]
	if last.Role != "assistant" || last.Content != "unit one done" {
		t.Errorf("seed ends with %q/%q, want assistant/unit one done", last.Role, last.Content)
	}

	// First dispatch was unseeded.
	if len(runner.seedFor(t, 0)) != 0 {
		t.Error("first dispatch must not be seeded")
	}
}

func TestReviewSession_RollbackRevertsFilesAndConversation(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	badFile := filepath.Join(repo, "wrong-approach.txt")

	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "i chose violence"},
			{result: "corrected implementation"},
		},
	}
	runner.sideEffect = func(call int, repoPath string) {
		if call == 0 {
			writeTestFile(t, repoPath, "wrong-approach.txt", "bad idea\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "refactor the parser")

	env := mustSupervisorExecute(t, sup, `{"action":"rollback","session_id":`+jsonString(sid)+`,"guidance":"use a recursive descent parser instead"}`)
	if env.Status != "review" {
		t.Errorf("status = %q, want review", env.Status)
	}

	// The bad file must be gone: the workspace rewound to unit start.
	if _, err := os.Stat(badFile); !os.IsNotExist(err) {
		t.Errorf("wrong-approach.txt should be rolled back, stat err = %v", err)
	}

	// The conversation must be rewound too: the corrected dispatch is
	// seeded with the unit-start conversation (empty here), not the
	// failed unit's history.
	seed2 := runner.seedFor(t, 1)
	if len(seed2) != 0 {
		t.Errorf("rollback dispatch seed = %d messages, want 0 (conversation rewound to unit start)", len(seed2))
	}

	prompt2 := runner.promptFor(t, 1)
	for _, want := range []string{"rolled back", "recursive descent parser"} {
		if !strings.Contains(prompt2, want) {
			t.Errorf("rollback prompt missing %q:\n%s", want, prompt2)
		}
	}
}

func TestReviewSession_RollbackKeepsPriorConversation(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one done"},
			{result: "unit two went sideways"},
			{result: "unit two fixed"},
		},
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "start")

	// Accept unit 1, then roll back unit 2.
	mustSupervisorExecute(t, sup, `{"action":"continue","session_id":`+jsonString(sid)+`}`)
	env := mustSupervisorExecute(t, sup, `{"action":"rollback","session_id":`+jsonString(sid)+`,"guidance":"narrow the scope"}`)
	if env.Status != "review" {
		t.Fatalf("status = %q, want review", env.Status)
	}

	// The corrected unit-2 dispatch must continue from unit 1's
	// accepted conversation (the "last good point"), not the failed
	// unit 2 history.
	seed3 := runner.seedFor(t, 2)
	if len(seed3) == 0 {
		t.Fatal("rollback after continue must seed the prior accepted conversation")
	}
	last := seed3[len(seed3)-1]
	if last.Role != "assistant" || last.Content != "unit one done" {
		t.Errorf("seed ends with %q/%q, want assistant/unit one done (last good point)", last.Role, last.Content)
	}
}

func TestReviewSession_ForkAndChooseA(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)

	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one done"},
			{result: "variant A implementation"},
			{result: "variant B implementation"},
		},
	}
	runner.sideEffect = func(call int, repoPath string) {
		switch call {
		case 1:
			writeTestFile(t, repoPath, "variant-a.txt", "A\n")
		case 2:
			writeTestFile(t, repoPath, "variant-b.txt", "B\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "prepare the ground")

	forkEnv := mustSupervisorExecute(t, sup, `{"action":"fork","session_id":`+jsonString(sid)+`,"prompt_a":"implement with approach A","prompt_b":"implement with approach B"}`)
	if forkEnv.Status != "awaiting_choose" {
		t.Fatalf("fork status = %q, want awaiting_choose", forkEnv.Status)
	}
	varA, okA := forkEnv.Variants["a"]
	varB, okB := forkEnv.Variants["b"]
	if !okA || !okB {
		t.Fatalf("fork envelope must carry both variants, got %+v", forkEnv.Variants)
	}
	if !strings.Contains(varA.Diff, "variant-a.txt") {
		t.Errorf("variant a diff should mention variant-a.txt:\n%s", varA.Diff)
	}
	if !strings.Contains(varB.Diff, "variant-b.txt") {
		t.Errorf("variant b diff should mention variant-b.txt:\n%s", varB.Diff)
	}

	// After fork, the workspace sits at the fork point (post-B reset).
	if _, err := os.Stat(filepath.Join(repo, "variant-b.txt")); !os.IsNotExist(err) {
		t.Errorf("fork point should be restored after variant B, stat err = %v", err)
	}

	chooseEnv := mustSupervisorExecute(t, sup, `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`)
	if chooseEnv.Status != "chosen" {
		t.Fatalf("choose status = %q, want chosen", chooseEnv.Status)
	}
	if !strings.Contains(chooseEnv.Diff, "variant-a.txt") {
		t.Errorf("chosen env diff should reflect variant A:\n%s", chooseEnv.Diff)
	}

	// Winner's tree applied, loser's gone.
	if _, err := os.Stat(filepath.Join(repo, "variant-a.txt")); err != nil {
		t.Errorf("variant-a.txt should exist after choose a: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "variant-b.txt")); !os.IsNotExist(err) {
		t.Errorf("variant-b.txt should be gone after choose a, stat err = %v", err)
	}

	// Session resumes review: rollback after choose rewinds to the
	// winner's own checkpoint (taken at choose time), so the chosen
	// tree survives — the next unit runs from that point.
	rbEnv := mustSupervisorExecute(t, sup, `{"action":"rollback","session_id":`+jsonString(sid)+`,"guidance":"try again"}`)
	if rbEnv.Status != "review" {
		t.Errorf("rollback-after-choose status = %q, want review", rbEnv.Status)
	}
	if _, err := os.Stat(filepath.Join(repo, "variant-a.txt")); err != nil {
		t.Errorf("rollback after choose rewinds to the chosen state, so variant-a.txt must survive: %v", err)
	}
}

func TestReviewSession_ForkAndChooseB(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)

	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "unit one done"},
			{result: "variant A implementation"},
			{result: "variant B implementation"},
		},
	}
	runner.sideEffect = func(call int, repoPath string) {
		switch call {
		case 1:
			writeTestFile(t, repoPath, "variant-a.txt", "A\n")
		case 2:
			writeTestFile(t, repoPath, "variant-b.txt", "B\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "prepare")

	mustSupervisorExecute(t, sup, `{"action":"fork","session_id":`+jsonString(sid)+`,"prompt_a":"A","prompt_b":"B"}`)
	chooseEnv := mustSupervisorExecute(t, sup, `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"b"}`)
	if chooseEnv.Status != "chosen" {
		t.Fatalf("choose status = %q, want chosen", chooseEnv.Status)
	}
	if _, err := os.Stat(filepath.Join(repo, "variant-b.txt")); err != nil {
		t.Errorf("variant-b.txt should exist after choose b: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "variant-a.txt")); !os.IsNotExist(err) {
		t.Errorf("variant-a.txt should be gone after choose b, stat err = %v", err)
	}
}

func TestReviewSession_ForkVariantsSeedFromForkConversation(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "groundwork done"},
			{result: "unit two output"}, // accepted via continue; fork rewinds to its start
			{result: "A"},
			{result: "B"},
		},
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "prepare")
	mustSupervisorExecute(t, sup, `{"action":"continue","session_id":`+jsonString(sid)+`}`)
	mustSupervisorExecute(t, sup, `{"action":"fork","session_id":`+jsonString(sid)+`,"prompt_a":"approach A","prompt_b":"approach B"}`)

	// Both variants must be seeded with the fork-point conversation only
	// (the state at unit 2's start: unit 1's accepted exchange). The
	// variant prompt is passed as the runner's user input — appending it
	// to the seed too would duplicate the user message.
	for _, call := range []int{2, 3} {
		seed := runner.seedFor(t, call)
		if len(seed) == 0 {
			t.Fatalf("variant dispatch %d must seed fork conversation", call)
		}
		last := seed[len(seed)-1]
		if last.Role != "assistant" || last.Content != "groundwork done" {
			t.Errorf("variant dispatch %d seed must end with the fork-point assistant message, got %q/%q", call, last.Role, last.Content)
		}
		for _, m := range seed {
			if m.Role == "user" && strings.Contains(m.Content, "approach") {
				t.Errorf("variant dispatch %d seed must not embed the variant prompt (runner appends it): %q", call, m.Content)
			}
		}
	}
	if got := runner.promptFor(t, 2); got != "approach A" {
		t.Errorf("variant A prompt = %q, want %q", got, "approach A")
	}
	if got := runner.promptFor(t, 3); got != "approach B" {
		t.Errorf("variant B prompt = %q, want %q", got, "approach B")
	}
}

func TestReviewSession_AbortRestoresAndCloses(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	badFile := filepath.Join(repo, "bad.txt")

	runner := &sessionRunner{
		repoPath:  repo,
		responses: []runnerResponse{{result: "oops"}},
	}
	runner.sideEffect = func(call int, repoPath string) {
		if call == 0 {
			writeTestFile(t, repoPath, "bad.txt", "junk\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "do the thing")

	env := mustSupervisorExecute(t, sup, `{"action":"abort","session_id":`+jsonString(sid)+`}`)
	if env.Status != "aborted" {
		t.Errorf("status = %q, want aborted", env.Status)
	}
	if _, err := os.Stat(badFile); !os.IsNotExist(err) {
		t.Errorf("bad.txt should be rewound by abort, stat err = %v", err)
	}

	// Session closed: further verdicts fail.
	if _, err := sup.Execute(context.Background(), `{"action":"continue","session_id":`+jsonString(sid)+`}`); err == nil {
		t.Error("continue on a closed session must fail")
	}

	// A new review session can start now.
	sid2 := startTestReviewSession(t, tool, "fresh start")
	if sid2 == sid {
		t.Error("new session must have a new id")
	}
}

func TestReviewSession_SecondSessionRejected(t *testing.T) {
	tool, _, repo := newReviewTestEnv(t)
	runner := &sessionRunner{repoPath: repo}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "first")

	_, err := tool.Execute(context.Background(), `{"prompt":"second","role":"worker","review":true}`)
	if err == nil {
		t.Fatal("second concurrent review session must be rejected")
	}
	if !strings.Contains(err.Error(), sid) {
		t.Errorf("error should name the active session %q: %v", sid, err)
	}
}

func TestReviewSession_CancelledMidUnitStaysResumable(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath: repo,
		responses: []runnerResponse{
			{result: "partial work"},
			{result: "resumed fine"},
		},
	}
	tool.Runner = runner.run()

	// Parent cancellation before/during the unit: the envelope reports
	// cancelled and the session stays registered with its checkpoint.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := tool.Execute(ctx, `{"prompt":"start","role":"worker","review":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	env := decodeReviewEnvelope(t, result)
	if env.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", env.Status)
	}

	// The session stays registered with its checkpoint: a later
	// rollback works with a fresh context.
	rbEnv := mustSupervisorExecute(t, sup, `{"action":"rollback","session_id":`+jsonString(env.SessionID)+`,"guidance":"narrower prompt"}`)
	if rbEnv.Status != "review" {
		t.Errorf("rollback after cancel status = %q, want review", rbEnv.Status)
	}
}

func TestReviewSession_ChooseRequiresFork(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{repoPath: repo, responses: []runnerResponse{{result: "done"}}}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "start")

	if _, err := sup.Execute(context.Background(), `{"action":"choose","session_id":`+jsonString(sid)+`,"winner":"a"}`); err == nil {
		t.Error("choose without a fork must fail")
	}
}

func TestReviewSession_RollbackRequiresGuidance(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{repoPath: repo, responses: []runnerResponse{{result: "done"}}}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "start")

	if _, err := sup.Execute(context.Background(), `{"action":"rollback","session_id":`+jsonString(sid)+`}`); err == nil {
		t.Error("rollback without guidance must fail")
	}
}

func TestReviewSession_UnknownSession(t *testing.T) {
	_, sup, _ := newReviewTestEnv(t)
	if _, err := sup.Execute(context.Background(), `{"action":"continue","session_id":"nope"}`); err == nil {
		t.Error("unknown session must fail")
	}
}

func TestReviewSession_AcceptClosesAndKeepsWork(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	goodFile := filepath.Join(repo, "good.txt")

	runner := &sessionRunner{repoPath: repo, responses: []runnerResponse{{result: "good work"}}}
	runner.sideEffect = func(call int, repoPath string) {
		if call == 0 {
			writeTestFile(t, repoPath, "good.txt", "keep me\n")
		}
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "do good work")

	env := mustSupervisorExecute(t, sup, `{"action":"accept","session_id":`+jsonString(sid)+`}`)
	if env.Status != "accepted" {
		t.Errorf("status = %q, want accepted", env.Status)
	}
	if env.Result != "good work" {
		t.Errorf("final result = %q, want the last unit's report", env.Result)
	}
	if _, err := os.Stat(goodFile); err != nil {
		t.Errorf("accepted work must be kept: %v", err)
	}
	if _, err := sup.Execute(context.Background(), `{"action":"accept","session_id":`+jsonString(sid)+`}`); err == nil {
		t.Error("accept on a closed session must fail")
	}
}

func TestReviewSession_ContinueDefaultGuidance(t *testing.T) {
	tool, sup, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath:  repo,
		responses: []runnerResponse{{result: "one"}, {result: "two"}},
	}
	tool.Runner = runner.run()

	sid := startTestReviewSession(t, tool, "start")

	// continue without guidance must not error.
	env := mustSupervisorExecute(t, sup, `{"action":"continue","session_id":`+jsonString(sid)+`}`)
	if env.Status != "review" {
		t.Errorf("status = %q, want review", env.Status)
	}
	if !strings.Contains(runner.promptFor(t, 1), "next unit") {
		t.Errorf("default continuation prompt should mention the next unit: %q", runner.promptFor(t, 1))
	}
}

func writeTestFile(t *testing.T, repo, name, content string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestSupervisorTool_SchemaExposesReviewActions(t *testing.T) {
	var schema struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
			SessionID any `json:"session_id"`
			PromptA   any `json:"prompt_a"`
			PromptB   any `json:"prompt_b"`
			Winner    any `json:"winner"`
			Restore   any `json:"restore"`
		} `json:"properties"`
	}
	if err := json.Unmarshal((&SupervisorTool{}).Schema(), &schema); err != nil {
		t.Fatalf("supervisor schema not valid JSON: %v", err)
	}

	for _, want := range []string{"list_scopes", "inject", "halt", "status", "continue", "rollback", "review_diff", "fork", "choose", "accept", "abort"} {
		if !containsString(schema.Properties.Action.Enum, want) {
			t.Errorf("supervisor action enum missing %q: %v", want, schema.Properties.Action.Enum)
		}
	}
	if schema.Properties.SessionID == nil {
		t.Error("supervisor schema must expose session_id for review verdicts")
	}
	if schema.Properties.PromptA == nil || schema.Properties.PromptB == nil {
		t.Error("supervisor schema must expose prompt_a/prompt_b for fork")
	}
	if schema.Properties.Winner == nil {
		t.Error("supervisor schema must expose winner for choose")
	}
	if schema.Properties.Restore == nil {
		t.Error("supervisor schema must expose restore for abort")
	}
}

func TestSupervisedTask_SchemaReviewParam(t *testing.T) {
	tool := &SupervisedTaskTool{RoleNames: []string{"worker"}}
	var schema struct {
		Properties struct {
			Review any `json:"review"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("supervised_task schema not valid JSON: %v", err)
	}
	if schema.Properties.Review == nil {
		t.Error("supervised_task schema must expose the review boolean param")
	}
}

// TestReviewSession_EmptyResultStatus verifies a silent sub-agent unit
// surfaces as status "empty" for review rather than an error.
func TestReviewSession_EmptyResultStatus(t *testing.T) {
	tool, _, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath:  repo,
		responses: []runnerResponse{{result: ""}},
	}
	tool.Runner = runner.run()

	result, err := tool.Execute(context.Background(), `{"prompt":"stay quiet","role":"worker","review":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := decodeReviewEnvelope(t, result)
	if env.Status != "empty" {
		t.Errorf("status = %q, want empty", env.Status)
	}
}

// TestReviewSession_ErrorResultStillReviewable verifies a hard unit
// error does not auto-retry: the envelope carries the error and the
// verdict options.
func TestReviewSession_ErrorResultStillReviewable(t *testing.T) {
	tool, _, repo := newReviewTestEnv(t)
	runner := &sessionRunner{
		repoPath:  repo,
		responses: []runnerResponse{{result: "", err: errors.New("tool exploded")}},
	}
	tool.Runner = runner.run()

	result, err := tool.Execute(context.Background(), `{"prompt":"break things","role":"worker","review":true}`)
	if err != nil {
		t.Fatalf("Execute must return a review envelope, not a Go error: %v", err)
	}
	env := decodeReviewEnvelope(t, result)
	if env.Status != "review" {
		t.Errorf("status = %q, want review (errors are reviewable, not fatal)", env.Status)
	}
	if !strings.Contains(env.Error, "tool exploded") {
		t.Errorf("error = %q, want the unit failure", env.Error)
	}
	if runner.callCount() != 1 {
		t.Errorf("runner called %d times, want 1 (no auto-retry in review mode)", runner.callCount())
	}
}
