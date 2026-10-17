package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/buchenberg/yaah/internal/prompts"
)

// Status values for goTestResult.Status. "error" is distinct from "failed":
// failed means tests ran and some did not pass, error means the run itself did
// not happen (toolchain missing, build never produced a test, or the time limit
// killed the process). A caller reading only the counts cannot tell the two
// apart, which is why the field exists.
const (
	goTestOK     = "ok"
	goTestFailed = "failed"
	goTestError  = "error"
)

// GoTestTool runs `go test -json` and returns structured results.
type GoTestTool struct {
	WS Workspace
}

var _ WorkspaceSetter = (*GoTestTool)(nil)

func (t *GoTestTool) SetWorkspace(ws Workspace) { t.WS = ws }

func NewGoTestTool() *GoTestTool { return &GoTestTool{} }

func (t *GoTestTool) Name() string        { return "go_test" }
func (t *GoTestTool) Description() string { return prompts.ToolDescription("go_test") }

func (t *GoTestTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"packages": {
				"type": "string",
				"description": "Go package pattern relative to the workspace, like './...'"
			},
			"timeout_seconds": {
				"type": "integer",
				"description": "Timeout in seconds for the test run (default 120)"
			},
			"flags": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Extra go test flags, e.g. ['-v', '-count=1', '-run', 'TestFoo']"
			},
			"coverprofile": {
				"type": "boolean",
				"description": "If true, run with -coverprofile and include coverage summary"
			}
		},
		"required": ["packages"]
	}`)
}

type goTestParams struct {
	Packages       string   `json:"packages"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Flags          []string `json:"flags"`
	Coverprofile   bool     `json:"coverprofile"`
}

// testEvent is a single JSON line from "go test -json". ImportPath and
// FailedBuild appear on build events, which carry no Package or Test: a package
// that does not compile is reported as build-output/build-fail, not as a failed
// test, so ignoring these fields makes a broken build look like a green run.
type testEvent struct {
	Time       string  `json:"Time"`
	Action     string  `json:"Action"`
	Package    string  `json:"Package"`
	Test       string  `json:"Test"`
	Output     string  `json:"Output"`
	Elapsed    float64 `json:"Elapsed"`
	ImportPath string  `json:"ImportPath"`
}

type goTestPkgResult struct {
	Package string  `json:"package"`
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Skipped int     `json:"skipped"`
	Elapsed float64 `json:"elapsed_seconds"`
}

