package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeExecWorkspace answers Exec with a canned response. The tools under test
// only reach the workspace through Exec, so an embedded (nil) local workspace
// satisfies the rest of the interface.
type fakeExecWorkspace struct {
	*localWorkspace
	respond func(req ExecRequest) (ExecResult, error)
}

func (f *fakeExecWorkspace) Exec(_ context.Context, req ExecRequest) (ExecResult, error) {
	return f.respond(req)
}

func parseGoTestResult(t *testing.T, stdout string, exitCode int) *goTestResult {
	t.Helper()
	r := &goTestResult{ExitCode: exitCode}
	r.parse(stdout)
	if r.Status == "" {
		t.Fatal("parse left status empty")
	}
	return r
}

func TestGoTestParse_PassingRun(t *testing.T) {
	stdout := strings.Join([]string{
		`{"Time":"2026-10-17T00:00:00Z","Action":"start","Package":"example.com/x"}`,
		`{"Time":"2026-10-17T00:00:00Z","Action":"run","Package":"example.com/x","Test":"TestOK"}`,
		`{"Time":"2026-10-17T00:00:00Z","Action":"output","Package":"example.com/x","Test":"TestOK","Output":"=== RUN   TestOK\n"}`,
		`{"Time":"2026-10-17T00:00:00Z","Action":"pass","Package":"example.com/x","Test":"TestOK","Elapsed":0.01}`,
		`{"Time":"2026-10-17T00:00:00Z","Action":"pass","Package":"example.com/x","Elapsed":0.01}`,
	}, "\n")

	r := parseGoTestResult(t, stdout, 0)
	if r.Status != goTestOK {
		t.Errorf("status = %q, want %q", r.Status, goTestOK)
	}
	if r.Passed != 1 || r.Failed != 0 || r.Total != 1 {
		t.Errorf("counts = %d/%d/%d, want 1/0/1", r.Passed, r.Failed, r.Total)
	}
	if len(r.Packages) != 1 || r.Packages[0].Package != "example.com/x" {
		t.Errorf("packages = %+v", r.Packages)
	}
	if r.RawOutput != "" {
		t.Errorf("raw_output should be empty on a successful run, got %q", r.RawOutput)
	}
}

func TestGoTestParse_FailingTest(t *testing.T) {
	stdout := strings.Join([]string{
		`{"Action":"start","Package":"example.com/x"}`,
		`{"Action":"fail","Package":"example.com/x","Test":"TestBad","Elapsed":0.01}`,
		`{"Action":"fail","Package":"example.com/x","Elapsed":0.01}`,
	}, "\n")

	r := parseGoTestResult(t, stdout, 1)
	if r.Status != goTestFailed {
		t.Errorf("status = %q, want %q", r.Status, goTestFailed)
	}
	if r.Failed != 1 || r.Total != 1 {
		t.Errorf("counts = failed %d total %d, want 1/1", r.Failed, r.Total)
	}
	if len(r.FailedTests) != 1 || r.FailedTests[0] != "example.com/x/TestBad" {
		t.Errorf("failed_tests = %v", r.FailedTests)
	}
}

// TestGoTestParse_BuildFailure reproduces the stream from a cross-module
// absolute pattern: go exits 1 with no test events, and every cause is in
// build-output. Before this was parsed the result was a green-looking
// {"passed":0,"failed":0,"exit_code":1}.
func TestGoTestParse_BuildFailure(t *testing.T) {
	stdout := strings.Join([]string{
		`{"ImportPath":"/tmp/other/...","Action":"build-output","Output":"# /tmp/other/...\n"}`,
		`{"ImportPath":"/tmp/other/...","Action":"build-output","Output":"pattern /tmp/other/...: directory prefix ../other does not contain main module or its selected dependencies\n"}`,
		`{"ImportPath":"/tmp/other/...","Action":"build-fail"}`,
		`{"Time":"2026-10-17T00:00:00Z","Action":"start","Package":"/tmp/other/..."}`,
		`{"Time":"2026-10-17T00:00:00Z","Action":"output","Package":"/tmp/other/...","Output":"FAIL\t/tmp/other/... [setup failed]\n"}`,
		`{"Time":"2026-10-17T00:00:00Z","Action":"fail","Package":"/tmp/other/...","Elapsed":0,"FailedBuild":"/tmp/other/..."}`,
	}, "\n")

	r := parseGoTestResult(t, stdout, 1)
	if r.Status != goTestError {
		t.Errorf("status = %q, want %q (a build failure is not a green run)", r.Status, goTestError)
	}
	if r.Total != 0 {
		t.Errorf("total = %d, want 0", r.Total)
	}
	if !strings.Contains(r.BuildOutput, "does not contain main module") {
		t.Errorf("build_output must carry the compiler message verbatim, got %q", r.BuildOutput)
	}
	if len(r.FailedBuilds) != 1 || r.FailedBuilds[0] != "/tmp/other/..." {
		t.Errorf("failed_builds = %v", r.FailedBuilds)
	}
	if r.RawOutput == "" {
		t.Error("raw_output must preserve the stdout tail when nothing ran")
	}
}

