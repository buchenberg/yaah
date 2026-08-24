package yaah

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/config"
)

// TestPlanSearchPaths_UserLevelUsesRealHome pins the review B5 fix:
// user-level plans live at ~/.agents/plans (the real home dir), not
// under ~/.yaah/.agents/plans where config.HomeDir() pointed them.
func TestPlanSearchPaths_UserLevelUsesRealHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	dirs := planSearchPaths()
	if len(dirs) < 2 {
		t.Fatalf("planSearchPaths returned %d dirs; want >= 2", len(dirs))
	}

	last := dirs[len(dirs)-1]
	if want := filepath.Join(home, ".agents", "plans"); last != want {
		t.Errorf("user-level plans dir = %q; want %q", last, want)
	}
	if strings.Contains(last, filepath.Join(".yaah", ".agents")) {
		t.Errorf("user-level plans dir must not live under ~/.yaah: %q", last)
	}

	// Project tiers walk up from cwd and all end in .agents/plans.
	cwd, _ := os.Getwd()
	if !strings.HasPrefix(dirs[0], cwd) {
		t.Errorf("first dir %q should be under cwd %q", dirs[0], cwd)
	}
	for _, d := range dirs[:len(dirs)-1] {
		if !strings.HasSuffix(d, filepath.Join(".agents", "plans")) {
			t.Errorf("project dir %q does not end in .agents/plans", d)
		}
	}
}

// TestSkillSearchPaths_Tiers pins the existing skills ordering:
// project dirs → ~/.yaah/skills → ~/.agents/skills.
func TestSkillSearchPaths_Tiers(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	dirs := skillSearchPaths()
	if len(dirs) < 2 {
		t.Fatalf("skillSearchPaths returned %d dirs; want >= 2", len(dirs))
	}

	if last := dirs[len(dirs)-1]; last != filepath.Join(home, ".agents", "skills") {
		t.Errorf("user-level skills dir = %q; want %q", last, filepath.Join(home, ".agents", "skills"))
	}
	if yaahTier := dirs[len(dirs)-2]; yaahTier != filepath.Join(config.HomeDir(), "skills") {
		t.Errorf("yaah skills dir = %q; want %q", yaahTier, filepath.Join(config.HomeDir(), "skills"))
	}
}
