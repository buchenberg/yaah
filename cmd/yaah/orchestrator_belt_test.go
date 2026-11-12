package yaah

// orchestrator_belt_test.go pins yaah-6mi9.1: the empty-config default is
// the curated orchestrator belt, an explicit list wins verbatim, "*" or
// "all" restores the full surface, and the curated boundary is pinned at
// the registry — under default config the belt refuses exactly what the
// declaration says it withholds, so an undeclared drop (a new leaf tool
// nobody curated, a gated lane the belt second-guesses) fails here
// instead of escaping silently.

import (
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
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

// TestDefaultOrchestratorBeltDeclaration keeps the declaration honest on
// its own terms: the cores the bench evidence showed in use are present,
// the gated feature lanes are admitted (their own registration gates
// decide availability), the withheld families are absent from the list,
// and the withheld list is itself pinned to the bead's names. The
// registry-level test below pins what the declaration cannot check on
// its own: what the belt actually refuses.
func TestDefaultOrchestratorBeltDeclaration(t *testing.T) {
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
		// The gated lanes — belt-admitted, registration-gated:
		"proposal", "apply_out",
		"memory_search", "memory_search_sessions",
		"memory_add", "memory_update", "memory_delete", "memory_promote",
		"go_test", "go_mod", "go_outline", "go_refactor", "staticcheck",
		// The wake pair — gated to interactive sessions by
		// registerWakeTools (yaah-6mi9.2):
		"wait_for", "watch",
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

// TestOrchestratorBeltRegistryBoundary pins the curation where it can
// actually drift: through the real registry under default config, the
// belt's refusal set must equal the declared withholdings plus the
// off-platform shell (the platform rule's own refusal) — no more, no
// less. A new leaf tool nobody curated shows up here as an undeclared
// refusal; so would a gated lane the belt refused instead of deferring
// to its own gate. The wake pair is of the second kind (yaah-6mi9.2):
// it registers in interactive sessions and is omitted by the gate in
// one-shot ones, in neither case refused by the belt.
func TestOrchestratorBeltRegistryBoundary(t *testing.T) {
	// Interactive shape: the wake pair registers; nothing else that the
	// belt admits is missing from the registry for shape reasons except
	// question (this construction is not one-shot, so question lives).
	st, err := buildToolRegistry(&config.Config{}, SessionOptions{}, &workspaceStage{cwd: t.TempDir()}, subagent.Persona{}, nil)
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	registerWakeTools(&loopInfraStage{}, st, false)

	// Refused: the declared withholdings, plus the shell the platform
	// rule refuses on this OS.
	offPlatform := "powershell"
	if runtime.GOOS == "windows" {
		offPlatform = "bash"
	}
	want := slices.Clone(removedFromOrchestratorDefault)
	want = append(want, offPlatform)
	slices.Sort(want)
	if got := st.belt.Refused(); !reflect.DeepEqual(got, want) {
		t.Errorf("default-config interactive belt.Refused() = %v\nwant exactly %v\n(every difference is an undeclared drop or an unrecorded withholding)", got, want)
	}

	// Nothing withheld is held by the registry.
	for _, held := range removedFromOrchestratorDefault {
		if st.reg.Get(held) != nil {
			t.Errorf("registry holds withheld tool %q", held)
		}
	}

	// The wake pair is belt-admitted and registered in the interactive
	// shape — the gate, not the belt, owns its availability.
	for _, wake := range []string{"wait_for", "watch"} {
		if !st.belt.Allows(wake) {
			t.Errorf("default belt refuses wake tool %q; session shape decides availability", wake)
		}
		if st.reg.Get(wake) == nil {
			t.Errorf("interactive session does not hold wake tool %q", wake)
		}
	}

	// One-shot shape: the gate omits the pair instead of the belt
	// refusing it — same refusal set, different omissions.
	sto, err := buildToolRegistry(&config.Config{}, SessionOptions{OneShot: true}, &workspaceStage{cwd: t.TempDir()}, subagent.Persona{}, nil)
	if err != nil {
		t.Fatalf("buildToolRegistry (one-shot): %v", err)
	}
	registerWakeTools(&loopInfraStage{}, sto, true)
	if got := sto.belt.Refused(); !reflect.DeepEqual(got, want) {
		t.Errorf("default-config one-shot belt.Refused() = %v, want %v (the gate must omit, not refuse)", got, want)
	}
	for _, wake := range []string{"wait_for", "watch"} {
		if sto.reg.Get(wake) != nil {
			t.Errorf("one-shot session holds wake tool %q; the gate must omit it", wake)
		}
		if !sto.belt.Knows(wake) {
			t.Errorf("one-shot gate must name %q on the belt so a persona naming it is not a typo", wake)
		}
	}
	if !slices.Contains(sto.omittedTools, "wait_for") || !slices.Contains(sto.omittedTools, "watch") {
		t.Errorf("one-shot gate must record the wake pair in omittedTools so the prompt can say couldn't, not didn't; got %v", sto.omittedTools)
	}

	// The gated lanes are belt-admitted: their own registration gates —
	// not the curation — decide whether the session gets them. In this
	// default-config, no-store, non-module, non-isolated construction
	// exactly those gates are closed, so none of the lane tools is
	// held here either; admitting them is what keeps the gates honest.
	for _, lane := range []string{
		"proposal", "apply_out",
		"memory_search", "memory_add", "memory_promote",
		"go_test", "staticcheck",
	} {
		if !st.belt.Allows(lane) {
			t.Errorf("default belt refuses gated lane tool %q; the lane's own gate decides availability", lane)
		}
		if st.reg.Get(lane) != nil {
			t.Errorf("gate leaked: registry holds %q under default-config non-lane conditions", lane)
		}
	}

	// A core tool the bench evidence showed in use survives the whole
	// construction, on the platform that admits it.
	if runtime.GOOS != "windows" && st.reg.Get("bash") == nil {
		t.Error("registry does not hold bash on a non-Windows platform")
	}
}

// TestOrchestratorBeltPassesThroughTheLoopSeam documents the wiring
// shape since the mid-session persona switch (yaah-21uk.12): the belt is
// config-only, and buildToolRegistry hands effectiveBeltNames a nil
// persona so the resolved list flows through verbatim. The persona
// narrows the advertised set per loop build instead
// (narrowToPersonaTools, also narrow-only), so this pins that the
// resolved default reaches the belt exactly as curated.
func TestOrchestratorBeltPassesThroughTheLoopSeam(t *testing.T) {
	resolved := orchestratorBeltNames(nil)
	if got := effectiveBeltNames(resolved, nil); !reflect.DeepEqual(got, resolved) {
		t.Errorf("nil persona must not alter the resolved default: got %v", got)
	}
	// And the helper's own intersection semantics (used when a caller
	// passes both) remain narrow-only against the curated default: a
	// persona naming a withheld tool gets nothing of it — the tool is
	// not in the resolved default, so the intersection drops it rather
	// than adding it.
	if got := effectiveBeltNames(resolved, []string{"copy"}); len(got) != 0 {
		t.Errorf("narrow-only intersection over the curated default: got %v, want empty", got)
	}
}
