package yaah

// persona_reload_test.go pins yaah-21uk.11's reload semantics: role
// reloads refresh the registry, so a NEW session's resolveSessionPersona
// sees the new persona view (added, edited, deleted), while an EXISTING
// agentSession keeps the persona, budget, and belt it captured at
// construction. The reload output reports the refreshed persona view
// and the next-session-only rule.

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/control"
)

// saveDefaultRoleRegistry restores whatever registry was installed
// before the test, so a reload-driven test cannot leak its registry
// into later tests in the package.
func saveDefaultRoleRegistry(t *testing.T) {
	t.Helper()
	saved := subagent.DefaultRegistry()
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(saved) })
}

// reloadPersonaRegistry rebuilds the default registry through the same
// funnel both reload triggers use: the role tool's mutations go through
// SubAgentRoleResolver.ReloadRoles and the REPL's reloadRoles calls
// subagent.ReloadDefaultRoles — one registry swap either way.
func reloadPersonaRegistry(t *testing.T, dir string) {
	t.Helper()
	resolver := runner.SubAgentRoleResolver{}
	if err := resolver.ReloadRoles(runner.BuiltinRoleFiles(), []string{dir}); err != nil {
		t.Fatalf("ReloadRoles: %v", err)
	}
}

// writePersonaRoleFile writes one role file into the reload dir.
func writePersonaRoleFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// personaNamed reports whether the persona list contains name.
func personaNamed(personas []subagent.Persona, name string) bool {
	return slices.ContainsFunc(personas, func(p subagent.Persona) bool { return p.Name == name })
}

// TestRoleReloadRefreshesPersonaResolutionForNewSessions walks the
// lifecycle: a role file that does not exist is not a persona, adding a
// primary-capable one makes the NEXT resolveSessionPersona resolve it
// (and lists it in Personas), a delegate-only file registers without
// becoming a persona, and deleting the file drops it again.
func TestRoleReloadRefreshesPersonaResolutionForNewSessions(t *testing.T) {
	saveDefaultRoleRegistry(t)
	dir := t.TempDir()

	// Before the file exists: planner is missing.
	reloadPersonaRegistry(t, dir)
	if _, ok := subagent.ResolvePersona("planner"); ok {
		t.Fatal("planner resolved before its role file existed")
	}

	// Add a primary-capable role: the next session resolves it.
	writePersonaRoleFile(t, filepath.Join(dir, "planner.md"), `---
name: Planner
mode: all
tools: [read]
max_iterations: 30
max_turns: 20
description: plans changes
---
plan body
`)
	reloadPersonaRegistry(t, dir)
	sp := resolveSessionPersona("agents.default.agent", "planner")
	if sp.Fallback || sp.Persona.Name != "planner" {
		t.Fatalf("resolveSessionPersona(planner) after add = %+v, want the planner persona", sp)
	}
	if !personaNamed(subagent.Personas(), "planner") {
		t.Error("Personas() after add does not list planner")
	}

	// A delegate-only file registers as a role but never as a persona.
	writePersonaRoleFile(t, filepath.Join(dir, "grunt.md"), `---
name: Grunt
mode: subagent
tools: [bash]
description: does work
---
work body
`)
	reloadPersonaRegistry(t, dir)
	if _, ok := subagent.ResolvePersona("grunt"); ok {
		t.Error("grunt (mode subagent) resolved as a persona")
	}

	// Delete the role file: the next session no longer sees it.
	if err := os.Remove(filepath.Join(dir, "planner.md")); err != nil {
		t.Fatalf("removing planner.md: %v", err)
	}
	reloadPersonaRegistry(t, dir)
	sp = resolveSessionPersona("agents.default.agent", "planner")
	if !sp.Fallback || sp.Persona.Name != subagent.BuildPersonaName {
		t.Fatalf("resolveSessionPersona(planner) after delete = %+v, want fallback to build", sp)
	}
	if personaNamed(subagent.Personas(), "planner") {
		t.Error("Personas() after delete still lists planner")
	}
}

