package yaah

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/prompts"
)

// sceneTestRegistry loads a temp-dir registry shaped like the user's
// report: grump and goat-joke-teller role files WITHOUT a mode key (the
// parity W3 default makes them primary-capable personas), plus the
// built-ins.
func sceneTestRegistry(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"grump.md":            "---\nname: Harold\nspecialty: grump\ndescription: Is obstinate\ntools: []\n---\nGRUMP BODY MARKER\n",
		"goat-joke-teller.md": "---\nname: Griff\nspecialty: goat-joke-teller\ndescription: Tells goat jokes\ntools: []\n---\nGOAT BODY MARKER\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(nil) })
}

// TestDoubleSwitchStateRebinding is the double-switch STATE guard: two
// in-session persona switches in a row — the second one must rebind
// persona, prompt body and directive, not keep the first switch's
// persona. It is not the behavioral report reproduction (the 0.155.0
// model-behavior drift the supersession clause answers); it pins that
// session state follows every pick.
func TestDoubleSwitchStateRebinding(t *testing.T) {
	sceneTestRegistry(t)

	build, _ := subagent.ResolvePersona(subagent.BuildPersonaName)
	layers := prompts.Layers{Identity: prompts.IdentityPrompt}
	sys, end := prompts.BuildMarked(layers)
	s := &agentSession{cfg: &config.Config{}}
	s.promptLayers = layers
	s.systemPrompt = sys
	s.identityEnd = end
	s.persona = build
	base := buildMainPrompt(s.cfg, SessionOptions{}, sys, end)
	s.mainPrompt = base
	s.SetView(&safeCaptureView{})

	t.Run("first switch to grump", func(t *testing.T) {
		if err := s.SetPersona("grump"); err != nil {
			t.Fatalf("SetPersona(grump): %v", err)
		}
		if got := s.PersonaName(); got != "grump" {
			t.Fatalf("persona = %q, want grump", got)
		}
		if s.mainPrompt == base {
			t.Fatal("grump switch left mainPrompt unchanged")
		}
	})

	t.Run("second switch to goat-joke-teller", func(t *testing.T) {
		if err := s.SetPersona("goat-joke-teller"); err != nil {
			t.Fatalf("SetPersona(goat-joke-teller): %v", err)
		}
		if got := s.PersonaName(); got != "goat-joke-teller" {
			t.Fatalf("persona still %q after the second switch; want goat-joke-teller", got)
		}
		if !strings.Contains(s.systemPrompt, "GOAT BODY MARKER") {
			t.Errorf("second switch did not rebind the persona body:\n%s", s.systemPrompt)
		}
		if strings.Contains(s.systemPrompt, "GRUMP BODY MARKER") {
			t.Errorf("second switch kept grump body text:\n%s", s.systemPrompt)
		}
		if !strings.Contains(s.modeDirective(s.persona), "Goat-joke-teller persona is active") {
			t.Errorf("directive still names the grump persona:\n%s", s.modeDirective(s.persona))
		}
	})
}

// TestReplacePersonaRebindsIdentityAndAnchor pins the rebuild side of
// `system: replace` (parity W2): switching to a replace-mode persona
// rebinds the persona body AS the identity layer — the embedded identity
// text must be gone from the rebuilt system prompt — and the anchor must
// travel with it, so the main prompt's directives still land after the
// replaced identity. It also pins the non-replace counterpart: a layer
// persona keeps the embedded identity and the construction anchor.
func TestReplacePersonaRebindsIdentityAndAnchor(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"replacer.md": "---\nname: Rex\nspecialty: replacer\ndescription: Replaces the identity\ntools: []\nsystem: replace\n---\nREPLACED IDENTITY MARKER: The session runs as this body.\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(nil) })

	build, ok := subagent.ResolvePersona(subagent.BuildPersonaName)
	if !ok {
		t.Fatal("the built-in build persona must resolve")
	}
	layers := prompts.Layers{Identity: prompts.IdentityPrompt}
	sys, end := prompts.BuildMarked(layers)
	s := &agentSession{cfg: &config.Config{}}
	s.promptLayers = layers
	s.systemPrompt = sys
	s.identityEnd = end
	s.persona = build
	s.SetView(&safeCaptureView{})

	opts := SessionOptions{DirectiveOverrides: []string{"always run tests"}}
	s.opts = opts
	s.mainPrompt = buildMainPrompt(s.cfg, opts, sys, end)

	if err := s.SetPersona("replacer"); err != nil {
		t.Fatalf("SetPersona(replacer): %v", err)
	}

	// The replaced identity owns the leading bytes; the anchor follows it.
	const replaced = "REPLACED IDENTITY MARKER: The session runs as this body."
	if !strings.HasPrefix(s.systemPrompt, "REPLACED IDENTITY MARKER") {
		t.Fatalf("the replace persona's body must lead the rebuilt system prompt:\n%q", s.systemPrompt[:min(len(s.systemPrompt), 120)])
	}
	if strings.Contains(s.systemPrompt, strings.TrimSpace(prompts.IdentityPrompt)) {
		t.Error("the rebuilt prompt must not carry the embedded identity text")
	}
	wantAnchor := len(replaced)
	if s.identityEnd != wantAnchor {
		t.Fatalf("anchor = %d, want the replaced body's end %d (construction anchor %d)", s.identityEnd, wantAnchor, end)
	}
	// Directives in the rebuilt main prompt sit after the replaced identity.
	head := strings.Index(s.mainPrompt, prompts.FormatDirectives(opts.DirectiveOverrides))
	if head < 0 || head != s.identityEnd+2 {
		t.Fatalf("main prompt directives must land at the rebuilt anchor: head=%d anchor=%d", head, s.identityEnd)
	}
	if strings.Contains(s.mainPrompt, strings.TrimSpace(prompts.IdentityPrompt)) {
		t.Error("the rebuilt main prompt must not carry the embedded identity text")
	}
}

