package yaah

// workflow_bench_run_test.go is the CI gate for the five orch-* bench tasks
// the spec-driven bench gained in yaah-0u0v.27: orch-settle-discard,
// orch-guard, orch-quality-gate, orch-escalation, orch-recovery.
//
// Same discipline as the orch-fanout gate in workflow_bench_spec_test.go: no
// model, no network, no sleep. The runner seam carries a deterministic
// closure and every asserted orchestration fact comes from the document —
// which nodes dispatch, what the loop replays, what the settle disposes,
// what each emit writes. The retained-lane tasks run against the real
// retained machinery (a temp git repo, a real worktree), the same
// composition a composed host injects; the report-lane tasks need only the
// runner.
//
// The recovery drill is two halves, matching the document's header comment:
// the report-the-adoption shape end to end, and a crashed run resumed to
// completion with exactly the un-done node re-dispatched.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	goatherdgit "github.com/buchenberg/goatherd-runtime/sandbox/git"

	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/jobs"
)

// mustBenchGit runs git in dir, failing the test on error — the same helper
// the host package's retained tests use, local to keep packages honest.
func mustBenchGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// wfBenchRetainedEnv builds the retained runtime a composed host would
// inject: a temp git repository with one commit, a root scope over it, and
// a fresh proposal registry. The runner is the model's stand-in; everything
// else is the real machinery — a proposal seals a real worktree and a
// settlement applies it to the host repository.
func wfBenchRetainedEnv(t *testing.T, runner jobs.TaskRunner) *dispatch.RetainedRuntime {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(":memory:")
	if err != nil {
		t.Fatalf("open test trace store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr := goatherd.NewScopeManager(store)

	repo := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "gitconfig-system"))
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		mustBenchGit(t, repo, args...)
	}
	if err := os.WriteFile(filepath.Join(repo, "mathx.py"), []byte("def mean(values):\n    return sum(values) / (len(values) - 1)\n"), 0o644); err != nil {
		t.Fatalf("seed mathx.py: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "trap.txt"), []byte("do not touch\n"), 0o644); err != nil {
		t.Fatalf("seed trap.txt: %v", err)
	}
	mustBenchGit(t, repo, "add", "-A")
	mustBenchGit(t, repo, "commit", "-m", "init")

	root, err := mgr.Create("wf-bench-root", goatherdgit.NewLocalGitSandbox(repo,
		goatherdgit.WithCleanPolicy(goatherdgit.CleanPreserveUntracked)))
	if err != nil {
		t.Fatalf("create root scope: %v", err)
	}
	return &dispatch.RetainedRuntime{
		Runner:       runner,
		RepoPath:     repo,
		ScopeManager: mgr,
		RootScopeID:  root.ID(),
		WorktreeRoot: t.TempDir(),
		Proposals:    dispatch.NewProposalRegistry(),
	}
}

// stashBenchRetained hands a composed retained runtime to the run and resume
// commands through the same process-scoped seam the composition root uses,
// and clears it when the test ends.
func stashBenchRetained(t *testing.T, env *dispatch.RetainedRuntime) {
	t.Helper()
	prev := workflowRetainedRuntime.Load()
	workflowRetainedRuntime.Store(env)
	t.Cleanup(func() { workflowRetainedRuntime.Store(prev) })
}

