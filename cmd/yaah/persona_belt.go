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
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/permissions"
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
// keep their existing warning verbatim. Persona names are checked
// against the registry rather than the belt: since narrowing moved to
// the per-run seam (yaah-21uk.12) the belt is config-only, so a persona
// name is a typo exactly when the registry holds no tool under it and
// the belt never met the name — not known via a gate (memory tools,
// role) and not refused as a real tool the config belt turned away.
func unknownToolWarnings(tr *toolRegistryStage) []string {
	if tr.belt == nil {
		return nil
	}
	var out []string
	warned := make(map[string]bool)
	for _, name := range tr.belt.Unknown() {
		warned[name] = true
		out = append(out, fmt.Sprintf("warning: config `agents.default.tools` names unknown tool %q; ignored", name))
	}
	for _, name := range tr.personaTools {
		if warned[name] || tr.reg.Get(name) != nil || tr.belt.Knows(name) {
			continue
		}
		out = append(out, fmt.Sprintf("warning: persona %q names unknown tool %q; ignored", tr.personaName, name))
	}
	return out
}

// mergePermissions composes the global config ruleset with the session
// persona's rules (parity W5.4, opencode semantics): global rules first,
// persona rules appended after, and last-match-wins means the persona's
// rule wins wherever both name the same tool-and-resource. Neither side
// can widen the tool belt or re-enable a withheld tool — the ruleset
// only refines within what the mode axis and the belt already allow.
func mergePermissions(global, persona permissions.Rules) permissions.Rules {
	if len(global) == 0 {
		return persona
	}
	if len(persona) == 0 {
		return global
	}
	out := make(permissions.Rules, 0, len(global)+len(persona))
	out = append(out, global...)
	out = append(out, persona...)
	return out
}

// savedAllowConsult is the persisted-"always" consultant the loop's
// evaluator asks before prompting: it reports whether a saved row already
// approves (tool, resource). The consult never widens — its only caller is
// the evaluator's Ask branch, so a deny from the ruleset or a posture stays
// terminal whatever the store says.
//
// Credential-shaped targets never consult (yaah-x6vi.7): rows naming a
// secrets file are skipped when the ruleset is built, so an "always" for
// `.env` — saved before the carve-out or by a future writer that missed the
// check — stays an ask forever. The resource this filters is the ask's own
// derivation, the same string the ACP save site refused, so the two sides
// cannot disagree about what counts.
//
// The cwd is canonicalized exactly like the rows that were written
// (cwdpath.CanonicalStored), so a session started from a symlinked path
// still reads the rows it wrote. A query failure is not its caller's deny:
// the consultant warns and reports "no saved row", which re-asks — a store
// problem must re-prompt, never silently allow.
func savedAllowConsult(db *memory.DB, cwd string) func(name, resource string) bool {
	if db == nil {
		return nil
	}
	rows, err := db.SavedPermissions(cwdpath.CanonicalStored(cwd))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: saved approvals unavailable, re-asking every call: %v\n", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	rules := make(permissions.Rules, 0, len(rows))
	for _, r := range rows {
		if permissions.SensitiveResource(r.Action, r.Resource) {
			continue
		}
		rules = append(rules, permissions.Rule{Tool: r.Action, Resource: r.Resource, Effect: permissions.Allow})
	}
	return func(name, resource string) bool {
		// Defence in depth. The evaluator decides the carve-out on the call
		// (permissions.SensitiveCall), but this closure must not depend on
		// that: Rules.Match treats an empty Resource as tool-wide and a
		// non-empty one as a glob, so a row this filter left in — because
		// the row's own basename looked innocuous — can still match a
		// secrets target at consult time.
		if permissions.SensitiveResource(name, resource) {
			return false
		}
		eff, matched := rules.Match(name, resource)
		return matched && eff == permissions.Allow
	}
}
