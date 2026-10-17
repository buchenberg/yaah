package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func runStaticcheck(t *testing.T, respond func(ExecRequest) (ExecResult, error), args string) staticcheckResult {
	t.Helper()
	out, err := (&StaticcheckTool{WS: &fakeExecWorkspace{respond: respond}}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var r staticcheckResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	return r
}

func TestStaticcheck_CleanRun(t *testing.T) {
	r := runStaticcheck(t, func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 0}, nil
	}, `{"packages":"./..."}`)
	if r.Status != staticcheckOK || r.Count != 0 {
		t.Errorf("status = %q count = %d, want ok/0", r.Status, r.Count)
	}
}

func TestStaticcheck_DiagnosticsAreNotAnError(t *testing.T) {
	r := runStaticcheck(t, func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stdout: "./x.go:3:2: unreachable code\n"}, nil
	}, `{"packages":"./..."}`)
	if r.Status != staticcheckOK {
		t.Errorf("status = %q, want ok (the analyzer ran and found issues)", r.Status)
	}
	if r.Count != 1 {
		t.Errorf("count = %d, want 1", r.Count)
	}
}

// TestStaticcheck_ExecFailureIsAnError: the exec error used to be discarded, so
// a toolchain that never started produced zero diagnostics and an "available"
// flag with no cause attached.
func TestStaticcheck_ExecFailureIsAnError(t *testing.T) {
	r := runStaticcheck(t, func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: -1}, errors.New(`exec: "go": executable file not found in $PATH`)
	}, `{"packages":"./..."}`)
	if r.Status != staticcheckError {
		t.Errorf("status = %q, want error", r.Status)
	}
	if !strings.Contains(r.Stderr, "could not run") || !strings.Contains(r.Stderr, "executable file not found") {
		t.Errorf("stderr must carry the cause, got %q", r.Stderr)
	}
}

// TestStaticcheck_AnalyzerFailureWithoutDiagnostics: a vet run that exits
// non-zero and matches no diagnostic line is a failed run, not clean code.
func TestStaticcheck_AnalyzerFailureWithoutDiagnostics(t *testing.T) {
	r := runStaticcheck(t, func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stdout: "vet: no packages to vet\n"}, nil
	}, `{"packages":"./..."}`)
	if r.Status != staticcheckError {
		t.Errorf("status = %q, want error", r.Status)
	}
	if !strings.Contains(r.Stdout, "no packages to vet") {
		t.Errorf("the raw analyzer output must survive, got %q", r.Stdout)
	}
}

// TestStaticcheck_PartialRunIsAnError: "both" with staticcheck missing is not a
// clean result, even when vet ran and found nothing.
func TestStaticcheck_PartialRunIsAnError(t *testing.T) {
	r := runStaticcheck(t, func(req ExecRequest) (ExecResult, error) {
		if req.Command == "staticcheck" {
			return ExecResult{ExitCode: 127}, errors.New("exit status 127")
		}
		return ExecResult{ExitCode: 0}, nil
	}, `{"packages":"./...","analyzers":"both"}`)
	if r.Status != staticcheckError {
		t.Errorf("status = %q, want error when a requested analyzer did not run", r.Status)
	}
	if !r.VetAvailable || r.StaticcheckAvailable {
		t.Errorf("availability = vet:%v staticcheck:%v, want true/false", r.VetAvailable, r.StaticcheckAvailable)
	}
	if !strings.Contains(r.Stderr, "staticcheck not found") {
		t.Errorf("stderr must explain the missing analyzer, got %q", r.Stderr)
	}
}

func TestStaticcheck_RejectsUnknownAnalyzers(t *testing.T) {
	out, err := (&StaticcheckTool{WS: &fakeExecWorkspace{respond: func(ExecRequest) (ExecResult, error) {
		t.Fatal("no analyzer should run for an invalid request")
		return ExecResult{}, nil
	}}}).Execute(context.Background(), `{"packages":"./...","analyzers":"golint"}`)
	if err == nil {
		t.Fatalf("expected an error, got %q", out)
	}
	if !strings.Contains(err.Error(), "analyzers must be one of") {
		t.Errorf("error = %q", err)
	}
}

// TestStaticcheck_BuildFailureIsAnErrorNotADiagnostic: `go vet` on a package
// that does not compile prints a `# pkg` header and then
// `vet: x.go:3:9: undefined: foo`, which matches diagRe with the bogus file
// "vet: x.go". That must be a failed run with the compiler output verbatim,
// not a warning-level finding on a package vet never analyzed.
func TestStaticcheck_BuildFailureIsAnErrorNotADiagnostic(t *testing.T) {
	r := runStaticcheck(t, func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stdout: "# example.com/x\nvet: x.go:3:9: undefined: foo\n"}, nil
	}, `{"packages":"./..."}`)
	if r.Status != staticcheckError {
		t.Errorf("status = %q, want error (nothing was analyzed)", r.Status)
	}
	if r.Count != 0 {
		t.Errorf("count = %d, want 0 (compiler errors are not findings)", r.Count)
	}
	if !strings.Contains(r.Stdout, "undefined: foo") {
		t.Errorf("compiler output must survive verbatim, got %q", r.Stdout)
	}
}

func TestStaticcheck_StaticcheckLoadFailureIsAnError(t *testing.T) {
	r := runStaticcheck(t, func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stdout: "-: # example.com/x\nx.go:3:9: undefined: foo (compile)\n"}, nil
	}, `{"packages":"./...","analyzers":"staticcheck"}`)
	if r.Status != staticcheckError {
		t.Errorf("status = %q, want error", r.Status)
	}
	if r.Count != 0 {
		t.Errorf("count = %d, want 0", r.Count)
	}
}
