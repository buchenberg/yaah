package yaah

// toolbelt_wiring_test.go pins the lazy toolbelt's session wiring
// (yaah-6mi9.5): the loader registers under the curated default and
// an explicit belt that names it, an explicit belt that omits it keeps
// the session exactly as configured, and the mount-time admission
// answers name their origins — persona for a persona allowlist the
// mount would widen, mode_floor for the read-only inspection set,
// with file_ops as the reachable wholly-withheld family.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// toolbeltWiring builds the default registry stage and registers the
// lazy toolbelt on it, the way buildMCPSkills sequences them.
func toolbeltWiring(t *testing.T, readOnly bool, personaTools []string) (*toolRegistryStage, *sessionConfig) {
	t.Helper()
	tr, err := buildToolRegistry(&config.Config{}, SessionOptions{}, &workspaceStage{cwd: t.TempDir()}, subagent.Persona{}, nil)
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	tr.personaTools = personaTools
	lc := &sessionConfig{readOnly: readOnly}
	registerLazyToolbelt(lc, tr)
	return tr, lc
}

func TestRegisterLazyToolbeltDefaultSession(t *testing.T) {
	tr, _ := toolbeltWiring(t, false, nil)
	if tr.reg.Get("toolbelt") == nil {
		t.Fatal("toolbelt not registered under the curated default")
	}
	if !tr.belt.Allows("toolbelt") {
		t.Error("curated default does not admit the toolbelt loader")
	}
	if ok, origin := tr.toolbeltTool.Admits("diff"); !ok || origin != "" {
		t.Errorf("Admits(diff) = %v/%q, want true — a plain session mounts the folded families", ok, origin)
	}
	if ok, origin := tr.toolbeltTool.Admits("mkdir"); !ok || origin != "" {
		t.Errorf("Admits(mkdir) = %v/%q, want true — the file_ops restore path is open", ok, origin)
	}
}

func TestRegisterLazyToolbeltExplicitBeltOmitsLoader(t *testing.T) {
	// An explicit agents.default.tools list without "toolbelt": the
	// belt refuses the loader's registration, the session keeps its
	// configured shape, and no mount surface exists.
	tr, err := buildToolRegistry(&config.Config{Agent: config.AgentConfig{
		Default: config.Defaults{Tools: []string{"read", "bash", "edit"}},
	}}, SessionOptions{}, &workspaceStage{cwd: t.TempDir()}, subagent.Persona{}, nil)
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	lc := &sessionConfig{}
	registerLazyToolbelt(lc, tr)
	if tr.reg.Get("toolbelt") != nil {
		t.Error("loader registered under an explicit belt that omits it — config owns admission")
	}
}

func TestRegisterLazyToolbeltPersonaOrigin(t *testing.T) {
	// A persona allowlist cannot be widened by a mount (the design's
	// matrix): every name outside it answers persona.
	tr, _ := toolbeltWiring(t, false, []string{"bash", "read", "write"})
	if ok, origin := tr.toolbeltTool.Admits("diff"); ok || origin != "persona" {
		t.Errorf("Admits(diff) = %v/%q, want false/persona", ok, origin)
	}
	if ok, origin := tr.toolbeltTool.Admits("read"); !ok || origin != "" {
		t.Errorf("Admits(read) = %v/%q, want true — the persona names it", ok, origin)
	}
}

func TestRegisterLazyToolbeltModeFloorOrigin(t *testing.T) {
	// The read-only session answers mode_floor for members outside the
	// inspection set; the inspection-set members of analysis still
	// mount. And file_ops is the reachable atomic case: no member of
	// the cluster is in the inspection set, so the mount fails citing
	// mode_floor and registers nothing.
	tr, _ := toolbeltWiring(t, true, nil)
	if ok, origin := tr.toolbeltTool.Admits("mkdir"); ok || origin != "mode_floor" {
		t.Errorf("Admits(mkdir) = %v/%q, want false/mode_floor", ok, origin)
	}
	if ok, origin := tr.toolbeltTool.Admits("diff"); !ok || origin != "" {
		t.Errorf("Admits(diff) = %v/%q, want true — diff is in the inspection set", ok, origin)
	}
	_, err := tr.toolbeltTool.Execute(context.Background(), `{"action":"mount","family":"file_ops"}`)
	if err == nil {
		t.Fatal("read-only mount of file_ops succeeded — wholly withheld families must fail")
	}
	if !strings.Contains(err.Error(), "mode_floor") {
		t.Errorf("refusal = %q, want mode_floor named as the origin", err)
	}
	for name := range map[string]bool{"copy": true, "mkdir": true, "tail": true} {
		if tr.reg.Get(name) != nil {
			t.Errorf("%s registered by a wholly refused mount", name)
		}
	}
	// The folded names stay where the curation put them: refused
	// (therefore known), mountable, invisible.
	refused := tr.belt.Refused()
	for _, name := range foldedFromOrchestratorDefault {
		if !slices.Contains(refused, name) && tr.reg.Get(name) == nil {
			t.Errorf("%s: neither refused nor registered after the failed mount — the belt state drifted", name)
		}
	}
}

// TestToolbeltStubBlockInGuidance pins the prompt side (yaah-6mi9.5):
// the stub block rides the guidance when the loader registered —
// every family, full membership, the mount instruction — and never
// when an explicit belt omitted it.
func TestToolbeltStubBlockInGuidance(t *testing.T) {
	tr, _ := toolbeltWiring(t, false, nil)
	ws := &workspaceStage{cwd: t.TempDir()}
	mcps := &mcpSkillsStage{}
	loop := &loopInfraStage{taskTool: &tools.DelegateTool{}}
	prompt := appendToolGuidance(SessionOptions{}, ws, tr, mcps, loop, []string{"append"})
	for _, want := range []string{
		"## Mountable tool families",
		"file_ops", "analysis", "net",
		"file_ops: one-shot file moves/copies/listing (append, bulk_files, checksum, copy, copy_tree, find, mkdir, move, move_tree, rmdir, tail)",
		"analysis: query and compare workspace content (calculate, json_query, file_info, diff, bisect)",
		"net: direct HTTP requests and web page fetches (http, webfetch)",
		"action: mount",
		"## Tool belt",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("guidance missing %q", want)
		}
	}
	// No loader, no stubs: the guidance must never advertise mounts the
	// session cannot make.
	tr2, err := buildToolRegistry(&config.Config{Agent: config.AgentConfig{
		Default: config.Defaults{Tools: []string{"read", "bash"}},
	}}, SessionOptions{}, &workspaceStage{cwd: t.TempDir()}, subagent.Persona{}, nil)
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	registerLazyToolbelt(&sessionConfig{}, tr2)
	prompt2 := appendToolGuidance(SessionOptions{}, ws, tr2, mcps, loop, nil)
	if strings.Contains(prompt2, "Mountable tool families") {
		t.Error("guidance advertises mountable families without the loader registered")
	}
}
