package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_findsAgentsMd(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("# Project rules\nBe helpful."), 0o644)

	files := Load(tmp, tmp)
	if len(files) == 0 {
		t.Fatal("expected to find AGENTS.md")
	}
	if !strings.Contains(files[0], "Be helpful") {
		t.Errorf("content = %q", files[0])
	}
}

func TestLoad_findsClaudeMd(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("# Claude rules\nThink carefully."), 0o644)

	files := Load(tmp, tmp)
	if len(files) == 0 {
		t.Fatal("expected to find CLAUDE.md")
	}
}

func TestLoad_walksUpFromCwd(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "project", "src")
	os.MkdirAll(child, 0o755)
	os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("parent rules"), 0o644)

	files := Load(child, parent)
	if len(files) == 0 {
		t.Fatal("expected to find AGENTS.md in parent")
	}
	if !strings.Contains(files[0], "parent rules") {
		t.Errorf("content = %q", files[0])
	}
}

func TestLoad_stopsAtWorktree(t *testing.T) {
	// Create two levels: parent and child worktree
	parent := t.TempDir()
	child := filepath.Join(parent, "worktree")
	os.MkdirAll(child, 0o755)
	os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("parent rules"), 0o644)

	// worktree is set to child — should NOT reach parent
	files := Load(child, child)
	if len(files) != 0 {
		t.Errorf("expected 0 files (parent outside worktree), got %d", len(files))
	}
}

func TestLoad_returnsEmptyWhenNothingFound(t *testing.T) {
	tmp := t.TempDir()
	files := Load(tmp, tmp)
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestLoad_prioritizesAgentsMdOverClaudeMd(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("agents rules"), 0o644)
	os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("claude rules"), 0o644)

	files := Load(tmp, tmp)
	if len(files) == 0 {
		t.Fatal("expected at least 1 file")
	}
	// Both should be found, but AGENTS.md first
	if !strings.Contains(files[0], "agents rules") {
		t.Errorf("expected AGENTS.md first, got %q", files[0])
	}
}
