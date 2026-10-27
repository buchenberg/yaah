package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// goModExec routes a go_mod call to canned output. Every case uses the same
// fake workspace so the tool's command plumbing (SeparateStreams) is exercised.
func runGoMod(t *testing.T, args string, respond func(req ExecRequest) (ExecResult, error)) goModResult {
	t.Helper()
	tool := &GoModTool{WS: &fakeExecWorkspace{respond: respond}}
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var got goModResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	return got
}

func hasArgs(req ExecRequest, want ...string) bool {
	return strings.Join(req.Args, " ") == strings.Join(want, " ")
}

// TestGoMod_VerifyFailureCarriesStderr pins yaah-8p8: go writes the reason to
// stderr, and success:false without that text is an unexplained failure.
func TestGoMod_VerifyFailureCarriesStderr(t *testing.T) {
	got := runGoMod(t, `{"action":"verify"}`, func(req ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "missing go.sum entry for module example.com/x"},
			errors.New("exit status 1")
	})

	if got.Success {
		t.Error("success = true after a failed verify")
	}
	if !strings.Contains(got.Stderr, "missing go.sum entry") {
		t.Errorf("stderr lost the go error: %q", got.Stderr)
	}
	if len(got.Modules) != 0 {
		t.Errorf("failure text was parsed as modules: %+v", got.Modules)
	}
}

func TestGoMod_AddFailureCarriesStderr(t *testing.T) {
	got := runGoMod(t, `{"action":"add","module":"example.com/x"}`, func(req ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "unknown revision example.com/x@latest"},
			errors.New("exit status 1")
	})

	if got.Success {
		t.Error("success = true after a failed add")
	}
	if !strings.Contains(got.Stderr, "unknown revision") {
		t.Errorf("stderr lost the go error: %q", got.Stderr)
	}
}

// TestGoMod_RemoveTidyFailureFlipsSuccess: the mutation may succeed but leave
// go.mod inconsistent; a failed tidy must be reported with its own error and
// must make the call unsuccessful.
func TestGoMod_RemoveTidyFailureFlipsSuccess(t *testing.T) {
	got := runGoMod(t, `{"action":"remove","module":"example.com/x"}`, func(req ExecRequest) (ExecResult, error) {
		switch {
		case hasArgs(req, "get", "--", "example.com/x@none"):
			return ExecResult{Stdout: "removed example.com/x\n"}, nil
		case hasArgs(req, "mod", "tidy"):
			return ExecResult{ExitCode: 1, Stderr: "go: updates to go.mod needed"},
				errors.New("exit status 1")
		default:
			t.Fatalf("unexpected go command: %v", req.Args)
			return ExecResult{}, nil
		}
	})

	if got.Success {
		t.Error("success = true despite a failed post-remove tidy")
	}
	if !strings.Contains(got.Stderr, "tidy after remove") || !strings.Contains(got.Stderr, "updates to go.mod needed") {
		t.Errorf("stderr = %q, want the tidy failure and its cause", got.Stderr)
	}
}

func TestGoMod_UpgradeTidyFailureFlipsSuccess(t *testing.T) {
	got := runGoMod(t, `{"action":"upgrade","module":"example.com/x"}`, func(req ExecRequest) (ExecResult, error) {
		switch {
		case hasArgs(req, "get", "-u", "--", "example.com/x"):
			return ExecResult{Stdout: "upgraded example.com/x\n"}, nil
		case hasArgs(req, "mod", "tidy"):
			return ExecResult{ExitCode: 1, Stderr: "go: inconsistent vendoring"},
				errors.New("exit status 1")
		default:
			t.Fatalf("unexpected go command: %v", req.Args)
			return ExecResult{}, nil
		}
	})

	if got.Success {
		t.Error("success = true despite a failed post-upgrade tidy")
	}
	if !strings.Contains(got.Stderr, "tidy after upgrade") || !strings.Contains(got.Stderr, "inconsistent vendoring") {
		t.Errorf("stderr = %q, want the tidy failure and its cause", got.Stderr)
	}
}
