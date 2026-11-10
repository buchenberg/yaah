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
