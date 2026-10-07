package yaah

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
)

func TestBuildSubAgentBasePrompt_LeanIdentity(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("Project rule: prefer table-driven tests."), 0o644); err != nil {
		t.Fatal(err)
	}

	got := buildSubAgentBasePrompt(cwd, nil)

	if !strings.Contains(got, "You are a yaah sub-agent") {
		t.Errorf("missing sub-agent identity:\n%s", got)
	}
	// These live in the orchestrator identity; the sub-agent base must never
	// embed it, whatever the builder wiring looks like.
	for _, banned := range []string{
		prompts.IdentityPrompt,
		"spawn_subagent",
		"list_subagents",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("sub-agent base must not contain %q:\n%s", banned, got)
		}
	}
	if !strings.Contains(got, "Project rule: prefer table-driven tests.") {
		t.Errorf("sub-agent base must carry project AGENTS.md content:\n%s", got)
	}
}

func TestBuildSubAgentBasePrompt_CarriesUserContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("User rule: no emoji in responses."), 0o644); err != nil {
		t.Fatal(err)
	}

	got := buildSubAgentBasePrompt(t.TempDir(), nil)

	if !strings.Contains(got, "User rule: no emoji in responses.") {
		t.Errorf("sub-agent base must carry user AGENTS.md content:\n%s", got)
	}
}

func TestBuildSubAgentBasePrompt_MemoryFactsWithoutToolGuidelines(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	db, err := memory.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.AddMemory(memory.Entry{
		Text: "the widget config lives in configs/widget.yaml",
		Tags: `["project:probe"]`,
	}); err != nil {
		t.Fatal(err)
	}

	got := buildSubAgentBasePrompt(t.TempDir(), db)

	if !strings.Contains(got, "the widget config lives in configs/widget.yaml") {
		t.Errorf("sub-agent base must carry stored memory facts:\n%s", got)
	}
	// The main-session builder appends memory-tool guidelines whenever db is
	// non-nil; the sub-agent base must carry the facts but not the guidelines,
	// since no sub-agent role has memory tools.
	for _, banned := range []string{"Memory Guidelines", "memory_add", "memory_search"} {
		if strings.Contains(got, banned) {
			t.Errorf("sub-agent base must not contain memory-tool guidance %q:\n%s", banned, got)
		}
	}
}