// TestSessionPersonaFixedAcrossRoleReload pins the invariant the other
// half of the semantics depends on: an existing agentSession keeps the
// persona (and the budget and belt derived from it) it captured at
// construction, even when the underlying role file is edited or deleted
// underneath it and the registry reloaded. A session built after the
// reload resolves the new values; the running one does not change.
func TestSessionPersonaFixedAcrossRoleReload(t *testing.T) {
	saveDefaultRoleRegistry(t)
	dir := t.TempDir()
	writePersonaRoleFile(t, filepath.Join(dir, "planner.md"), `---
name: Planner
mode: all
tools: [read]
max_iterations: 30
max_turns: 20
description: plans changes
---
plan body
`)
	reloadPersonaRegistry(t, dir)

	built := resolveSessionPersona("agents.default.agent", "planner")
	s := &agentSession{cfg: &config.Config{}, persona: built.Persona}
	personaBefore := s.persona
	budgetBefore := resolveOrchestratorBudget(s.cfg, s.persona)
	beltBefore := effectiveBeltNames(nil, s.persona.Tools)

	// The role changes underneath the session: smaller budgets and a
	// wider tool list, same name.
	writePersonaRoleFile(t, filepath.Join(dir, "planner.md"), `---
name: Planner
mode: all
tools: [read, grep]
max_iterations: 7
max_turns: 3
description: plans changes
---
plan body
`)
	reloadPersonaRegistry(t, dir)

	// A session built NOW resolves the new values.
	fresh := resolveSessionPersona("agents.default.agent", "planner")
	if fresh.Fallback || fresh.Persona.MaxLoopCycles != 7 || fresh.Persona.MaxToolTurns != 3 {
		t.Fatalf("session built after reload = %+v, want the edited budgets 7/3", fresh)
	}

	// The existing session keeps what it captured.
	if !reflect.DeepEqual(s.persona, personaBefore) {
		t.Errorf("existing session persona changed after reload: %+v, want %+v", s.persona, personaBefore)
	}
	if belt := effectiveBeltNames(nil, s.persona.Tools); !slices.Equal(belt, beltBefore) {
		t.Errorf("existing session belt = %v, want the captured %v", belt, beltBefore)
	}
	if got := resolveOrchestratorBudget(s.cfg, s.persona); got.Iterations != budgetBefore.Iterations || got.Turns != budgetBefore.Turns {
		t.Errorf("existing session budget = %d/%d, want the captured %d/%d", got.Iterations, got.Turns, budgetBefore.Iterations, budgetBefore.Turns)
	}

	// Deleting the role file entirely also leaves the session untouched.
	if err := os.Remove(filepath.Join(dir, "planner.md")); err != nil {
		t.Fatalf("removing planner.md: %v", err)
	}
	reloadPersonaRegistry(t, dir)
	if s.persona.Name != "planner" {
		t.Errorf("existing session persona = %q, want planner even after the file was deleted", s.persona.Name)
	}
}

// TestReloadRolesReportsPersonaView pins the visibility half: the
// /roles and /reload-roles output reports the refreshed persona view
// and states the next-session-only rule, so the user can see what the
// next session will pick up and knows the current session is untouched.
func TestReloadRolesReportsPersonaView(t *testing.T) {
	saveDefaultRoleRegistry(t)
	ch := make(chan control.Msg, 8)
	s := &agentSession{cwd: t.TempDir(), ctrlCh: ch}
	s.reloadRoles()

	var statuses []string
drain:
	for {
		select {
		case m := <-ch:
			if st, ok := m.(*control.Status); ok {
				statuses = append(statuses, st.Text)
			}
		default:
			break drain
		}
	}

	var personaLine string
	for _, text := range statuses {
		if strings.Contains(text, "personas:") {
			personaLine = text
		}
	}
	if personaLine == "" {
		t.Fatalf("reload output has no personas line: %q", statuses)
	}
	// The synthetic built-ins lead the list; registry personas follow.
	if !strings.HasPrefix(personaLine, "personas: build, plan") {
		t.Errorf("personas line = %q, want the built-ins leading", personaLine)
	}
	if !strings.Contains(personaLine, "new sessions") {
		t.Errorf("personas line %q does not state the next-session rule", personaLine)
	}
}