// TestGoTestParse_BuildFailureWithPassingPackages: one package passed before
// another failed to build. The counts are real, so the status is "failed", but
// the build error must still be visible.
func TestGoTestParse_BuildFailureWithPassingPackages(t *testing.T) {
	stdout := strings.Join([]string{
		`{"Action":"pass","Package":"example.com/x","Test":"TestOK","Elapsed":0.01}`,
		`{"Action":"pass","Package":"example.com/x","Elapsed":0.01}`,
		`{"ImportPath":"example.com/y","Action":"build-output","Output":"# example.com/y\n./y.go:3:1: syntax error\n"}`,
		`{"ImportPath":"example.com/y","Action":"build-fail"}`,
	}, "\n")

	r := parseGoTestResult(t, stdout, 1)
	if r.Status != goTestFailed {
		t.Errorf("status = %q, want %q", r.Status, goTestFailed)
	}
	if r.Passed != 1 {
		t.Errorf("passed = %d, want 1", r.Passed)
	}
	if !strings.Contains(r.BuildOutput, "syntax error") {
		t.Errorf("build_output = %q, want the compile error", r.BuildOutput)
	}
	// Not an error status: the suite did run, so raw_output is unnecessary.
	if r.RawOutput != "" {
		t.Errorf("raw_output should be empty when tests ran, got %q", r.RawOutput)
	}
}

func TestGoTestStatus(t *testing.T) {
	cases := []struct {
		name        string
		exit        int
		total       int
		failed      int
		timedOut    bool
		buildFailed bool
		sawPackage  bool
		want        string
	}{
		{"clean run", 0, 5, 0, false, false, true, goTestOK},
		{"no test files", 0, 0, 0, false, false, true, goTestOK},
		{"compile-only (-c) runs nothing", 0, 0, 0, false, false, false, goTestError},
		{"text output (-json=false) parses nothing", 0, 0, 0, false, false, false, goTestError},
		{"failing tests", 1, 5, 1, false, false, true, goTestFailed},
		{"build failure with no tests", 1, 0, 0, false, true, true, goTestError},
		{"build failure alongside passing tests", 1, 3, 0, false, true, true, goTestFailed},
		{"package-level failure (TestMain exit)", 1, 0, 0, false, false, true, goTestFailed},
		{"nonzero exit with nothing parsed", 1, 0, 0, false, false, false, goTestError},
		{"go's own -timeout with partial counts", 1, 2, 0, true, false, true, goTestError},
		{"never started", -1, 0, 0, false, false, false, goTestError},
		{"killed by the caller's deadline", -1, 2, 0, true, false, true, goTestError},
	}
	for _, c := range cases {
		if got := goTestStatus(c.exit, c.total, c.failed, c.timedOut, c.buildFailed, c.sawPackage); got != c.want {
			t.Errorf("%s: goTestStatus(%d, %d, %d, %v, %v, %v) = %q, want %q",
				c.name, c.exit, c.total, c.failed, c.timedOut, c.buildFailed, c.sawPackage, got, c.want)
		}
	}
}