// benchWriteRunner is a sub-agent double that writes one file into the
// worktree it is handed and returns a JSON body — the shape a proposal-lane
// dispatch reports.
func benchWriteRunner(name, content string) jobs.TaskRunner {
	return func(_ context.Context, _ string, params jobs.SubAgentParams) (string, error) {
		if err := os.WriteFile(filepath.Join(params.Workdir, name), []byte(content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf(`{"file": %q}`, name), nil
	}
}

// runWfBenchSpec runs a checked-in bench spec through `workflow run` with
// the report-lane flags every bench driver uses, and returns the parsed
// report plus the raw stdout.
func runWfBenchSpec(t *testing.T, task, runID, statePath, artifactRoot string) (workflowRunReport, string, error) {
	t.Helper()
	spec := filepath.Join(wfBenchDir, task, "spec", "workflow.yaml")
	out, err := runWfRun(t, "--json", "--run-id", runID,
		"--state", statePath, "--artifact-root", artifactRoot, spec)
	if err != nil && !strings.Contains(out, "run_id") {
		return workflowRunReport{}, out, err
	}
	return wfJSONReport[workflowRunReport](t, out), out, err
}

// benchTraceOf exports a run's trace exactly the way the driver does for
// orch_assert.py.
func benchTraceOf(t *testing.T, task, runID, statePath string) workflowTraceReport {
	t.Helper()
	spec := filepath.Join(wfBenchDir, task, "spec", "workflow.yaml")
	out, err := runWfExplainFromTrace(t, runID, statePath, spec)
	if err != nil {
		t.Fatalf("explain --from-trace: %v\noutput:\n%s", err, out)
	}
	return wfJSONReport[workflowTraceReport](t, out)
}

// ------------------------------------------------- orch-settle-discard

func TestBenchSpecOrchSettleDiscardRunsFromTheDocument(t *testing.T) {
	dir := t.TempDir()
	artifactRoot := filepath.Join(dir, "app")

	calls := 0
	runner := func(ctx context.Context, prompt string, params jobs.SubAgentParams) (string, error) {
		calls++
		if !strings.Contains(prompt, "fizzbuzz") {
			t.Errorf("delegate prompt does not carry the bench task: %q", prompt)
		}
		return benchWriteRunner("fizzbuzz.py", "def fizzbuzz(n):\n    pass\n")(ctx, prompt, params)
	}
	env := wfBenchRetainedEnv(t, runner)
	stashBenchRetained(t, env)
	swapRunRunner(t, runner, &calls)

	rep, out, err := runWfBenchSpec(t, "orch-settle-discard", "bench-orch-settle-discard",
		filepath.Join(dir, "state.db"), artifactRoot)
	if err != nil {
		t.Fatalf("bench run: %v\noutput:\n%s", err, out)
	}
	if !rep.OK || rep.Status != "succeeded" {
		t.Fatalf("run status = %q ok=%v reason=%q\n%s", rep.Status, rep.OK, rep.Reason, out)
	}
	if calls != 1 || rep.Dispatches != 1 {
		t.Errorf("calls/dispatches = %d/%d, want one sealed delegate", calls, rep.Dispatches)
	}

	// The task's end state: answer.txt names the discarded proposal, and the
	// sealed work is nowhere — /app (the artifact root) never saw
	// fizzbuzz.py, and the host repo never received it either.
	answer := filepath.Join(artifactRoot, "answer.txt")
	data, rerr := os.ReadFile(answer)
	if rerr != nil {
		t.Fatalf("reading %s: %v", answer, rerr)
	}
	if line := strings.TrimSpace(string(data)); !strings.HasPrefix(line, "discarded: proposal:") {
		t.Errorf("answer.txt = %q, want it to name the discarded proposal", line)
	}
	if _, serr := os.Stat(filepath.Join(artifactRoot, "fizzbuzz.py")); serr == nil {
		t.Error("fizzbuzz.py landed in the artifact root: the discard verb must sink the sealed work")
	}
	if _, serr := os.Stat(filepath.Join(env.RepoPath, "fizzbuzz.py")); serr == nil {
		t.Error("fizzbuzz.py landed on the host repo: the discard verb must sink the sealed work")
	}

	tr := benchTraceOf(t, "orch-settle-discard", "bench-orch-settle-discard", filepath.Join(dir, "state.db"))
	if len(tr.Proposals) != 1 {
		t.Errorf("trace reports %d proposal(s), want the one the delegate sealed", len(tr.Proposals))
	}
	if len(tr.Settlements) != 1 {
		t.Fatalf("trace reports %d settlement(s), want the discard", len(tr.Settlements))
	}
	s := tr.Settlements[0]
	if s.Action != "discard" || s.Status != "settled" {
		t.Errorf("settlement action/status = %q/%q, want discard/settled", s.Action, s.Status)
	}
	if tr.Dispatches != 1 || len(tr.Divergences) != 0 {
		t.Errorf("trace dispatches/divergences = %d/%v", tr.Dispatches, tr.Divergences)
	}
}

// ------------------------------------------------------ orch-guard

func TestBenchSpecOrchGuardRunsFromTheDocument(t *testing.T) {
	dir := t.TempDir()
	artifactRoot := filepath.Join(dir, "app")

	// The flaky script the bench pins fails four times, succeeds on the
	// fifth, and host supervision injects exactly the warning the task says
	// a run without is a failure. The bogus counter is per-prompt state
	// inside the double, mirroring flaky.sh's /tmp/flaky.count.
	const warning = "WARNING: error rate 100% (4/4 recent calls). Consider a different approach."
	calls := 0
	probes := 0
	swapRunRunner(t, func(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
		switch {
		case strings.Contains(prompt, "Run /app/flaky.sh exactly once"):
			probes++
			if probes < 5 {
				return fmt.Sprintf(`{"outcome": "fail", "attempt": %d, "warning": ""}`, probes), nil
			}
			return fmt.Sprintf(`{"outcome": "ok", "attempt": 5, "warning": %q}`, warning), nil
		case strings.Contains(prompt, "supervised retry loop recorded these attempts"):
			// The summariser reads the collect the document rendered into
			// its prompt — a JSON-encoded list of the attempt reports, with
			// each report's own quotes backslash-escaped inside the strings.
			// Unescape first, then count failures: attempts = failures +
			// the success, both read out of the record the loop produced.
			plain := strings.ReplaceAll(prompt, `\`, "")
			fails := strings.Count(plain, `"outcome":"fail"`) + strings.Count(plain, `"outcome": "fail"`)
			return fmt.Sprintf(`{"attempts": %d, "warning": %q}`, fails+1, warning), nil
		default:
			return "", fmt.Errorf("bench guard runner got an unknown prompt: %q", prompt)
		}
	}, &calls)

	rep, out, err := runWfBenchSpec(t, "orch-guard", "bench-orch-guard",
		pathJoin(dir, "state.db"), artifactRoot)
	if err != nil {
		t.Fatalf("bench run: %v\noutput:\n%s", err, out)
	}
	if !rep.OK || rep.Status != "succeeded" {
		t.Fatalf("run status = %q ok=%v reason=%q\n%s", rep.Status, rep.OK, rep.Reason, out)
	}

	for want, path := range map[string]string{
		"attempts: 5\n" + warning: "answer.txt",
		"attempts=5":              "ok.txt",
	} {
		data, rerr := os.ReadFile(filepath.Join(artifactRoot, path))
		if rerr != nil {
			t.Fatalf("reading %s: %v", path, rerr)
		}
		if got := string(data); !strings.HasPrefix(got, want) {
			t.Errorf("%s = %q, want it to start with %q", path, got, want)
		}
	}

	tr := benchTraceOf(t, "orch-guard", "bench-orch-guard", pathJoin(dir, "state.db"))
	// Five probe attempts plus the summariser: the loop is the record that
	// each attempt was its own dispatch.
	if tr.Dispatches != 6 {
		t.Errorf("dispatches = %d, want 5 supervised attempts plus one summary", tr.Dispatches)
	}
	var loop *workflowTraceNodeView
	for i := range tr.Nodes {
		if tr.Nodes[i].Kind == "loop" {
			loop = &tr.Nodes[i]
		}
	}
	if loop == nil || loop.State != "succeeded" {
		t.Fatalf("no succeeded loop node in the trace: %+v", loop)
	}
	if len(tr.Divergences) != 0 {
		t.Errorf("the run diverged from its document: %+v", tr.Divergences)
	}
}

// -------------------------------------------------- orch-quality-gate

func TestBenchSpecOrchQualityGateRunsFromTheDocument(t *testing.T) {
	dir := t.TempDir()
	artifactRoot := filepath.Join(dir, "app")

	const bugfree = "\"\"\"Math helpers.\"\"\"\n\ndef mean(values):\n    return sum(values) / len(values)\n"
	const buggy = "\"\"\"Math helpers.\"\"\"\n\ndef mean(values):\n    return sum(values) / (len(values) - 1)\n"
	checks := 0
	propRe := regexp.MustCompile(`proposal:[0-9a-z:_-]+`)

	// implement writes the docstring onto the broken file (the bench's
	// first move); fix repairs the average; check drives the gate — fail
	// on the first verification, pass on the second.
	runner := func(ctx context.Context, prompt string, params jobs.SubAgentParams) (string, error) {
		switch {
		case strings.Contains(prompt, "Add a module docstring"):
			return benchWriteRunner("mathx.py", buggy)(ctx, prompt, params)
		case strings.Contains(prompt, "quality gate reported a failure"):
			return benchWriteRunner("mathx.py", bugfree)(ctx, prompt, params)
		case strings.Contains(prompt, "proposal under verification"):
			checks++
			id := "unset"
			if m := propRe.FindStringSubmatch(prompt); m != nil {
				id = m[0]
			}
			if checks == 1 {
				return fmt.Sprintf(`{"verdict": "fail", "proposal_id": %q, "reasons": ["verify: /app/verify.py failed"]}`, id), nil
			}
			return fmt.Sprintf(`{"verdict": "pass", "proposal_id": %q, "reasons": []}`, id), nil
		default:
			return "", fmt.Errorf("bench quality-gate runner got an unknown prompt: %q", prompt)
		}
	}
	env := wfBenchRetainedEnv(t, runner)
	stashBenchRetained(t, env)
	swapRunRunner(t, runner, new(int))

	rep, out, err := runWfBenchSpec(t, "orch-quality-gate", "bench-orch-quality-gate",
		pathJoin(dir, "state.db"), artifactRoot)
	if err != nil {
		t.Fatalf("bench run: %v\noutput:\n%s", err, out)
	}
	if !rep.OK || rep.Status != "succeeded" {
		t.Fatalf("run status = %q ok=%v reason=%q\n%s", rep.Status, rep.OK, rep.Reason, out)
	}

	// The quality-gate end state: the gate's comment says only what passed
	// lands, and what landed on the host repo is docstring plus the fixed
	// average — the bench's success condition with no trap.txt in sight.
	landed, rerr := os.ReadFile(filepath.Join(env.RepoPath, "mathx.py"))
	if rerr != nil {
		t.Fatalf("the settled work never landed on the host repo: %v", rerr)
	}
	if string(landed) != bugfree {
		t.Errorf("landed mathx.py = %q, want the repaired %q", string(landed), bugfree)
	}
	if _, terr := os.Stat(filepath.Join(env.RepoPath, "trap.txt")); terr != nil {
		t.Fatalf("trap.txt vanished: %v", terr)
	}
	report := filepath.Join(artifactRoot, "artifacts", "quality-gate.md")
	if _, rerr := os.Stat(report); rerr != nil {
		t.Errorf("the gate report artifact was not written: %v", rerr)
	}

	tr := benchTraceOf(t, "orch-quality-gate", "bench-orch-quality-gate", pathJoin(dir, "state.db"))
	// Every sealed proposal left the run with exactly one disposition: the
	// passing fix applies, the failed fix is discarded, the superseded
	// implementation is discarded.
	if len(tr.Settlements) != 3 {
		t.Fatalf("settlements = %+v, want three (apply + two dispositions)", tr.Settlements)
	}
	for _, s := range tr.Settlements {
		if s.Status != "settled" || s.LeftOpen {
			t.Errorf("settlement %+v is not settled clean", s)
		}
		switch s.NodePath {
		case "steps.land":
			if s.Action != "apply" {
				t.Errorf("steps.land action = %q, want apply", s.Action)
			}
		case "steps.reject":
			if s.Action != "discard" {
				t.Errorf("steps.reject action = %q, want discard", s.Action)
			}
		case "steps.supersede":
			if s.Action != "discard" {
				t.Errorf("steps.supersede action = %q, want discard", s.Action)
			}
		default:
			t.Errorf("settlement from unexpected node %s", s.NodePath)
		}
	}
	// The loop ran two gate cycles: the failing one and the passing one.
	if tr.Dispatches != 5 {
		t.Errorf("dispatches = %d, want implement + fix + check + fix + check", tr.Dispatches)
	}
}

// --------------------------------------------------- orch-escalation

func TestBenchSpecOrchEscalationRunsFromTheDocument(t *testing.T) {
	dir := t.TempDir()
	artifactRoot := filepath.Join(dir, "app")

	const block = "```json\n{\n  \"severity\": \"blocker\",\n  \"kind\": \"missing_input\",\n  \"summary\": \"data/measurements.csv does not exist; the summary cannot be computed\",\n  \"evidence\": \"ls /app/data returned no such directory\"\n}\n```"
	var gotPrompt string
	calls := 0
	swapRunRunner(t, func(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
		gotPrompt = prompt
		return block, nil
	}, &calls)

	rep, out, err := runWfBenchSpec(t, "orch-escalation", "bench-orch-escalation",
		pathJoin(dir, "state.db"), artifactRoot)
	if err != nil {
		t.Fatalf("bench run: %v\noutput:\n%s", err, out)
	}
	if !rep.OK || rep.Status != "succeeded" {
		t.Fatalf("refusing to fabricate must be a successful run, got %q (%q)\n%s", rep.Status, rep.Reason, out)
	}
	answer := filepath.Join(artifactRoot, "answer.txt")
	data, rerr := os.ReadFile(answer)
	if rerr != nil {
		t.Fatalf("reading %s: %v", answer, rerr)
	}
	if got := string(data); got != block {
		t.Errorf("answer.txt = %q, want the escalation block verbatim %q", got, block)
	}
	if calls != 1 || rep.Dispatches != 1 {
		t.Errorf("calls/dispatches = %d/%d, want the one honest delegate", calls, rep.Dispatches)
	}
	if !strings.Contains(gotPrompt, "severity") || !strings.Contains(gotPrompt, "TASK.md") {
		t.Errorf("delegate prompt does not carry the escalation contract: %q", gotPrompt)
	}
}

// ---------------------------------------------------- orch-recovery

func TestBenchSpecOrchRecoveryRunsFromTheDocument(t *testing.T) {
	dir := t.TempDir()
	artifactRoot := filepath.Join(dir, "app")

	const adopted = "adopted: /app/.orch-worktree/wt"
	var gotPrompt string
	calls := 0
	swapRunRunner(t, func(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
		gotPrompt = prompt
		return adopted, nil
	}, &calls)

	rep, out, err := runWfBenchSpec(t, "orch-recovery", "bench-orch-recovery",
		pathJoin(dir, "state.db"), artifactRoot)
	if err != nil {
		t.Fatalf("bench run: %v\noutput:\n%s", err, out)
	}
	if !rep.OK || rep.Status != "succeeded" {
		t.Fatalf("run status = %q ok=%v reason=%q\n%s", rep.Status, rep.OK, rep.Reason, out)
	}
	recovery := filepath.Join(artifactRoot, "recovery.txt")
	data, rerr := os.ReadFile(recovery)
	if rerr != nil {
		t.Fatalf("reading %s: %v", recovery, rerr)
	}
	if got := strings.TrimSpace(string(data)); got != adopted {
		t.Errorf("recovery.txt = %q, want %q", got, adopted)
	}
	if !strings.Contains(gotPrompt, "shepherd") {
		t.Errorf("delegate prompt does not carry the recovery scan: %q", gotPrompt)
	}
}

// TestBenchSpecResumeRefusesFinishedRun pins the resume contract from the
// command seam: the durable record's disposition is final — a run that ended
// aborted stays aborted, and resume refuses it by name rather than
// re-dispatching work whose failure the user already saw. The other half of
// the recovery drill — a process that dies mid-run and IS resumable — is
// proven at the scheduler core, where kills can be seeded deterministically:
// internal/workflow/exec/resume_property_test.go (P1-P4: convergence with the
// baseline, at most one settle per proposal, unique ordinals, honest
// write-ahead failures).
func TestBenchSpecResumeRefusesFinishedRun(t *testing.T) {
	dir := t.TempDir()
	artifactRoot := t.TempDir()
	statePath := pathJoin(dir, "state.db")

	calls := 0
	swapRunRunner(t, func(context.Context, string, jobs.SubAgentParams) (string, error) {
		return "", fmt.Errorf("process killed mid-dispatch (bench crash drill)")
	}, &calls)

	_, out, _ := runWfBenchSpec(t, "orch-recovery", "bench-orch-recovery", statePath, artifactRoot)
	if strings.Contains(out, `"status":"succeeded"`) {
		t.Fatalf("the crash drill's first run succeeded; the crash was not staged:\n%s", out)
	}
	if calls != 1 {
		t.Fatalf("staged crash dispatched %d times, want the one failed attempt", calls)
	}

	// The operator comes back and tries to resume a run whose disposition is
	// already final: the command refuses it, and no dispatch is re-issued.
	swapRunRunner(t, func(context.Context, string, jobs.SubAgentParams) (string, error) {
		return "adopted: /app/.orch-worktree/wt", nil
	}, &calls)
	resume := newWorkflowResumeCmd()
	var buf bytes.Buffer
	resume.SetOut(&buf)
	resume.SetErr(&buf)
	resume.SetContext(context.Background())
	if err := resume.ParseFlags([]string{
		"--state", statePath, "--artifact-root", artifactRoot, "bench-orch-recovery",
	}); err != nil {
		t.Fatalf("parse resume flags: %v", err)
	}
	if err := resume.ValidateArgs(resume.Flags().Args()); err != nil {
		t.Fatalf("validate resume args: %v", err)
	}
	rerr := resume.RunE(resume, resume.Flags().Args())
	if rerr == nil {
		t.Fatalf("resume accepted a run whose disposition is final:\n%s", buf.String())
	}
	if !strings.Contains(rerr.Error(), "already finished") {
		t.Errorf("resume refusal = %v, want the finished-disposition refusal", rerr)
	}
	if calls != 1 {
		t.Errorf("dispatches grew to %d refusing a finished run", calls)
	}
}

// path_join helper local to this file keeps the call sites short.
func pathJoin(parts ...string) string {
	return filepath.Join(parts...)
}
