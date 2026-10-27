package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
)

func TestExecute_RoleValidation(t *testing.T) {
	var gotRole string
	tt := &DelegateTool{
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
		if err == nil || !IsRoleNotFound(err) {
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
	tt := &DelegateTool{
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
		if err == nil || !IsRoleNotFound(err) {
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
	requiredHasRole(t, (&DelegateTool{RoleNames: []string{"analyst"}}).Schema())
	// Fallback schema (no registry).
	requiredHasRole(t, (&DelegateTool{}).Schema())
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

// TestExecute_BackgroundViaManager verifies the background:true path
// dispatches through BackgroundJobs, returns immediately with a job id,
// and the runner is invoked.
func TestExecute_BackgroundViaManager(t *testing.T) {
	mgr := jobs.NewBackgroundJobs()
	defer mgr.Close()

	var ran atomic.Bool
	var mu sync.Mutex
	var delivered string
	mgr.Deliver = func(role, desc, res string, err error) {
		mu.Lock()
		delivered = res
		mu.Unlock()
	}

	tt := &DelegateTool{
		RoleNames:      []string{"analyst"},
		BackgroundJobs: mgr,
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			ran.Store(true)
			WriteSubAgentModel(ctx, "bg-model")
			return "bg-result: " + prompt, nil
		},
	}

	res, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"analyst","background":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Immediate result is the running placeholder carrying a job id.
	var parsed struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal([]byte(res), &parsed); err != nil {
		t.Fatalf("parse result: %v\n%s", err, res)
	}
	if parsed.Status != "running" {
		t.Errorf("status = %q, want running", parsed.Status)
	}
	if parsed.JobID == "" {
		t.Error("expected non-empty job_id in background result")
	}

	// The runner must eventually run and deliver its result.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !ran.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	if !ran.Load() {
		t.Fatal("background runner was never invoked")
	}

	// Wait for delivery.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		d := delivered
		mu.Unlock()
		if d != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if delivered != "bg-result: p" {
		t.Errorf("delivered = %q, want %q", delivered, "bg-result: p")
	}
}

// TestExecute_BackgroundWithoutManagerErrors verifies that requesting
// background mode without a manager yields a clear error (e.g. in a
// context where background is unavailable).
func TestExecute_BackgroundWithoutManagerErrors(t *testing.T) {
	tt := &DelegateTool{
		RoleNames: []string{"analyst"},
		Runner: func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
			return "should not run", nil
		},
	}
	_, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"analyst","background":true}`)
	if err == nil {
		t.Fatal("expected error when background requested without a manager, got nil")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("error = %q, want it to mention unavailability", err.Error())
	}
}

// TestExecute_BackgroundRetainedReturnsProposal: background:true must not be the
// way around the retained lane. A job that ran the raw runner would mutate the
// host tree with no proposal — the guarantee "retained" exists to make — so the
// job is launched through startRetainedRun and its delivered result is a proposal
// envelope like any other dispatch's.
func TestExecute_BackgroundRetainedReturnsProposal(t *testing.T) {
	resetRetainedProposals()

	mgr := jobs.NewBackgroundJobs()
	defer mgr.Close()

	var mu sync.Mutex
	var delivered string
	mgr.Deliver = func(role, desc, res string, err error) {
		mu.Lock()
		delivered = res
		mu.Unlock()
	}

	runner := writingRunner(t, "bg.txt")
	rt := newRetainedTestEnv(t, runner)

	tt := &DelegateTool{
		RoleNames:      []string{"developer"},
		BackgroundJobs: mgr,
		Runner:         runner.run(),
		Retained:       &rt,
	}
	if _, err := tt.Execute(context.Background(),
		`{"description":"d","prompt":"p","role":"developer","background":true}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		d := delivered
		mu.Unlock()
		if d != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	got := delivered
	mu.Unlock()
	if got == "" {
		t.Fatal("the background job delivered nothing")
	}
	env := decodeProposalEnvelope(t, got)
	if env.Status != "proposal" || env.ProposalID == "" {
		t.Fatalf("a background dispatch must deliver a proposal, got %+v", env)
	}

	// The isolated world took the write; the host tree did not.
	wd := runner.workdirFor(t, 0)
	if wd == "" || wd == rt.RepoPath {
		t.Fatalf("the background dispatch must run in an isolated worktree, got %q", wd)
	}
	if _, err := os.Stat(filepath.Join(wd, "bg.txt")); err != nil {
		t.Errorf("the worktree should hold the change: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rt.RepoPath, "bg.txt")); err == nil {
		t.Error("the host tree must not be touched before settlement")
	}
}

// TestExecute_TimeoutIncludesPartialOutput pins the shared-lane contract: a
// non-empty result returned alongside the deadline is the partial output, and
// it must reach the orchestrator in the structured result instead of being
// replaced by the bare error.
func TestExecute_TimeoutIncludesPartialOutput(t *testing.T) {
	tt := &DelegateTool{
		RoleNames: []string{"analyst"},
		Runner: func(context.Context, string, SubAgentParams) (string, error) {
			return "almost done", context.DeadlineExceeded
		},
	}

	out, err := tt.Execute(context.Background(), `{"description":"d","prompt":"p","role":"analyst","timeout_seconds":1}`)
	if err != nil {
		t.Fatalf("a timeout is a structured result, not a tool error: %v", err)
	}
	var got struct {
		Error   string `json:"error"`
		Role    string `json:"role"`
		Partial string `json:"partial"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got.Error != "timed out" {
		t.Errorf("error = %q, want timed out", got.Error)
	}
	if got.Role != "analyst" {
		t.Errorf("role = %q, want analyst (the orchestrator needs the budget that expired)", got.Role)
	}
	if got.Partial != "almost done" {
		t.Errorf("partial = %q, want the sub-agent's output", got.Partial)
	}
}

// TestExecute_LaneForRoleRouting pins yaah-7yf: LaneForRole decouples the
// output contract from the substrate. A report role keeps the isolated
// worktree but gets its report back with nothing to settle; a shared role
// runs the raw runner directly on the workspace even when the session is
// retained; a nil LaneForRole keeps every dispatch on the proposal lane.
func TestExecute_LaneForRoleRouting(t *testing.T) {
	newTool := func(runner TaskRunner, rt *RetainedRuntime, lane func(role string) DelegateLane) *DelegateTool {
		return &DelegateTool{
			RoleNames:   []string{"developer", "reviewer", "counter"},
			Runner:      runner,
			Retained:    rt,
			LaneForRole: lane,
		}
	}
	args := func(role string) string {
		return `{"description":"d","prompt":"p","role":"` + role + `"}`
	}

	t.Run("report lane returns the report, no proposal", func(t *testing.T) {
		resetRetainedProposals()
		runner := writingRunner(t, "r.txt")
		rt := newRetainedTestEnv(t, runner)
		tt := newTool(runner.run(), &rt, func(string) DelegateLane { return LaneReport })

		res, err := tt.Execute(context.Background(), args("reviewer"))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res != "default" {
			t.Errorf("report lane must return the runner text, got %q", res)
		}
		if got := listRetainedProposalsSummaries(); got != "No open proposals." {
			t.Errorf("report lane must not register a proposal, got %q", got)
		}
		wd := runner.workdirFor(t, 0)
		if wd == "" || wd == rt.RepoPath {
			t.Fatalf("report lane must still isolate, got workdir %q", wd)
		}
		if _, err := os.Stat(wd); !os.IsNotExist(err) {
			t.Errorf("report lane worktree must be torn down (stat err=%v)", err)
		}
	})

	t.Run("shared lane bypasses retention", func(t *testing.T) {
		resetRetainedProposals()
		runner := &sessionRunner{responses: []runnerResponse{{result: "shared report"}}}
		rt := newRetainedTestEnv(t, runner)
		tt := newTool(runner.run(), &rt, func(string) DelegateLane { return LaneShared })

		res, err := tt.Execute(context.Background(), args("counter"))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res != "shared report" {
			t.Errorf("shared lane must return the runner text, got %q", res)
		}
		if wd := runner.workdirFor(t, 0); wd != "" {
			t.Errorf("shared lane must not provision a worktree, got %q", wd)
		}
		if got := listRetainedProposalsSummaries(); got != "No open proposals." {
			t.Errorf("shared lane must not register a proposal, got %q", got)
		}
	})

	t.Run("nil LaneForRole keeps the proposal lane", func(t *testing.T) {
		resetRetainedProposals()
		runner := writingRunner(t, "p.txt")
		rt := newRetainedTestEnv(t, runner)
		tt := &DelegateTool{RoleNames: []string{"developer"}, Runner: runner.run(), Retained: &rt}

		res, err := tt.Execute(context.Background(), args("developer"))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		env := decodeProposalEnvelope(t, res)
		if env.Status != "proposal" || env.ProposalID == "" {
			t.Fatalf("nil LaneForRole must keep the proposal lane, got %+v", env)
		}
	})
}

// capturingRunner records the params Execute forwarded so a test can
// assert the clamped values that actually reached the runner.
func capturingRunner(rec *SubAgentParams) TaskRunner {
	return func(ctx context.Context, prompt string, params SubAgentParams) (string, error) {
		*rec = params
		return "ok", nil
	}
}

// TestExecute_ClampsNumericParams pins yaah-pgk: JSON Schema bounds are
// advisory — nothing in the transport validates them — so every numeric
// delegate param is clamped to the advertised bound before it reaches the
// runner. An injected payload can otherwise send a negative value or 10^6
// and neutralize the deadline or iterate forever.
//
// 0 is the "unset, use the role's own budget" sentinel for every one of
// these params, so a clamped-away value must land on 0 rather than being
// turned into a default: only values actually provided are clamped.
func TestExecute_ClampsNumericParams(t *testing.T) {
	tests := []struct {
		name        string
		params      string
		wantIter    int
		wantTurns   int
		wantOutput  int
		wantTimeout int
	}{
		// max_turns: schema "minimum": 1, "maximum": 50.
		{"max_turns over the maximum is capped", `"max_turns":1000000`, 0, 50, 0, 0},
		{"max_turns at the maximum is honoured", `"max_turns":50`, 0, 50, 0, 0},
		{"max_turns inside the window is honoured", `"max_turns":7`, 0, 7, 0, 0},
		{"max_turns negative falls back to the role default", `"max_turns":-3`, 0, 0, 0, 0},
		{"max_turns absent stays unset", ``, 0, 0, 0, 0},

		// max_iterations: schema "minimum": 1, "maximum": 50.
		{"max_iterations over the maximum is capped", `"max_iterations":1000000`, 50, 0, 0, 0},
		{"max_iterations at the maximum is honoured", `"max_iterations":50`, 50, 0, 0, 0},
		{"max_iterations negative falls back to the role default", `"max_iterations":-2`, 0, 0, 0, 0},
		{"max_iterations absent stays unset", ``, 0, 0, 0, 0},

		// timeout_seconds: not in the schema (roles own timeouts); the
		// 600s cap is the documented ceiling in clampTimeoutSeconds.
		{"timeout_seconds over the maximum is capped", `"timeout_seconds":1000000`, 0, 0, 0, 600},
		{"timeout_seconds negative falls back to the role default", `"timeout_seconds":-30`, 0, 0, 0, 0},

		// output_limit: schema "minimum": 1024, no maximum.
		{"output_limit below the minimum is raised to it", `"output_limit":512`, 0, 0, 1024, 0},
		{"output_limit at the minimum is honoured", `"output_limit":1024`, 0, 0, 1024, 0},
		{"output_limit above the minimum is honoured", `"output_limit":8192`, 0, 0, 8192, 0},
		{"output_limit has no advertised maximum so a large value stands", `"output_limit":1048576`, 0, 0, 1048576, 0},
		{"output_limit negative falls back to the role default", `"output_limit":-1`, 0, 0, 0, 0},
		{"output_limit absent stays unset", ``, 0, 0, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got SubAgentParams
			tt := &DelegateTool{
				RoleNames: []string{"analyst"},
				Runner:    capturingRunner(&got),
			}
			args := `{"description":"d","prompt":"p","role":"analyst"`
			if tc.params != "" {
				args += "," + tc.params
			}
			args += "}"

			if _, err := tt.Execute(context.Background(), args); err != nil {
				t.Fatalf("Execute(%s): %v", args, err)
			}
			if got.MaxToolTurns != tc.wantTurns {
				t.Errorf("runner got max_turns=%d, want %d", got.MaxToolTurns, tc.wantTurns)
			}
			if got.MaxLoopCycles != tc.wantIter {
				t.Errorf("runner got max_iterations=%d, want %d", got.MaxLoopCycles, tc.wantIter)
			}
			if got.OutputLimit != tc.wantOutput {
				t.Errorf("runner got output_limit=%d, want %d", got.OutputLimit, tc.wantOutput)
			}
			if got.TimeoutSeconds != tc.wantTimeout {
				t.Errorf("runner got timeout_seconds=%d, want %d", got.TimeoutSeconds, tc.wantTimeout)
			}
		})
	}
}

// TestClampMaxToolTurns covers the max_turns window in isolation, including
// the 0/absent sentinel that must survive unchanged.
func TestClampMaxToolTurns(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"over the maximum is capped", 1000000, 50},
		{"one over the maximum is capped", 51, 50},
		{"at the maximum is honoured", 50, 50},
		{"inside the window is honoured", 12, 12},
		{"at the minimum is honoured", 1, 1},
		{"zero means unset and is preserved", 0, 0},
		{"negative means unset", -3, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampMaxToolTurns(tc.in); got != tc.want {
				t.Errorf("clampMaxToolTurns(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestClampOutputLimit covers the 1024-byte floor. The schema advertises no
// maximum, so none is enforced here — a bigger cap is the orchestrator's
// call, not a containment property.
func TestClampOutputLimit(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"below the minimum is raised to it", 512, 1024},
		{"one below the minimum is raised to it", 1023, 1024},
		{"at the minimum is honoured", 1024, 1024},
		{"above the minimum is honoured", 51200, 51200},
		{"a very large value stands (no advertised maximum)", 1048576, 1048576},
		{"zero means unset and is preserved", 0, 0},
		{"negative means unset", -1024, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampOutputLimit(tc.in); got != tc.want {
				t.Errorf("clampOutputLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestSchema_BoundsMatchClamp keeps the runtime clamp and the advertised
// bounds from drifting apart: the clamp is only as honest as the numbers the
// model was told, and the schema is only as honest as what the runtime does
// with a value that honours it. Every schema variant is checked, because the
// fallback literal schema and the generated enum schema are separate
// declarations.
func TestSchema_BoundsMatchClamp(t *testing.T) {
	type bounds struct {
		min    float64
		max    float64
		hasMax bool
	}
	want := map[string]bounds{
		"max_iterations": {min: 1, max: 50, hasMax: true},
		"max_turns":      {min: 1, max: 50, hasMax: true},
		"output_limit":   {min: 1024, hasMax: false},
	}

	schemas := map[string]*DelegateTool{
		"enum schema":     {RoleNames: []string{"analyst"}},
		"fallback schema": {},
	}
	for name, tool := range schemas {
		t.Run(name, func(t *testing.T) {
			var parsed struct {
				Properties map[string]struct {
					Minimum *float64 `json:"minimum"`
					Maximum *float64 `json:"maximum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tool.Schema(), &parsed); err != nil {
				t.Fatalf("invalid schema JSON: %v", err)
			}
			for prop, w := range want {
				p, ok := parsed.Properties[prop]
				if !ok {
					t.Fatalf("schema is missing %q", prop)
				}
				if p.Minimum == nil || *p.Minimum != w.min {
					t.Errorf("%s minimum = %v, want %v", prop, p.Minimum, w.min)
				}
				if w.hasMax {
					if p.Maximum == nil || *p.Maximum != w.max {
						t.Errorf("%s maximum = %v, want %v", prop, p.Maximum, w.max)
					}
					continue
				}
				if p.Maximum != nil {
					t.Errorf("%s advertises maximum %v but no clamp enforces it", prop, *p.Maximum)
				}
			}

			// The clamp must agree with what the schema promised: over-max is
			// capped to the maximum, below-min is answered per bound style.
			if got := clampMaxToolTurns(int(want["max_turns"].max) + 1); float64(got) != want["max_turns"].max {
				t.Errorf("clampMaxToolTurns above the schema maximum = %d, want %v", got, want["max_turns"].max)
			}
			if got := clampMaxLoopCycles(int(want["max_iterations"].max) + 1); float64(got) != want["max_iterations"].max {
				t.Errorf("clampMaxLoopCycles above the schema maximum = %d, want %v", got, want["max_iterations"].max)
			}
			if got := clampOutputLimit(int(want["output_limit"].min) - 1); float64(got) != want["output_limit"].min {
				t.Errorf("clampOutputLimit below the schema minimum = %d, want %v", got, want["output_limit"].min)
			}
		})
	}
}
