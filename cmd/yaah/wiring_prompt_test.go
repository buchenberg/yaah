package yaah

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if strings.Contains(got, strings.TrimSpace(prompts.IdentityPrompt)) {
		t.Error("sub-agent base must not embed the main identity prompt")
	}
	for _, banned := range []string{
		"spawn_subagent",
		"list_subagents",
		"Memory Guidelines",
		"memory_add",
		"sub-agent concurrency limit",
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
