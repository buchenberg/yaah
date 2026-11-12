package yaah

// orchestrator_belt_test.go pins yaah-6mi9.1: the empty-config default is
// the curated orchestrator belt, an explicit list wins verbatim, "*"
// restores the full surface, and the curated boundary (what is withheld
// and why) cannot drift silently.

import (
	"reflect"
	"slices"
	"testing"
)

func TestOrchestratorBeltNamesDefault(t *testing.T) {
	got := orchestratorBeltNames(nil)
	if !reflect.DeepEqual(got, defaultOrchestratorBelt) {
		t.Errorf("empty config: orchestratorBeltNames(nil) = %v, want the curated default", got)
	}
	// Blank entries read as empty: the same curated default.
	if got := orchestratorBeltNames([]string{" ", ""}); !reflect.DeepEqual(got, defaultOrchestratorBelt) {
		t.Errorf("blank-only config: got %v, want the curated default", got)
	}
}

func TestOrchestratorBeltNamesExplicitWins(t *testing.T) {
	got := orchestratorBeltNames([]string{" read ", "write", ""})
	want := []string{"read", "write"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("explicit config: got %v, want %v (normalized, verbatim)", got, want)
	}
}

func TestOrchestratorBeltNamesWildcardRestoresFullSurface(t *testing.T) {
	for _, sentinel := range []string{"*", "all"} {
		if got := orchestratorBeltNames([]string{sentinel}); got != nil {
			t.Errorf("config %q: got %v, want nil (NewBelt reads nil as allow-all)", sentinel, got)
		}
	}
	// A wildcard alongside other names is a belt entry, not a sentinel.
	if got := orchestratorBeltNames([]string{"*", "read"}); got == nil || len(got) != 2 {
		t.Errorf("config [* read]: got %v, want the two names verbatim", got)
	}
}

// TestDefaultOrchestratorBeltBoundary pins the curation: the cores the
// bench evidence showed in use are present, the withheld families are
// absent, and the boundary matches the bead yaah-6mi9.1 description.
// Adding a tool to the default belt is an acknowledged ~780-token/call
// floor cost — update the bead evidence, not just the list.
func TestDefaultOrchestratorBeltBoundary(t *testing.T) {
	for _, core := range []string{
		// Every tool the 2026-11-12 run actually called:
		"bash", "write", "read", "ls",
		// The dispatch and supervision surface:
		"delegate", "list_subagents", "subagent_jobs", "supervisor",
		// Belts the 2026-11-02 comparable run carried:
		"background_process", "bisect", "calculate", "delete", "diff",
		"edit", "file_info", "git", "glob", "grep", "http",
		"json_query", "patch", "plan", "question", "replace", "role",
		"skill", "todowrite", "webfetch",
		// The platform shell pair (platform rule resolves one):
		"powershell",
	} {
		if !slices.Contains(defaultOrchestratorBelt, core) {
			t.Errorf("curated default missing core tool %q", core)
		}
	}
	for _, held := range removedFromOrchestratorDefault {
		if slices.Contains(defaultOrchestratorBelt, held) {
			t.Errorf("curated default still names withheld tool %q", held)
		}
	}
	// The withheld list is itself pinned: a name moving from withheld to
	// default (or the tool being deleted outright) must edit this file.
	wantHeld := []string{
		"append", "bulk_files", "checksum", "copy", "copy_tree",
		"find", "mkdir", "move", "move_tree", "rmdir", "tail",
		"wait_for", "watch",
	}
	if !reflect.DeepEqual(removedFromOrchestratorDefault, wantHeld) {
		t.Errorf("removedFromOrchestratorDefault = %v, want %v", removedFromOrchestratorDefault, wantHeld)
	}
	// No duplicates, no blanks in the default.
	seen := make(map[string]bool, len(defaultOrchestratorBelt))
	for _, n := range defaultOrchestratorBelt {
		if n == "" {
			t.Fatal("curated default contains a blank name")
		}
		if seen[n] {
			t.Errorf("curated default names %q twice", n)
		}
		seen[n] = true
	}
}

// TestOrchestratorBeltComposesWithPersonaNarrowing exercises the real
// seam: the resolved default flows through effectiveBeltNames the same
// way an explicit config belt always has, so persona narrowing keeps
// intersecting against a non-empty default instead of allowing all.
func TestOrchestratorBeltComposesWithPersonaNarrowing(t *testing.T) {
	resolved := orchestratorBeltNames(nil)
	if got := effectiveBeltNames(resolved, []string{"read", "bash"}); !reflect.DeepEqual(got, []string{"bash", "read"}) {
		t.Errorf("persona narrowing over the default: got %v, want [bash read]", got)
	}
	// A persona naming a withheld tool narrows to nothing of it: the
	// tool is not in the resolved default, so the intersection drops it
	// rather than adding it — narrow-only by construction.
	got := effectiveBeltNames(resolved, []string{"copy"})
	if len(got) != 0 {
		t.Errorf("persona naming withheld tool: got %v, want empty (narrow-only intersection)", got)
	}
}
