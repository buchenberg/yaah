package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoleSearchPaths_UserLevelUsesRealHome pins the yaah-gqqo fix:
// user-level roles live at ~/.agents/roles (the real home dir), not
// under ~/.yaah/.agents/roles where config.HomeDir() pointed them.
// Mirrors cmd/yaah's TestPlanSearchPaths_UserLevelUsesRealHome.
func TestRoleSearchPaths_UserLevelUsesRealHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	cwd, _ := os.Getwd()
	dirs := RoleSearchPaths(cwd)
	if len(dirs) < 2 {
		t.Fatalf("RoleSearchPaths returned %d dirs; want >= 2", len(dirs))
	}

	t.Run("user-level dir is real home, not ~/.yaah", func(t *testing.T) {
		last := dirs[len(dirs)-1]
		if want := filepath.Join(home, ".agents", "roles"); last != want {
			t.Errorf("user-level roles dir = %q; want %q", last, want)
		}
		if strings.Contains(last, filepath.Join(".yaah", ".agents")) {
			t.Errorf("user-level roles dir must not live under ~/.yaah: %q", last)
		}
	})

	t.Run("project tier walks up from cwd unchanged", func(t *testing.T) {
		// Everything before the user-level dir must be exactly the
		// walk-up from cwd. (Those dirs may legitimately pass through a
		// directory named .yaah when tests run inside a yaah worktree,
		// so the no-.yaah assertion above is scoped to the user tier.)
		var want []string
		for dir := cwd; ; dir = filepath.Dir(dir) {
			want = append(want, filepath.Join(dir, ".agents", "roles"))
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
		project := dirs[:len(dirs)-1]
		if len(project) != len(want) {
			t.Fatalf("project tier has %d dirs; want %d (%v vs %v)", len(project), len(want), project, want)
		}
		for i := range want {
			if project[i] != want[i] {
				t.Errorf("project tier dir %d = %q; want %q", i, project[i], want[i])
			}
		}
		if len(project) > 0 && !strings.HasPrefix(project[0], cwd) {
			t.Errorf("first dir %q should be under cwd %q", project[0], cwd)
		}
	})
}
