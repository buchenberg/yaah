package yaah

// persona_belt.go holds the tool-belt seam between the session persona
// and agents.default.tools (yaah-21uk.7). A persona's tools list
// narrows the orchestrator's tool surface through the same mechanism
// the configured belt uses — tools.NewBelt at buildToolRegistry — so
// there is one enforcement point (registry admission), one prompt
// delta (belt.Refused), and one unknown-name warning. Narrow-only by
// construction: an intersection can shrink the surface, never grow it.

import (
	"fmt"
	"slices"
	"strings"
)

// normalizeToolNames trims a tools list and drops blank entries,
// preserving order — the same normalization tools.NewBelt applies to
// the configured belt, so a persona name and a belt name compare equal
// exactly when they name the same tool.
func normalizeToolNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if t := strings.TrimSpace(n); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// effectiveBeltNames merges the configured tool belt with the session
// persona's tools list. Both inputs are normalized here (trim, drop
// blanks — the same normalization tools.NewBelt applies), so a padded
// name in either source still matches its clean twin. The four
// combinations:
//
//   - configured + persona: the intersection, in the configured
//     belt's order — a persona can only shrink the belt;
//   - persona only: the persona's list becomes the belt;
//   - configured only: pass-through, today's behavior exactly;
//   - neither (persona empty — the built-in build persona, or a role
//     file without tools): pass-through, the full surface.
//
// The return feeds tools.NewBelt, so an empty result still means "no
// belt configured" — the full surface — never "zero tools".
func effectiveBeltNames(configured, persona []string) []string {
	persona = normalizeToolNames(persona)
	if len(persona) == 0 {
		return configured // pass-through: today's belt, byte-identical
	}
	configured = normalizeToolNames(configured)
	if len(configured) == 0 {
		return persona
	}
	personaSet := make(map[string]bool, len(persona))
	for _, n := range persona {
		personaSet[n] = true
	}
	out := make([]string, 0, len(configured))
	for _, n := range configured {
		if personaSet[n] {
			out = append(out, n)
		}
	}
	return out
}

// unknownToolWarnings returns the startup warnings for allowlist names
// that matched no tool the session knows about. It runs from
// appendToolGuidance, after every registration and gate, when the
// belt's know/refuse accounting is final. The configured belt's names
// keep their existing warning verbatim; a persona-sourced name warns
// the same way but names the persona, and a persona name the
// configured belt's intersection dropped is not silently lost — it
// warns too, unless it matches a real tool (then the belt refused it
// at registration and the tool delta already reports it).
func unknownToolWarnings(tr *toolRegistryStage) []string {
	if tr.belt == nil {
		return nil
	}
	var out []string
	warned := make(map[string]bool)
	for _, name := range tr.belt.Unknown() {
		warned[name] = true
		if slices.Contains(tr.personaTools, name) {
			out = append(out, fmt.Sprintf("warning: persona %q names unknown tool %q; ignored", tr.personaName, name))
			continue
		}
		out = append(out, fmt.Sprintf("warning: config `agents.default.tools` names unknown tool %q; ignored", name))
	}
	// Persona names outside the belt's allowed set were dropped by the
	// configured belt's intersection. A real tool among them was
	// refused and known at registration; one that matches nothing is a
	// typo in the role file and deserves the same warning as a typo in
	// the config.
	for _, name := range tr.personaTools {
		if warned[name] || tr.belt.Allows(name) || tr.belt.Knows(name) {
			continue
		}
		out = append(out, fmt.Sprintf("warning: persona %q names unknown tool %q; ignored", tr.personaName, name))
	}
	return out
}
