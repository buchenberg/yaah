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
	t.Helper()
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
