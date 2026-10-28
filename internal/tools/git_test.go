package tools

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// isolatedExecWorkspace is a workspace that is not the host — an isolated
// sub-agent lane. Local() is the only thing here that differs from
// fakeExecWorkspace, and it is what a tool would consult before taking a
// host-only path, so asserting behaviour across it states the isolated case
// instead of assuming it.
type isolatedExecWorkspace struct {
	*fakeExecWorkspace
}

func (isolatedExecWorkspace) Local() bool { return false }

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func TestGitTool_status(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	gt := &GitTool{}
	result, err := gt.Execute(context.Background(), `{"action":"status"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// status --porcelain returns something (possibly empty) without error
	t.Logf("status result: %q", result)
}

func TestGitTool_rejectsUnsupportedAction(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{"action":"rebase"}`)
	if err == nil {
		t.Fatal("expected error for unsupported action")
	}
}

func TestGitTool_returnsErrorForEmptyAction(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error for empty action")
	}
}

func TestGitTool_addRequiresPaths(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{"action":"add"}`)
	if err == nil {
		t.Fatal("expected error for add without paths")
	}
}

func TestGitTool_commitRequiresMessage(t *testing.T) {
	gt := &GitTool{}
	_, err := gt.Execute(context.Background(), `{"action":"commit"}`)
	if err == nil {
		t.Fatal("expected error for commit without message")
	}
}

func TestGitTool_isDangerousForMutatingActions(t *testing.T) {
	gt := &GitTool{}
	tests := []struct {
		args      string
		dangerous bool
	}{
		{`{"action":"status"}`, false},
		{`{"action":"diff"}`, false},
		{`{"action":"diff_cached"}`, false},
		{`{"action":"log"}`, false},
		{`{"action":"show"}`, false},
		{`{"action":"branch"}`, false},
		{`{"action":"add"}`, true},
		{`{"action":"commit"}`, true},
		{`{"action":"push"}`, true},
		{`{"action":"pull"}`, true},
		{`{"action":"fetch"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			if got := gt.IsDangerous(tt.args); got != tt.dangerous {
				t.Errorf("IsDangerous(%s) = %v, want %v", tt.args, got, tt.dangerous)
			}
		})
	}
}

