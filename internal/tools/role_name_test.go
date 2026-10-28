package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoleTool_rejectsNamesThatEscapeRolesDir pins bead yaah-06u: the role tool
// derives its file path as <dir>/<name>.md, so a name carrying a path separator,
// "..", an absolute path or a volume name reaches filepath.Join unresolved and
// escapes the roles directory on create and on delete. Every action that takes a
// name must refuse such names before any path exists, and nothing outside the
// roles directory may be created or removed.
func TestRoleTool_rejectsNamesThatEscapeRolesDir(t *testing.T) {
	rejected := []struct {
		name     string
		roleName string
		wantErr  string
	}{
		{"parent traversal", "../escaped", "invalid role name"},
		{"nested traversal", "../../tmp/escaped", "invalid role name"},
		{"separator", "sub/role", "invalid role name"},
		{"windows separator", `..\escaped`, "invalid role name"},
		{"absolute path", "/tmp/yaah-role-escape", "invalid role name"},
		{"dot", ".", "invalid role name"},
		{"dotdot", "..", "invalid role name"},
		{"empty", "", "requires a 'name'"},
	}

	// Every naming action is exercised: create and delete are the two that
	// touch the disk, edit and show locate a file the same way, and a guard
	// that covers only the disk writers would still leave show reading any
	// path a name can spell.
	for _, tt := range rejected {
		for _, action := range []string{"create", "edit", "delete", "show"} {
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				root := t.TempDir()
				rolesDir := filepath.Join(root, "roles")

				// The file a traversal would land on. It must survive every
				// attempt byte-for-byte: "no file outside the roles directory
				// was touched" is only meaningful against a file that is
				// there to be touched.
				sentinel := filepath.Join(root, "escaped.md")
				if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
					t.Fatal(err)
				}

				tool := &RoleTool{Dirs: []string{rolesDir}}
				args := fmt.Sprintf(`{"action":%q,"name":%q,"description":"d","body":"b"}`, action, tt.roleName)
				out, err := tool.Execute(context.Background(), args)
				if err == nil {
					t.Fatalf("%s with name %q succeeded (%q); want a rejection", action, tt.roleName, out)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("%s with name %q error = %q; want substring %q", action, tt.roleName, err, tt.wantErr)
				}

				// The roles directory was not even created, and the sentinel
				// is untouched.
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Name() != "escaped.md" {
					names := make([]string, 0, len(entries))
					for _, e := range entries {
						names = append(names, e.Name())
					}
					t.Errorf("root entries after %s with name %q = %v; want only the sentinel", action, tt.roleName, names)
				}
				data, err := os.ReadFile(sentinel)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != "sentinel" {
					t.Errorf("sentinel outside the roles directory was modified: %q", data)
				}
			})
		}
	}
}

// TestRoleTool_acceptsOrdinaryNames is the positive control for the test above:
// the guard must reject paths, not ordinary role names, or role management
// breaks for every legitimate caller.
func TestRoleTool_acceptsOrdinaryNames(t *testing.T) {
	root := t.TempDir()
	rolesDir := filepath.Join(root, "roles")
	tool := &RoleTool{Dirs: []string{rolesDir}}

	for _, name := range []string{"tester", "release.manager", "docs-writer"} {
		out, err := tool.Execute(context.Background(),
			fmt.Sprintf(`{"action":"create","name":%q,"description":"d","body":"b"}`, name))
		if err != nil {
			t.Fatalf("create %q failed: %v (output: %s)", name, err, out)
		}
		if _, err := os.Stat(filepath.Join(rolesDir, name+".md")); err != nil {
			t.Errorf("create %q: %v", name, err)
		}
	}

	if _, err := tool.Execute(context.Background(), `{"action":"delete","name":"tester"}`); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rolesDir, "tester.md")); !os.IsNotExist(err) {
		t.Errorf("delete left tester.md behind (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(rolesDir, "release.manager.md")); err != nil {
		t.Errorf("delete removed a sibling role: %v", err)
	}
}