// TestReplacePersonaTransitionsAndModeDirective extends
// TestReplacePersonaRebindsIdentityAndAnchor over the full transition
// matrix (review finding 5): replace→replace with a different body length,
// replace→layer (identity and anchor restore), and the per-run mode
// directive — which rides the same anchor — landing after a replaced
// identity.
func TestReplacePersonaTransitionsAndModeDirective(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"replacer.md": "---\nname: Rex\ndescription: long body\ntools: []\nsystem: replace\n---\n" + strings.Repeat("R", 300) + "\n",
		"shrinker.md": "---\nname: Sal\ndescription: short body\ntools: []\nsystem: replace\n---\nS-SHORT\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(nil) })

	build, ok := subagent.ResolvePersona(subagent.BuildPersonaName)
	if !ok {
		t.Fatal("the built-in build persona must resolve")
	}
	layers := prompts.Layers{Identity: prompts.IdentityPrompt}
	sys, constructionEnd := prompts.BuildMarked(layers)
	s := &agentSession{cfg: &config.Config{}}
	s.opts = SessionOptions{DirectiveOverrides: []string{"always run tests"}}
	s.promptLayers = layers
	s.systemPrompt = sys
	s.identityEnd = constructionEnd
	s.persona = build
	s.SetView(&safeCaptureView{})
	s.mainPrompt = buildMainPrompt(s.cfg, s.opts, sys, constructionEnd)

	if err := s.SetPersona("replacer"); err != nil {
		t.Fatalf("SetPersona(replacer): %v", err)
	}
	if s.identityEnd != 300 {
		t.Fatalf("replace→replace grow: anchor = %d, want 300", s.identityEnd)
	}
	if !strings.HasPrefix(s.systemPrompt, strings.Repeat("R", 300)) {
		t.Fatal("replace→replace grow: the new body must lead")
	}

	// The mode directive rides the same anchor: on the replaced identity it
	// must land right after the new identity, not at the prompt end.
	directive := s.modeDirective(s.persona)
	head := strings.Index(directive, "## Session directives")
	if head < 0 || head != 302 { // len(sep "\n\n") after the 300-byte identity
		t.Fatalf("mode directive must land at the replaced identity's anchor: head=%d anchor=%d", head, s.identityEnd)
	}

	if err := s.SetPersona("shrinker"); err != nil {
		t.Fatalf("SetPersona(shrinker): %v", err)
	}
	if s.identityEnd != len("S-SHORT") {
		t.Fatalf("replace→replace shrink: anchor = %d, want %d", s.identityEnd, len("S-SHORT"))
	}
	if got := strings.Index(s.systemPrompt, strings.Repeat("R", 300)); got >= 0 {
		t.Fatal("replace→replace shrink: the old long body must be gone")
	}

	// replace→layer: back to build — the embedded identity returns, the
	// anchor returns to the construction position, and build carries no
	// body layer.
	if err := s.SetPersona("build"); err != nil {
		t.Fatalf("SetPersona(build): %v", err)
	}
	if s.identityEnd != constructionEnd {
		t.Fatalf("replace→layer: anchor = %d, want the construction anchor %d", s.identityEnd, constructionEnd)
	}
	if !strings.HasPrefix(s.systemPrompt, strings.TrimSpace(prompts.IdentityPrompt)) {
		t.Fatal("replace→layer: the embedded identity must lead again")
	}
}

