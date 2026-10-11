package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apply_out_test.go pins the accept seam end to end against the in-memory
// backend: a bootstrapped session, mutations in the container, then the
// dry-run preview and the real apply onto the host — plus the approval
// gate that makes the apply a human decision.

func newApplyOutSession(t *testing.T) (*SessionSandbox, string) {
	t.Helper()
	src := gitHostRepo(t)
	open, _ := cleanOpener()
	ss, err := StartSessionSandbox(context.Background(), sessionTestConfig(), open, src, nil)
	if err != nil {
		t.Fatalf("StartSessionSandbox: %v", err)
	}
	t.Cleanup(func() { _ = ss.Destroy(context.Background()) })
	return ss, src
}

func TestApplyOutTool_DryRunPreviewsWithoutTouchingHost(t *testing.T) {
	ss, src := newApplyOutSession(t)

	ws := ss.Workspace()
	readme, err := ws.ResolvePath("readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile(context.Background(), readme, []byte("via the tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}
	out, err := tool.Execute(context.Background(), `{"dry_run": true}`)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "Dry run") || !strings.Contains(out, "readme.txt") {
		t.Errorf("dry-run output must name the preview and the changed file, got:\n%s", out)
	}
	if got, err := os.ReadFile(filepath.Join(src, "readme.txt")); err != nil || string(got) != "hello\n" {
		t.Errorf("host readme.txt = %q, %v — a dry run must not touch the host", got, err)
	}
	if tool.IsDangerous(`{"dry_run": true}`) {
		t.Error("a dry run must not be dangerous")
	}
}

func TestApplyOutTool_AppliesOntoHost(t *testing.T) {
	ss, src := newApplyOutSession(t)

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("via the tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}
	// The real apply requires a preceding dry run: the approval prompt
	// shows only the arguments, so the user must have seen the diff.
	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out, "readme.txt") {
		t.Errorf("apply output must list the landed file, got:\n%s", out)
	}
	if got, err := os.ReadFile(filepath.Join(src, "readme.txt")); err != nil || string(got) != "via the tool\n" {
		t.Errorf("host readme.txt = %q, %v — the accepted change did not land", got, err)
	}
}

// TestApplyOutTool_RefusesUnpreviewedApply pins the honest-approval
// invariant: the approval prompt cannot show the delta, so an apply with
// no dry run in the session is refused rather than let through blind.
func TestApplyOutTool_RefusesUnpreviewedApply(t *testing.T) {
	ss, src := newApplyOutSession(t)

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("unpreviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("an apply without a preceding dry run must be refused")
	}
	if got, err := os.ReadFile(filepath.Join(src, "readme.txt")); err != nil || string(got) != "hello\n" {
		t.Errorf("host readme.txt = %q, %v — the refused apply must not touch the host", got, err)
	}
}

// TestApplyOutTool_RefusesStalePreview pins the preview/apply race: a
// mutation landing between the dry run and the apply changes the delta,
// and the apply is refused rather than landing unpreviewed content.
func TestApplyOutTool_RefusesStalePreview(t *testing.T) {
	ss, src := newApplyOutSession(t)

	ws := ss.Workspace()
	readme, _ := ws.ResolvePath("readme.txt")
	notes, _ := ws.ResolvePath("notes.txt")
	if err := ws.WriteFile(context.Background(), readme, []byte("previewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}
	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	// A concurrent mutation after the preview.
	if err := ws.WriteFile(context.Background(), notes, []byte("raced in\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("an apply whose preview went stale must be refused")
	}
	if got, err := os.ReadFile(filepath.Join(src, "readme.txt")); err != nil || string(got) != "hello\n" {
		t.Errorf("host readme.txt = %q, %v — the refused apply must not touch the host", got, err)
	}
	// A fresh dry run re-arms the apply: the user is shown the updated
	// diff before approving it.
	if _, err := tool.Execute(context.Background(), `{"dry_run": true}`); err != nil {
		t.Fatalf("re-preview: %v", err)
	}
	if _, err := tool.Execute(context.Background(), `{}`); err != nil {
		t.Fatalf("apply after re-preview: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(src, "readme.txt")); err != nil || string(got) != "previewed\n" {
		t.Errorf("host readme.txt = %q, %v — the re-previewed apply did not land", got, err)
	}
}

func TestApplyOutTool_NoChangesIsANoOp(t *testing.T) {
	ss, src := newApplyOutSession(t)

	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("apply with no changes: %v", err)
	}
	if !strings.Contains(out, "No changes") {
		t.Errorf("output = %q, want the no-changes message", out)
	}
	if got, err := os.ReadFile(filepath.Join(src, "readme.txt")); err != nil || string(got) != "hello\n" {
		t.Errorf("host readme.txt = %q, %v — a no-op apply must not touch the host", got, err)
	}
}

// TestApplyOutTool_IsDangerous pins the approval gate: the real apply is
// dangerous, a dry run is not, and anything unparseable fails closed.
func TestApplyOutTool_IsDangerous(t *testing.T) {
	ss, src := newApplyOutSession(t)
	tool := &ApplyOutTool{Sandbox: ss, HostDir: src}

	cases := []struct {
		name string
		args string
		want bool
	}{
		{"empty arguments mean apply", "", true},
		{"a plain apply", `{}`, true},
		{"an explicit apply", `{"dry_run": false}`, true},
		{"a dry run", `{"dry_run": true}`, false},
		{"garbage fails closed", `{not json`, true},
	}
	for _, tc := range cases {
		if got := tool.IsDangerous(tc.args); got != tc.want {
			t.Errorf("IsDangerous(%q) (%s) = %v, want %v", tc.args, tc.name, got, tc.want)
		}
	}
}