// TestGoTestParse_GoOwnTimeout: `go test -timeout` kills the binary and exits
// non-zero without a per-test fail event, so the partial counts used to look
// like an ordinary failed run with no cause. The panic line marks it.
func TestGoTestParse_GoOwnTimeout(t *testing.T) {
	stdout := strings.Join([]string{
		`{"Action":"start","Package":"example.com/x"}`,
		`{"Action":"run","Package":"example.com/x","Test":"TestFast"}`,
		`{"Action":"pass","Package":"example.com/x","Test":"TestFast","Elapsed":0.01}`,
		`{"Action":"run","Package":"example.com/x","Test":"TestSlow"}`,
		`{"Action":"output","Package":"example.com/x","Test":"TestSlow","Output":"panic: test timed out after 1s\n"}`,
		`{"Action":"fail","Package":"example.com/x","Elapsed":1.006}`,
	}, "\n")

	r := parseGoTestResult(t, stdout, 1)
	if !r.TimedOut {
		t.Error("timed_out = false, want true for Go's own -timeout kill")
	}
	if r.Status != goTestError {
		t.Errorf("status = %q, want %q (the run never completed)", r.Status, goTestError)
	}
	if r.Passed != 1 {
		t.Errorf("partial counts lost: passed = %d, want 1", r.Passed)
	}
	if r.RawOutput == "" {
		t.Error("raw_output must carry the timeout tail")
	}

	// A test that prints the phrase itself is not a Go timeout: only the
	// testing package's panic line counts.
	lookalike := strings.Join([]string{
		`{"Action":"start","Package":"example.com/x"}`,
		`{"Action":"pass","Package":"example.com/x","Test":"TestPhrase","Elapsed":0.01}`,
		`{"Action":"output","Package":"example.com/x","Test":"TestPhrase","Output":"the string test timed out after appears here\n"}`,
		`{"Action":"pass","Package":"example.com/x","Elapsed":0.01}`,
	}, "\n")
	if got := parseGoTestResult(t, lookalike, 0); got.TimedOut {
		t.Errorf("a test printing the phrase must not set timed_out: %+v", got)
	}
}

// TestGoTestParse_NoEventsIsNotGreen: exit 0 with nothing parsed means nothing
// ran (-c compiles only; -json=false emits text this parser cannot read).
func TestGoTestParse_NoEventsIsNotGreen(t *testing.T) {
	r := parseGoTestResult(t, "", 0)
	if r.Status != goTestError {
		t.Errorf("status = %q, want %q when no package event was parsed", r.Status, goTestError)
	}
}

func TestGoTestTool_ExecFailureIsAnError(t *testing.T) {
	tool := &GoTestTool{WS: &fakeExecWorkspace{respond: func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: -1, Stderr: `exec: "go": executable file not found in $PATH`},
			errors.New(`exec: "go": executable file not found in $PATH`)
	}}}

	_, err := tool.Execute(context.Background(), `{"packages":"./..."}`)
	if err == nil {
		t.Fatal("a toolchain that never started must be an error, not a zero-count result")
	}
	if !strings.Contains(err.Error(), "could not run go") || !strings.Contains(err.Error(), "executable file not found") {
		t.Errorf("error must name the cause, got %q", err)
	}
}

func TestGoTestTool_TimeoutKeepsPartialCounts(t *testing.T) {
	tool := &GoTestTool{WS: &fakeExecWorkspace{respond: func(ExecRequest) (ExecResult, error) {
		return ExecResult{
			ExitCode: -1,
			Stdout:   `{"Action":"pass","Package":"example.com/x","Test":"TestOK","Elapsed":0.01}` + "\n",
		}, errors.New("signal: killed")
	}}}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	out, err := tool.Execute(ctx, `{"packages":"./..."}`)
	if err != nil {
		t.Fatalf("a timed-out run should report a structured error result, got %v", err)
	}
	var r goTestResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if r.Status != goTestError || !r.TimedOut {
		t.Errorf("status = %q timed_out = %v, want error/true", r.Status, r.TimedOut)
	}
	if r.Passed != 1 {
		t.Errorf("partial counts lost: passed = %d, want 1", r.Passed)
	}
}

func TestGoTestParse_ScansLongLines(t *testing.T) {
	// A panic trace can exceed bufio's 64 KiB default token limit; the parser
	// must not stop at the first long line and drop the events after it.
	long := strings.Repeat("x", 200*1024)
	stdout := `{"Action":"output","Package":"example.com/x","Output":"` + long + `"}` + "\n" +
		`{"Action":"pass","Package":"example.com/x","Test":"TestOK","Elapsed":0.01}` + "\n" +
		`{"Action":"pass","Package":"example.com/x","Elapsed":0.01}` + "\n"

	r := parseGoTestResult(t, stdout, 0)
	if r.Passed != 1 || r.Status != goTestOK {
		t.Errorf("events after a long line were dropped: passed=%d status=%q", r.Passed, r.Status)
	}
}