// TestGitTool_branchDangerIsArgumentAware pins the yaah-5m5 fix: a branch
// call that mutates refs must reach the approval gate even though the action
// table classifies `branch` as safe, because the same action both lists refs
// and deletes them. -D is the case that matters: it deletes a branch whose
// commits are unmerged, which -d refuses to do. An unrecognized option is
// assumed mutating rather than trusted.
func TestGitTool_branchDangerIsArgumentAware(t *testing.T) {
	gt := &GitTool{}
	tests := []struct {
		name      string
		args      string
		dangerous bool
	}{
		{"bare listing", `{"action":"branch"}`, false},
		{"all", `{"action":"branch","flags":["-a"]}`, false},
		{"remote listing", `{"action":"branch","flags":["-r"]}`, false},
		{"list", `{"action":"branch","flags":["--list"]}`, false},
		// -l is version-dependent --list (git >= 2.16) / --create-reflog
		// (git <= 2.15), where `git branch -l <name>` created a branch;
		// the guard refuses rather than guessing the workspace's git.
		{"short list flag refused", `{"action":"branch","flags":["-l"]}`, true},
		{"short list with pattern refused", `{"action":"branch","flags":["-l"],"paths":["feat*"]}`, true},
		{"contains with value", `{"action":"branch","flags":["--contains","x"]}`, false},
		{"contains value via paths", `{"action":"branch","flags":["--contains"],"paths":["main"]}`, false},
		{"contains equals form", `{"action":"branch","flags":["--contains=main"]}`, false},
		{"merged with value", `{"action":"branch","flags":["--merged","main"]}`, false},
		{"points-at with value", `{"action":"branch","flags":["--points-at","HEAD"]}`, false},
		{"merged does not swallow a following delete", `{"action":"branch","flags":["--merged","-D"],"paths":["victim"]}`, true},
		{"points-at does not swallow a following delete", `{"action":"branch","flags":["--points-at","-D"],"paths":["victim"]}`, true},
		{"flag-like value is classified, not swallowed", `{"action":"branch","flags":["--sort","-refname"]}`, true},
		{"show-current", `{"action":"branch","flags":["--show-current"]}`, false},
		{"list pattern", `{"action":"branch","flags":["--list"],"paths":["feat*"]}`, false},
		{"delete forced", `{"action":"branch","flags":["-D","feature"]}`, true},
		{"delete unforced", `{"action":"branch","flags":["-d","x"]}`, true},
		{"move short", `{"action":"branch","flags":["-m","old","new"]}`, true},
		{"move forced", `{"action":"branch","flags":["-M","old","new"]}`, true},
		{"copy short", `{"action":"branch","flags":["-c","old","new"]}`, true},
		{"copy forced", `{"action":"branch","flags":["-C","old","new"]}`, true},
		{"delete long", `{"action":"branch","flags":["--delete","x"]}`, true},
		{"move long", `{"action":"branch","flags":["--move","a","b"]}`, true},
		{"force", `{"action":"branch","flags":["-f","feature"]}`, true},
		{"force long", `{"action":"branch","flags":["--force","feature"]}`, true},
		{"set upstream long", `{"action":"branch","flags":["--set-upstream-to","origin/main"]}`, true},
		{"set upstream short", `{"action":"branch","flags":["-u","origin/main"]}`, true},
		{"unset upstream", `{"action":"branch","flags":["--unset-upstream"]}`, true},
		{"edit description", `{"action":"branch","flags":["--edit-description"]}`, true},
		{"create via paths", `{"action":"branch","paths":["feature"]}`, true},
		{"unknown long flag", `{"action":"branch","flags":["--output=/tmp/pwned"]}`, true},
		{"unknown short flag", `{"action":"branch","flags":["-Z"]}`, true},
		// --abbrev takes an optional argument, so a separate -D is a real
		// delete to git, not the option's value (review 612).
		{"abbrev does not consume a following delete", `{"action":"branch","flags":["--abbrev","-D"],"paths":["victim"]}`, true},
		{"abbrev equals form stays read-only", `{"action":"branch","flags":["--abbrev=7","--list"]}`, false},
		{"bare name in flags", `{"action":"branch","flags":["feature"]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gt.IsDangerous(tt.args); got != tt.dangerous {
				t.Errorf("IsDangerous(%s) = %v, want %v", tt.args, got, tt.dangerous)
			}
		})
	}
}

// TestBranchFlagTables_AreDisjoint is the tripwire for the yaah-5m5 class of
// bug: a flag that reads as harmless for one subcommand (-D, -m, -M, -C, -c,
// -u are diff/log letters) must never be read-only for branch.
func TestBranchFlagTables_AreDisjoint(t *testing.T) {
	for flag := range branchReadOnlyFlags {
		if branchMutatingFlags[flag] {
			t.Errorf("%q is in both the read-only and the mutating branch table", flag)
		}
	}
	for flag := range branchMutatingFlags {
		if kind, _ := classifyBranchFlag(flag); kind != branchFlagMutating {
			t.Errorf("mutating branch flag %q classifies as %v, want branchFlagMutating", flag, kind)
		}
	}
	for flag := range branchReadOnlyFlags {
		if kind, _ := classifyBranchFlag(flag); kind != branchFlagReadOnly {
			t.Errorf("read-only branch flag %q classifies as %v, want branchFlagReadOnly", flag, kind)
		}
	}
	// The diff-oriented letters a branch call must never inherit silently.
	for _, flag := range []string{"-D", "-d", "-M", "-m", "-C", "-c", "-u", "-f"} {
		if branchReadOnlyFlags[flag] {
			t.Errorf("%q must not be read-only for branch", flag)
		}
		if kind, _ := classifyBranchFlag(flag); kind != branchFlagMutating {
			t.Errorf("%q classifies as %v, want branchFlagMutating", flag, kind)
		}
	}
}

// TestValidateActionFlags_branch pins the execution-side half of yaah-5m5:
// branch does not fall back to the shared letter-keyed whitelist, an approved
// mutating option is forwarded so it can actually run, and an unrecognized
// option is refused rather than guessed at.
func TestValidateActionFlags_branch(t *testing.T) {
	tests := []struct {
		name    string
		flags   []string
		want    []string
		wantErr bool
	}{
		{name: "no flags"},
		{name: "listing", flags: []string{"--list", "-a"}, want: []string{"--list", "-a"}},
		{name: "contains with value", flags: []string{"--contains", "main"}, want: []string{"--contains", "main"}},
		{name: "short -l refused (version-dependent meaning)", flags: []string{"-l"}, wantErr: true},
		{name: "delete riding a value flag is classified, not swallowed", flags: []string{"--merged", "-D"}, want: []string{"--merged", "-D"}},
		{name: "flag-like value refused as unknown option", flags: []string{"--contains", "-Z"}, wantErr: true},
		// Branch names travel in `paths`, so a mutating option arrives bare:
		// -D and -m take no value flag, only positional refs.
		{name: "delete forced", flags: []string{"-D"}, want: []string{"-D"}},
		{name: "move", flags: []string{"-m"}, want: []string{"-m"}},
		{name: "set upstream with value", flags: []string{"--set-upstream-to", "origin/main"}, want: []string{"--set-upstream-to", "origin/main"}},
		{name: "output flag refused", flags: []string{"--output=/tmp/pwned.diff"}, wantErr: true},
		{name: "unknown short refused", flags: []string{"-Z"}, wantErr: true},
		{name: "bare name refused", flags: []string{"feature"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateActionFlags("branch", tt.flags, false)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateActionFlags(branch, %v) = %v, want error", tt.flags, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateActionFlags(branch, %v) error: %v", tt.flags, err)
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("validateActionFlags(branch, %v) = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

// TestValidateActionFlags_nonBranchUnchanged guards the split: other actions
// still go through the shared whitelist, so a dangerous action still rejects
// its flags outright.
func TestValidateActionFlags_nonBranchUnchanged(t *testing.T) {
	if _, err := validateActionFlags("push", []string{"--all"}, true); err == nil {
		t.Error("validateActionFlags(push, --all, dangerous) should reject flags")
	}
	if got, err := validateActionFlags("diff", []string{"--stat"}, false); err != nil || len(got) != 1 {
		t.Errorf("validateActionFlags(diff, --stat) = %v, %v; want [--stat], nil", got, err)
	}
}

func TestGitTool_isNotDangerousForInvalidJSON(t *testing.T) {
	gt := &GitTool{}
	if gt.IsDangerous(`bad json`) {
		t.Error("expected false for invalid JSON")
	}
}

func TestGitTool_rejectsOutputFlag(t *testing.T) {
	gt := &GitTool{}
	tests := []struct {
		args string
		msg  string
	}{
		{`{"action":"diff","flags":["--output=/tmp/pwned.diff"]}`, "--output= form"},
		{`{"action":"diff","flags":["--output","/tmp/pwned.diff"]}`, "bare --output flag"},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			_, err := gt.Execute(context.Background(), tt.args)
			if err == nil {
				t.Fatalf("expected rejection for %s", tt.msg)
			}
			if !strings.Contains(err.Error(), "not in the safe whitelist") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGitTool_rejectsFlagInjectionViaPaths(t *testing.T) {
	gt := &GitTool{}
	tests := []struct {
		args string
		msg  string
	}{
		{`{"action":"diff","paths":["--help"]}`, "flag injection via diff paths"},
		{`{"action":"push","paths":["--force"]}`, "flag injection via push paths"},
		{`{"action":"pull","paths":["--rebase"]}`, "flag injection via pull paths"},
		{`{"action":"fetch","paths":["--all"]}`, "flag injection via fetch paths"},
		{`{"action":"add","paths":["-f"]}`, "flag injection via add paths"},
		{`{"action":"status","paths":["--ignored"]}`, "flag injection via status paths"},
		{`{"action":"diff","paths":["legit-file","--help"]}`, "flag injection mixed with valid path"},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			_, err := gt.Execute(context.Background(), tt.args)
			if err == nil {
				t.Fatal("expected error for flag injection")
			}
			t.Logf("correctly rejected: %v", err)
		})
	}
}

func TestGitTool_allowsDotSlashDashFilenames(t *testing.T) {
	// validatePaths allows ./-foo since it starts with '.', not '-'.
	// This test just validates the guard logic — it doesn't need git.
	gt := &GitTool{}
	// status with ./-filename should pass validation (status is read-only, safe)
	_, err := gt.Execute(context.Background(), `{"action":"status","paths":["./-foo","./-bar"]}`)
	if err != nil && gitAvailable() {
		// If git is available and we still get an error, it should NOT be
		// the flag-injection error.
		if err.Error() == "git: path \"./-foo\" starts with '-'; use '././-foo' instead to prevent flag injection" {
			t.Fatalf("./-foo was incorrectly flagged: %v", err)
		}
	}
}

// TestGitTool_commitPathsAreScoped pins yaah-jd4. A commit carrying `paths` used
// to drop them on the floor: the tool committed whatever else was staged and
// reported success, so "commit these two files" silently meant "commit
// everything". The paths are now forwarded as a trailing pathspec after `--`,
// which is the only form that both scopes the commit and stops a path being
// re-read as an option. The argv is pinned through the Exec seam rather than by
// committing for real, so the assertion is about what git is told, not about
// what a particular git version does with it.
func TestGitTool_commitPathsAreScoped(t *testing.T) {
	tests := []struct {
		name string
		args string
		want []string
	}{
		{
			name: "no paths commits the index",
			args: `{"action":"commit","message":"msg"}`,
			want: []string{"commit", "-m", "msg"},
		},
		{
			name: "one path is scoped after --",
			args: `{"action":"commit","message":"msg","paths":["a.go"]}`,
			want: []string{"commit", "-m", "msg", "--", "a.go"},
		},
		{
			name: "several paths keep their order",
			args: `{"action":"commit","message":"msg","paths":["a.go","b/c.go"]}`,
			want: []string{"commit", "-m", "msg", "--", "a.go", "b/c.go"},
		},
		{
			name: "dash-leading file stays behind --",
			args: `{"action":"commit","message":"msg","paths":["./-weird.go"]}`,
			want: []string{"commit", "-m", "msg", "--", "./-weird.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			ws := &fakeExecWorkspace{respond: func(req ExecRequest) (ExecResult, error) {
				got = append([]string{req.Command}, req.Args...)
				return ExecResult{Stdout: "[main abc1234] msg\n"}, nil
			}}
			if _, err := (&GitTool{WS: ws}).Execute(context.Background(), tt.args); err != nil {
				t.Fatalf("Execute(%s) error: %v", tt.args, err)
			}
			want := append([]string{"git"}, tt.want...)
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("argv = %v, want %v", got, want)
			}
		})
	}
}

// TestGitTool_gitAvailabilityIsTheWorkspacesQuestion pins yaah-8nz. The tool used
// to pre-flight the binary with exec.LookPath against this process's PATH, so an
// isolated workspace was told "is git installed?" about the host — and, worse,
// told so *instead of* running git that the workspace could run. The workspace is
// the authority: git running there is success even when the host PATH is empty,
// and a workspace where git cannot start gets the install hint even when the
// host has git. A real git failure (exit 128) must keep its own message, so the
// classification cannot swallow an ordinary error.
func TestGitTool_gitAvailabilityIsTheWorkspacesQuestion(t *testing.T) {
	t.Run("isolated workspace runs git while the host PATH has none", func(t *testing.T) {
		// Emptying the host PATH is the whole point: the old pre-flight read it.
		t.Setenv("PATH", "")
		ws := &isolatedExecWorkspace{fakeExecWorkspace: &fakeExecWorkspace{
			respond: func(ExecRequest) (ExecResult, error) {
				return ExecResult{Stdout: "## main\n"}, nil
			},
		}}
		out, err := (&GitTool{WS: ws}).Execute(context.Background(), `{"action":"status"}`)
		if err != nil {
			t.Fatalf("Execute() error: %v — the tool must ask the workspace, not the host PATH", err)
		}
		if !strings.Contains(out, "## main") {
			t.Errorf("output = %q, want the workspace's git output", out)
		}
	})

	t.Run("workspace without git keeps the install hint", func(t *testing.T) {
		if !gitAvailable() {
			t.Skip("git not on PATH: this case needs the host to have git to be meaningful")
		}
		ws := &isolatedExecWorkspace{fakeExecWorkspace: &fakeExecWorkspace{
			respond: func(ExecRequest) (ExecResult, error) {
				return ExecResult{ExitCode: -1}, exec.ErrNotFound
			},
		}}
		_, err := (&GitTool{WS: ws}).Execute(context.Background(), `{"action":"status"}`)
		if err == nil {
			t.Fatal("expected an error when git cannot start in the workspace")
		}
		if !strings.Contains(err.Error(), "is git installed") {
			t.Errorf("error = %v, want the install hint", err)
		}
		if !strings.Contains(err.Error(), "did not start in this workspace") {
			t.Errorf("error = %v, want it to name the workspace as the place that failed", err)
		}
	})

	t.Run("real git failure keeps its own message", func(t *testing.T) {
		ws := &fakeExecWorkspace{respond: func(ExecRequest) (ExecResult, error) {
			return ExecResult{ExitCode: 128, Stdout: "fatal: not a git repository"}, errors.New("exit status 128")
		}}
		_, err := (&GitTool{WS: ws}).Execute(context.Background(), `{"action":"status"}`)
		if err == nil {
			t.Fatal("expected error for a failing git")
		}
		if strings.Contains(err.Error(), "did not start in this workspace") {
			t.Errorf("a real git failure must not be dressed up as a missing binary: %v", err)
		}
		if !strings.Contains(err.Error(), "fatal: not a git repository") {
			t.Errorf("error = %v, want git's own output preserved", err)
		}
	})
}
