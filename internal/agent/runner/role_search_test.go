package runner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestRoleSearchPaths_UserLevelRolesAtRealHome pins the review fix: user-level
// roles live at ~/.agents/roles. config.HomeDir() is ~/.yaah, so the old code
// searched ~/.yaah/.agents/roles and reached the real home only as a side
// effect of walking up from cwd — which fails whenever cwd is outside $HOME
// (a worktree, a container, /tmp).
func TestRoleSearchPaths_UserLevelRolesAtRealHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("YAAH_HOME", "")     // config.HomeDir falls back to $HOME/.yaah

	outside := t.TempDir()
	dirs := RoleSearchPaths(outside)

	wantProject := filepath.Join(outside, ".agents", "roles")
	if len(dirs) == 0 || dirs[0] != wantProject {
		t.Fatalf("first search dir = %q, want the project one %q", dirs[0], wantProject)
	}
	for _, want := range []string{
		filepath.Join(home, ".agents", "roles"),
		filepath.Join(home, ".yaah", ".agents", "roles"), // legacy location, kept for existing installs
	} {
		if !slices.Contains(dirs, want) {
			t.Errorf("RoleSearchPaths(%q) = %v, missing %q", outside, dirs, want)
		}
	}

	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		if seen[d] {
			t.Errorf("duplicate search dir %q in %v", d, dirs)
		}
		seen[d] = true
	}

	// An empty cwd must not panic or emit a bare relative dir twice.
	for _, d := range RoleSearchPaths("") {
		if d == "" {
			t.Errorf("empty search dir in %v", RoleSearchPaths(""))
		}
	}
}

// TestRoleSearchPaths_HomeInsideWalkDeduplicated: when cwd is under home, the
// walk already yields ~/.agents/roles; the explicit append must not repeat it.
func TestRoleSearchPaths_HomeInsideWalkDeduplicated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("YAAH_HOME", "")

	project := filepath.Join(home, "code", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := RoleSearchPaths(project)
	want := filepath.Join(home, ".agents", "roles")
	count := 0
	for _, d := range dirs {
		if d == want {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%q appears %d times in %v, want once", want, count, dirs)
	}
}
