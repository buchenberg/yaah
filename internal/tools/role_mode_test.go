package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/rolefile"
)

// role_mode_test.go pins yaah-21uk.13: the role tool exposes the mode
// capability as a first-class create/edit argument, validates it against the
// primary|subagent|all vocabulary (a tool call fails; the registry's
// clamp-and-warn is for reading arbitrary files), and list annotates only
// non-default capabilities so delegate-only registries render
// byte-identically.

// fakeRoleResolver is a minimal RoleResolver for list-output tests; the real
// implementation lives in agent/runner and needs a populated registry.
type fakeRoleResolver struct {
	entries []RoleEntry
}

func (f fakeRoleResolver) RoleNames() []string {
	names := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		names = append(names, e.Name)
	}
	return names
}

func (f fakeRoleResolver) ListRoles() []RoleEntry { return f.entries }

func (f fakeRoleResolver) ReloadRoles(map[string][]byte, []string) error { return nil }

func TestRoleTool_createWritesMode(t *testing.T) {
	dir := t.TempDir()
	tool := &RoleTool{Dirs: []string{dir}}

	out, err := tool.Execute(context.Background(),
		`{"action":"create","name":"loop-runner","description":"runs the loop","body":"Run the loop.","mode":"primary"}`)
	if err != nil {
		t.Fatalf("create failed: %v (output: %s)", err, out)
	}

	data, err := os.ReadFile(filepath.Join(dir, "loop-runner.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Position: mode lands with the other create-written keys, in Marshal's
	// field order (after tools, before the budget block).
	toolsIdx := strings.Index(content, "\ntools:")
	modeIdx := strings.Index(content, "\nmode:")
	budgetIdx := strings.Index(content, "\nmax_iterations:")
	if toolsIdx < 0 || modeIdx < 0 || budgetIdx < 0 || modeIdx < toolsIdx || modeIdx > budgetIdx {
		t.Errorf("mode key out of position:\n%s", content)
	}

	fm, _, err := rolefile.Parse(content)
	if err != nil {
		t.Fatalf("created file does not parse: %v", err)
	}
	if fm.Mode != "primary" {
		t.Errorf("mode = %q; want primary", fm.Mode)
	}
}

func TestRoleTool_createWithoutModeOmitsKey(t *testing.T) {
	dir := t.TempDir()
	tool := &RoleTool{Dirs: []string{dir}}

	out, err := tool.Execute(context.Background(),
		`{"action":"create","name":"plain","description":"plain role","body":"Do work."}`)
	if err != nil {
		t.Fatalf("create failed: %v (output: %s)", err, out)
	}

	data, err := os.ReadFile(filepath.Join(dir, "plain.md"))
	if err != nil {
		t.Fatal(err)
	}
	// "\nmode:" cannot match json_mode (no newline before it), so this pins
	// the key's absence, not just a non-primary value.
	if strings.Contains(string(data), "\nmode:") {
		t.Errorf("created role without mode should not contain the key:\n%s", data)
	}
	fm, _, err := rolefile.Parse(string(data))
	if err != nil {
		t.Fatalf("created file does not parse: %v", err)
	}
	if fm.Mode != "" {
		t.Errorf("mode = %q; want empty (all default at parse time, parity W3)", fm.Mode)
	}
}

func TestRoleTool_rejectsUnknownMode(t *testing.T) {
	dir := t.TempDir()
	tool := &RoleTool{Dirs: []string{dir}}

	t.Run("create", func(t *testing.T) {
		_, err := tool.Execute(context.Background(),
			`{"action":"create","name":"bad","description":"x","body":"y","mode":"orchestrator"}`)
		if err == nil {
			t.Fatal("create with an unknown mode should fail")
		}
		for _, want := range []string{"orchestrator", "primary", "subagent", "all"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
		if _, statErr := os.Stat(filepath.Join(dir, "bad.md")); !os.IsNotExist(statErr) {
			t.Errorf("rejected create must not write a role file (stat: %v)", statErr)
		}
	})

	t.Run("edit", func(t *testing.T) {
		path := filepath.Join(dir, "moded.md")
		src := "---\nname: moded\ndescription: carries a mode\nmode: all\n---\n\nBuild things.\n"
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.Execute(context.Background(),
			`{"action":"edit","name":"moded","mode":"boss"}`); err == nil {
			t.Fatal("edit with an unknown mode should fail")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("rejected edit rewrote the file:\n%s", after)
		}
	})
}

func TestRoleTool_editSetsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "switchable.md")
	src := "---\nname: switchable\ndescription: starts delegate-only\n---\n\nDo work.\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &RoleTool{Dirs: []string{dir}}
	// Mode-only edit: the "nothing to edit" guard must count mode as a field.
	out, err := tool.Execute(context.Background(),
		`{"action":"edit","name":"switchable","mode":"primary"}`)
	if err != nil {
		t.Fatalf("edit failed: %v (output: %s)", err, out)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "mode: primary") {
		t.Errorf("edited file lost mode: primary\n%s", content)
	}
	fm, _, err := rolefile.Parse(content)
	if err != nil {
		t.Fatalf("edited file does not parse: %v", err)
	}
	if fm.Mode != "primary" {
		t.Errorf("mode = %q; want primary", fm.Mode)
	}
	if fm.Description != "starts delegate-only" {
		t.Errorf("mode-only edit disturbed description: %q", fm.Description)
	}
}

func TestRoleTool_editExplicitSubagentKeepsKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demoted.md")
	src := "---\nname: demoted\ndescription: was primary\nmode: all\n---\n\nDo work.\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &RoleTool{Dirs: []string{dir}}
	out, err := tool.Execute(context.Background(),
		`{"action":"edit","name":"demoted","mode":"subagent"}`)
	if err != nil {
		t.Fatalf("edit failed: %v (output: %s)", err, out)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// There is no unset via the tool: an explicit "subagent" restores the
	// default capability but keeps the key in the file.
	if !strings.Contains(string(data), "mode: subagent") {
		t.Errorf("explicit subagent edit should keep the key:\n%s", data)
	}
	fm, _, err := rolefile.Parse(string(data))
	if err != nil {
		t.Fatalf("edited file does not parse: %v", err)
	}
	if fm.Mode != "subagent" {
		t.Errorf("mode = %q; want subagent", fm.Mode)
	}
}

func TestRoleTool_listAnnotatesNonDefaultModes(t *testing.T) {
	cases := []struct {
		label string
		mode  string
		want  string
	}{
		// The default flipped to "all" (parity W3): all — and an unset
		// mode — render unannotated; primary and subagent are annotated.
		{"all renders unannotated", "all", "- **worker**: does work"},
		{"empty renders unannotated", "", "- **worker**: does work"},
		{"primary is annotated", "primary", "- **worker** [primary]: does work"},
		{"subagent is annotated", "subagent", "- **worker** [subagent]: does work"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			tool := &RoleTool{Resolver: fakeRoleResolver{entries: []RoleEntry{
				{Name: "worker", Description: "does work", Mode: tc.mode},
			}}}
			out, err := tool.Execute(context.Background(), `{"action":"list"}`)
			if err != nil {
				t.Fatalf("list failed: %v", err)
			}
			// Exact match: the default renderings are byte-identical to the
			// pre-yaah-21uk.13 output.
			if out != tc.want {
				t.Errorf("list =\n%s\nwant:\n%s", out, tc.want)
			}
		})
	}
}
