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
func runGoMod(t *testing.T, args string, respond func(req ExecRequest) (ExecResult, error)) (goModResult, error) {
	t.Helper()
	tool := &GoModTool{WS: &fakeExecWorkspace{respond: respond}}
	raw, err := tool.Execute(context.Background(), args)
	var got goModResult
	if raw != "" {
		if decodeErr := json.Unmarshal([]byte(raw), &got); decodeErr != nil {
			t.Fatalf("decode result: %v\n%s", decodeErr, raw)
		}
	}
	return got, err
}

func hasArgs(req ExecRequest, want ...string) bool {
	return strings.Join(req.Args, " ") == strings.Join(want, " ")
}

// TestGoMod_VerifyFailureCarriesStderr pins yaah-8p8: go writes the reason to
// stderr, and success:false without that text is an unexplained failure.
func TestGoMod_VerifyFailureCarriesStderr(t *testing.T) {
	got, err := runGoMod(t, `{"action":"verify"}`, func(req ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "missing go.sum entry for module example.com/x"},
			errors.New("exit status 1")
	})

	if err == nil {
		t.Fatal("a failed verify must return a non-nil error")
	}
	if !strings.Contains(err.Error(), "missing go.sum entry") {
		t.Errorf("error lost the go diagnostic: %v", err)
	}
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
	got, err := runGoMod(t, `{"action":"add","module":"example.com/x"}`, func(req ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "unknown revision example.com/x@latest"},
			errors.New("exit status 1")
	})

	if err == nil {
		t.Fatal("a failed add must return a non-nil error")
	}
	if !strings.Contains(err.Error(), "unknown revision") {
		t.Errorf("error lost the go diagnostic: %v", err)
	}
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
	got, err := runGoMod(t, `{"action":"remove","module":"example.com/x"}`, func(req ExecRequest) (ExecResult, error) {
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

	if err == nil {
		t.Fatal("a failed post-remove tidy must return a non-nil error")
	}
	if !strings.Contains(err.Error(), "updates to go.mod needed") {
		t.Errorf("error lost the tidy diagnostic: %v", err)
	}
	if got.Success {
		t.Error("success = true despite a failed post-remove tidy")
	}
	if !strings.Contains(got.Stderr, "tidy after remove") || !strings.Contains(got.Stderr, "updates to go.mod needed") {
		t.Errorf("stderr = %q, want the tidy failure and its cause", got.Stderr)
	}
}

func TestGoMod_UpgradeTidyFailureFlipsSuccess(t *testing.T) {
	got, err := runGoMod(t, `{"action":"upgrade","module":"example.com/x"}`, func(req ExecRequest) (ExecResult, error) {
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

	if err == nil {
		t.Fatal("a failed post-upgrade tidy must return a non-nil error")
	}
	if !strings.Contains(err.Error(), "inconsistent vendoring") {
		t.Errorf("error lost the tidy diagnostic: %v", err)
	}
	if got.Success {
		t.Error("success = true despite a failed post-upgrade tidy")
	}
	if !strings.Contains(got.Stderr, "tidy after upgrade") || !strings.Contains(got.Stderr, "inconsistent vendoring") {
		t.Errorf("stderr = %q, want the tidy failure and its cause", got.Stderr)
	}
}

// TestGoMod_DryRunAddResolvesVersions pins yaah-k9x: dry_run used to print a
// canned "would run" string; it must query the module state read-only instead.
func TestGoMod_DryRunAddResolvesVersions(t *testing.T) {
	got, err := runGoMod(t, `{"action":"add","module":"example.com/x","dry_run":true}`, func(req ExecRequest) (ExecResult, error) {
		if !hasArgs(req, "list", "-m", "-versions", "example.com/x") {
			t.Errorf("dry_run add must query versions via go list, got %v", req.Args)
		}
		return ExecResult{Stdout: "example.com/x v1.0.0 v1.1.0"}, nil
	})
	if err != nil {
		t.Fatalf("dry_run add: %v", err)
	}
	if !got.Success {
		t.Error("a successful preview must be success:true")
	}
	if !strings.Contains(got.Output, "v1.1.0") {
		t.Errorf("preview lost the resolved versions: %q", got.Output)
	}
	if !strings.Contains(got.Output, "would run: go get -- example.com/x@latest") {
		t.Errorf("preview lost the command it stands in for: %q", got.Output)
	}
}

// TestGoMod_DryRunRemoveReportsCurrentRequirement: the remove preview says
// what is required now, or that nothing is, instead of a canned string.
func TestGoMod_DryRunRemoveReportsCurrentRequirement(t *testing.T) {
	got, err := runGoMod(t, `{"action":"remove","module":"example.com/x","dry_run":true}`, func(req ExecRequest) (ExecResult, error) {
		if !hasArgs(req, "list", "-m", "example.com/x") {
			t.Errorf("dry_run remove must query the build list, got %v", req.Args)
		}
		return ExecResult{Stdout: "example.com/x v1.1.0"}, nil
	})
	if err != nil {
		t.Fatalf("dry_run remove: %v", err)
	}
	if !got.Success {
		t.Error("a successful preview must be success:true")
	}
	if !strings.Contains(got.Output, "currently required: example.com/x v1.1.0") {
		t.Errorf("preview lost the current requirement: %q", got.Output)
	}
}

// TestGoMod_DryRunUpgradeListsCurrentVersions: upgrade cannot predict MVS
// results, so the preview shows the current state and says the run decides.
func TestGoMod_DryRunUpgradeListsCurrentVersions(t *testing.T) {
	got, err := runGoMod(t, `{"action":"upgrade","dry_run":true}`, func(req ExecRequest) (ExecResult, error) {
		if !hasArgs(req, "list", "-m", "all") {
			t.Errorf("dry_run upgrade must list current modules, got %v", req.Args)
		}
		return ExecResult{Stdout: "go 1.26\nexample.com/x v1.1.0"}, nil
	})
	if err != nil {
		t.Fatalf("dry_run upgrade: %v", err)
	}
	if !got.Success {
		t.Error("a successful preview must be success:true")
	}
	if !strings.Contains(got.Output, "current versions:") || !strings.Contains(got.Output, "resolved by the upgrade run") {
		t.Errorf("preview does not state current versions and the resolution caveat: %q", got.Output)
	}
}

// TestGoMod_DryRunRemoveNotRequired pins yaah-k9x follow-up: "not a known
// dependency" is the preview's answer, not a failure.
func TestGoMod_DryRunRemoveNotRequired(t *testing.T) {
	got, err := runGoMod(t, `{"action":"remove","module":"example.com/x","dry_run":true}`, func(req ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "go: example.com/x: not a known dependency"}, errors.New("exit status 1")
	})
	if err != nil {
		t.Fatalf("not-required must be a successful preview: %v", err)
	}
	if !got.Success {
		t.Error("not-required preview must be success:true")
	}
	if !strings.Contains(got.Output, "not currently required") {
		t.Errorf("preview lost the not-required answer: %q", got.Output)
	}
}

// TestGoMod_DryRunRemoveRealFailureIsNotDressedUp: a go list failure that is
// not the not-required answer must fail with the diagnostic, not claim the
// module is absent.
func TestGoMod_DryRunRemoveRealFailureIsNotDressedUp(t *testing.T) {
	got, err := runGoMod(t, `{"action":"remove","module":"example.com/x","dry_run":true}`, func(req ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "go: cannot find main module"}, errors.New("exit status 1")
	})
	if err == nil {
		t.Fatal("a real go list failure must return a non-nil error")
	}
	if got.Success {
		t.Error("success = true after a failed preview query")
	}
	if !strings.Contains(err.Error(), "cannot find main module") {
		t.Errorf("error lost the go diagnostic: %v", err)
	}
}

// TestGoMod_DryRunUpgradeScopedToModule: a single-module upgrade preview
// queries that module's current version, not the full module graph.
func TestGoMod_DryRunUpgradeScopedToModule(t *testing.T) {
	got, err := runGoMod(t, `{"action":"upgrade","module":"example.com/x","dry_run":true}`, func(req ExecRequest) (ExecResult, error) {
		if !hasArgs(req, "list", "-m", "example.com/x") {
			t.Errorf("dry_run upgrade must scope the query to the module, got %v", req.Args)
		}
		return ExecResult{Stdout: "example.com/x v1.1.0"}, nil
	})
	if err != nil {
		t.Fatalf("dry_run upgrade: %v", err)
	}
	if !strings.Contains(got.Output, "would run: go get -u -- example.com/x") {
		t.Errorf("preview lost the module-scoped command: %q", got.Output)
	}
}