type goTestResult struct {
	Status      string            `json:"status"`
	Passed      int               `json:"passed"`
	Failed      int               `json:"failed"`
	Skipped     int               `json:"skipped"`
	Total       int               `json:"total"`
	Elapsed     float64           `json:"elapsed_seconds"`
	Packages    []goTestPkgResult `json:"packages"`
	Coverage    string            `json:"coverage,omitempty"`
	FailedTests []string          `json:"failed_tests,omitempty"`
	// BuildOutput is the verbatim compiler output of packages that failed to
	// build, and FailedBuilds names them. Without them a build failure is
	// invisible: it produces zero test events.
	BuildOutput  string   `json:"build_output,omitempty"`
	FailedBuilds []string `json:"failed_builds,omitempty"`
	// RawOutput preserves the stdout tail when no test events could be parsed,
	// so an unusable run is diagnosable instead of looking like an empty pass.
	RawOutput string `json:"raw_output,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
}

func (t *GoTestTool) Execute(ctx context.Context, args string) (string, error) {
	var params goTestParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("go_test: invalid arguments: %w", err)
	}

	timeout := params.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}

	coverFile := ""
	if params.Coverprofile {
		coverFile = fmt.Sprintf("coverage-%d.out", os.Getpid())
	}

	cmdArgs := []string{"test", "-json", "-timeout", fmt.Sprintf("%ds", timeout)}
	if coverFile != "" {
		cmdArgs = append(cmdArgs, "-coverprofile="+coverFile)
	}
	cmdArgs = append(cmdArgs, params.Packages)
	cmdArgs = append(cmdArgs, params.Flags...)

	ws := workspaceOf(t.WS, nil)

	// Streams are kept apart because the JSON event stream is parsed from stdout
	// alone; combined output would interleave build errors into it.
	res, execErr := ws.Exec(ctx, ExecRequest{Command: "go", Args: cmdArgs, SeparateStreams: true})

	// A non-zero exit is how go test reports failing tests, so it is not an
	// error. A non-positive exit with no output means the process never started
	// (missing go, snap-confine abort, a workspace that returns the zero-value
	// ExecResult with an error) — the old code discarded that error, so it
	// produced {"passed":0,"failed":0,"exit_code":1}, which reads as green.
	//
	// A kill after output (OOM, SIGKILL) is different: the JSON stream is
	// partial but reviewable, so it becomes a structured error result instead
	// of a hard error. A cancelled context is handled the same way below.
	killedWithOutput := false
	if execErr != nil && res.ExitCode <= 0 && ctx.Err() == nil {
		// Only stdout is evidence that go ran: it is the JSON event stream. A
		// launch failure explains itself on stderr and produced none.
		killedWithOutput = strings.TrimSpace(res.Stdout) != ""
		if !killedWithOutput {
			detail := strings.TrimSpace(res.Stderr)
			if detail != "" {
				return "", fmt.Errorf("go_test: could not run go: %w\n%s", execErr, detail)
			}
			return "", fmt.Errorf("go_test: could not run go: %w", execErr)
		}
	}

	result := &goTestResult{
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
		TimedOut: ctx.Err() == context.DeadlineExceeded,
	}
	result.parse(res.Stdout)
	if (ctx.Err() != nil || killedWithOutput) && result.Status != goTestError {
		// A cancelled, timed-out, or externally killed run yields partial
		// events, but the counts are not a verdict: the suite was cut short.
		result.Status = goTestError
	}
	if result.Status == goTestError || result.TimedOut {
		result.RawOutput = tailOutput(res.Stdout, 8*1024)
	}

	// Coverage summary
	if coverFile != "" {
		// The profile was written by the test run in the workspace, so it is read
		// and cleaned up there too, not on the host.
		coverRes, _ := ws.Exec(ctx, ExecRequest{
			Command:         "go",
			Args:            []string{"tool", "cover", "-func=" + coverFile},
			SeparateStreams: true,
		})
		if coverRes.ExitCode == 0 {
			lines := strings.Split(strings.TrimSpace(coverRes.Stdout), "\n")
			if len(lines) > 0 {
				result.Coverage = strings.TrimSpace(lines[len(lines)-1])
			}
		}
		// coverFile is relative to the command's working directory (the
		// workspace), so resolve it against ws.WorkDir(): a bare relative path
		// would be resolved against the process cwd instead, leaking the
		// profile in a worktree and risking deletion of an unrelated file.
		_ = ws.Remove(ctx, ws.Join(ws.WorkDir(), coverFile))
	}

	outBytes, _ := json.MarshalIndent(result, "", "  ")
	return string(outBytes), nil
}

// parse fills the result from the "go test -json" stream and decides its status.
func (r *goTestResult) parse(stdout string) {
	pkgMap := map[string]*goTestPkgResult{}
	var failedTests []string
	var failedBuilds []string
	var buildOutput strings.Builder
	var totalElapsed float64
	sawPackage := false
	goTimedOut := false

	scanner := bufio.NewScanner(strings.NewReader(stdout))
	// Generated code and panic traces produce long output lines; the default
	// 64 KiB token limit would stop the scan and drop the rest of the stream.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev testEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			// Not a JSON event. Older toolchains print build errors as plain
			// text here; keep the raw stream for the error path.
			continue
		}

		switch ev.Action {
		case "build-output":
			buildOutput.WriteString(ev.Output)
			continue
		case "build-fail":
			if ev.ImportPath != "" && !slices.Contains(failedBuilds, ev.ImportPath) {
				failedBuilds = append(failedBuilds, ev.ImportPath)
			}
			continue
		case "output":
			// go test -timeout prints this through an output event and exits
			// non-zero without a per-test fail event; without it a timed-out
			// suite is indistinguishable from a normal test failure.
			if strings.Contains(ev.Output, "test timed out after") {
				goTimedOut = true
			}
		}
		if ev.Package == "" {
			continue
		}
		sawPackage = true

		pkg, ok := pkgMap[ev.Package]
		if !ok {
			pkg = &goTestPkgResult{Package: ev.Package}
			pkgMap[ev.Package] = pkg
		}

		switch ev.Action {
		case "pass":
			if ev.Test != "" {
				pkg.Passed++
				r.Passed++
			}
			pkg.Elapsed = ev.Elapsed
		case "fail":
			if ev.Test != "" {
				pkg.Failed++
				r.Failed++
				failedTests = append(failedTests, fmt.Sprintf("%s/%s", ev.Package, ev.Test))
			}
			pkg.Elapsed = ev.Elapsed
		case "skip":
			if ev.Test != "" {
				pkg.Skipped++
				r.Skipped++
			}
		}
		totalElapsed += ev.Elapsed
	}

	for _, pkg := range pkgMap {
		r.Packages = append(r.Packages, *pkg)
	}
	sort.Slice(r.Packages, func(i, j int) bool { return r.Packages[i].Package < r.Packages[j].Package })
	r.Total = r.Passed + r.Failed + r.Skipped
	r.Elapsed = totalElapsed
	r.FailedTests = failedTests
	r.FailedBuilds = failedBuilds
	r.BuildOutput = strings.TrimRight(buildOutput.String(), "\n")
	r.TimedOut = r.TimedOut || goTimedOut
	r.Status = goTestStatus(r.ExitCode, r.Total, r.Failed, r.TimedOut, r.BuildOutput != "" || len(r.FailedBuilds) > 0, sawPackage)

	// If not one test event parsed, everything above is empty; hand back the raw
	// stream so the caller can see why instead of concluding the suite is green.
	if r.Status == goTestError || r.TimedOut {
		r.RawOutput = tailOutput(stdout, 8*1024)
	}
}

// goTestStatus separates "the tests failed" from "the run did not happen".
//
// sawPackage is true when go emitted at least one package-scoped event. Exit 0
// with no such event means nothing ran: `-c` only compiles, `-json=false`
// produces text this parser discards, and a pattern matching no packages emits
// nothing. A zero-test exit 0 with package events is legitimate (no test files).
//
// buildFailed means the failure came from compilation (build-output/build-fail)
// rather than from a test, which decides error vs failed when no test event
// carried a verdict.
func goTestStatus(exitCode, total, failed int, timedOut, buildFailed, sawPackage bool) string {
	switch {
	case timedOut:
		// A timed-out run never completed, whatever it counted before dying;
		// the partial counts and raw_output still travel with the error.
		return goTestError
	case exitCode == 0:
		if !sawPackage {
			return goTestError
		}
		if failed > 0 {
			return goTestFailed
		}
		return goTestOK
	case total == 0:
		if buildFailed {
			return goTestError
		}
		if sawPackage {
			// The package ran and failed without per-test events: TestMain
			// exited non-zero, or init panicked before the first test.
			return goTestFailed
		}
		return goTestError
	default:
		return goTestFailed
	}
}

// tailOutput returns at most the last n bytes of s, starting on a rune
// boundary, with a marker when it truncated.
func tailOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "...[output truncated]...\n" + s[cut:]
}