// TestResumeAnchorDerivesFromStoredPrompt pins the resume-path fix for the
// same anchor trap the wiring path covers: a stored replace-persona prompt
// whose role file changed between sessions must not reuse the fresh
// assembly's anchor (it would point inside the stored identity and inject
// the per-run mode directive mid-block on every request). The anchor
// degrades to -1 and the injection falls back to the prompt end — visible
// and directionless, never mid-identity.
func TestResumeAnchorDerivesFromStoredPrompt(t *testing.T) {
	stored := "OLD REPLACE IDENTITY, LONGER THAN THE NEW BODY\n\n## Environment Header\n\nbody tail"
	current := subagent.Persona{Name: "replacer", Body: "NEW BODY", SystemReplace: true}

	if got := identityAnchorFor(stored, current); got != -1 {
		t.Fatalf("a changed replace body must not yield an anchor, got %d", got)
	}
	// With the -1 anchor the injection must append at the end, after the
	// whole stored prompt — not inside the identity text.
	got := prompts.InjectAfterIdentityAt(stored, -1, []string{"always run tests"})
	if !strings.HasSuffix(got, prompts.FormatDirectives([]string{"always run tests"})) {
		t.Fatalf("a -1 anchor must degrade to the prompt end:\n%q", got)
	}
	if strings.Contains(got[:len("OLD REPLACE IDENTITY, LONGER THAN THE NEW BODY")], "## Session directives") {
		t.Fatal("directives must never inject inside the stored identity")
	}

	// The matching-body case still anchors precisely.
	fresh := "CURRENT REPLACE BODY\n\n## Environment"
	if got := identityAnchorFor(fresh, subagent.Persona{Name: "r", Body: "CURRENT REPLACE BODY", SystemReplace: true}); got != len("CURRENT REPLACE BODY") {
		t.Fatalf("an unchanged replace body anchors at its end: got %d", got)
	}
}

// TestResumeSessionSwitchToReplaceSplicesIdentity pins review finding 2's
// headline gap: switching to a replace persona on a RESUMED session — whose
// retained layers never reproduce the stored prompt (the memory-guidance
// gate plus drift) — must still rebind the identity, and only the identity:
// the stored tail survives byte-for-byte, the old layer body is gone, and
// the directives land at the new anchor.
func TestResumeSessionSwitchToReplaceSplicesIdentity(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"replacer.md": "---\nname: Rex\ndescription: replaces identity\ntools: []\nsystem: replace\n---\nREPLACED RESUME IDENTITY\n",
		"grump.md":    "---\nname: Harold\ndescription: layer persona\ntools: []\n---\nGRUMP STORED BODY LAYER\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(nil) })

	grump, ok := subagent.ResolvePersona("grump")
	if !ok {
		t.Fatal("the layer persona must resolve")
	}
	const grumpLayer = "GRUMP STORED BODY LAYER"
	const storedTail = "## Environment\nstored environment bytes\n\n## Memory\nstored memory snapshot"
	stored := strings.TrimSpace(prompts.IdentityPrompt) + "\n\n" + prompts.PersonaGuidanceBlock(grumpLayer) + "\n\n" + storedTail

	s := &agentSession{cfg: &config.Config{}}
	s.opts = SessionOptions{ResumeSessionID: "sess-resumed", DirectiveOverrides: []string{"always run tests"}}
	s.promptLayers = prompts.Layers{Identity: prompts.IdentityPrompt} // retained layers describe TODAY, not the stored prompt
	s.promptMemory = prompts.MemoryGuidance{}                         // resumed sessions get no fresh guidance block
	s.systemPrompt = stored
	s.identityEnd = len(strings.TrimSpace(prompts.IdentityPrompt)) + 2 + len(prompts.PersonaGuidanceBlock(grumpLayer))
	s.persona = grump
	s.SetView(&safeCaptureView{})
	s.mainPrompt = buildMainPrompt(s.cfg, s.opts, stored, s.identityEnd)

	if err := s.SetPersona("replacer"); err != nil {
		t.Fatalf("SetPersona(replacer): %v", err)
	}

	// The identity slice is spliced; the drift-protected tail is untouched.
	if !strings.HasPrefix(s.systemPrompt, "REPLACED RESUME IDENTITY") {
		t.Fatalf("resumed switch must splice the replace body in as the identity:\n%q", s.systemPrompt[:min(len(s.systemPrompt), 120)])
	}
	if !strings.Contains(s.systemPrompt, storedTail) {
		t.Fatal("the splice must keep the stored tail byte-for-byte")
	}
	if strings.Contains(s.systemPrompt, "GRUMP STORED BODY LAYER") {
		t.Fatal("the splice must remove the previous layer persona's body")
	}
	if s.identityEnd != len("REPLACED RESUME IDENTITY") {
		t.Fatalf("anchor = %d, want the spliced body's end", s.identityEnd)
	}
	if head := strings.Index(s.mainPrompt, "## Session directives"); head < 0 || head != s.identityEnd+2 {
		t.Fatalf("directives must land at the spliced anchor: head=%d anchor=%d", head, s.identityEnd)
	}
}
