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

// TestStaleAnalyzerHint covers yaah-2vc: an analyzer too old for the toolchain
// exits non-zero with no diagnostics, which is indistinguishable from a real
// analysis failure unless the tool says what to do about it.
func TestStaleAnalyzerHint(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantHint   bool
		wantQuoted string
	}{
		{
			name:       "export data version mismatch",
			output:     "staticcheck: cannot read export data: version 5 > maximum supported 4",
			wantHint:   true,
			wantQuoted: "version 5 > maximum supported 4",
		},
		{
			name:       "maximum supported phrasing alone",
			output:     "-: version 5 > maximum supported 4",
			wantHint:   true,
			wantQuoted: "maximum supported",
		},
		{
			name:     "genuine compile failure gets no hint",
			output:   "-: # example.com/pkg\n./x.go:3:2: undefined: nope",
			wantHint: false,
		},
		{
			name:     "clean analysis gets no hint",
			output:   "",
			wantHint: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleAnalyzerHint(tt.output)
			if tt.wantHint {
				if got == "" {
					t.Fatalf("staleAnalyzerHint(%q) = empty; want a hint", tt.output)
				}
				if !strings.Contains(got, "go install honnef.co/go/tools/cmd/staticcheck@latest") {
					t.Errorf("hint does not name the fix: %q", got)
				}
				if tt.wantQuoted != "" && !strings.Contains(got, tt.wantQuoted) {
					t.Errorf("hint does not quote the offending line %q: %q", tt.wantQuoted, got)
				}
				return
			}
			if got != "" {
				t.Errorf("staleAnalyzerHint(%q) = %q; want no hint", tt.output, got)
			}
		})
	}
}

// TestStaticcheckStaleAnalyzerWiring drives the hint through Execute, so the
// error path itself is covered rather than only the pure helper.
func TestStaticcheckStaleAnalyzerWiring(t *testing.T) {
	r := runStaticcheck(t, func(req ExecRequest) (ExecResult, error) {
		if req.Command == "go" {
			return ExecResult{ExitCode: 0}, nil
		}
		// A stale analyzer: non-zero, no diagnostics, only the version line.
		return ExecResult{ExitCode: 1, Stdout: "-: version 5 > maximum supported 4\n"}, nil
	}, `{"packages":"./...","analyzers":"staticcheck"}`)

	if r.Status != staticcheckError {
		t.Errorf("status = %q; want %q", r.Status, staticcheckError)
	}
	if r.Count != 0 {
		t.Errorf("count = %d; want 0 (nothing was analysed)", r.Count)
	}
	if !strings.Contains(r.Stderr, "too old for this Go toolchain") {
		t.Errorf("stderr does not carry the hint: %q", r.Stderr)
	}
	if !strings.Contains(r.Stderr, "version 5 > maximum supported 4") {
		t.Errorf("stderr does not quote the offending line: %q", r.Stderr)
	}
}

// TestStaticcheckCompileFailureGetsNoStaleHint is the guard against the loose
// gate: a genuine compile failure must not be blamed on the analyzer version.
func TestStaticcheckCompileFailureGetsNoStaleHint(t *testing.T) {
	r := runStaticcheck(t, func(req ExecRequest) (ExecResult, error) {
		if req.Command == "go" {
			return ExecResult{ExitCode: 0}, nil
		}
		return ExecResult{ExitCode: 1, Stdout: "-: # example.com/pkg\n./x.go:3:2: undefined: nope\n"}, nil
	}, `{"packages":"./...","analyzers":"staticcheck"}`)

	if strings.Contains(r.Stderr, "too old for this Go toolchain") {
		t.Errorf("compile failure was misdiagnosed as a stale analyzer: %q", r.Stderr)
	}
}
