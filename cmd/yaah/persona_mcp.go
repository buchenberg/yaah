package yaah

// persona_mcp.go is the seam where a persona's tools allowlist meets the
// MCP tools a session discovered. Narrowing is exact-match and
// narrow-only (internal/agent's narrowToPersonaTools), and the MCP names
// are learned only after the servers start — so a role file written
// without them silently loses every remote tool. Nothing else in the
// pipeline reports that: the tool simply is not in the request. This
// file makes the loss visible and tells the author how to undo it.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// personaMCPOmissions returns the registered MCP tool names the
// persona's tools allowlist excludes, sorted. An empty persona list
// narrows nothing, so nothing is omitted; the same is true when no MCP
// server registered a tool.
func personaMCPOmissions(personaTools []string, mcpNames map[string]bool) []string {
	allowed := normalizeToolNames(personaTools)
	if len(allowed) == 0 || len(mcpNames) == 0 {
		return nil
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	var omitted []string
	for name := range mcpNames {
		if !allowedSet[name] {
			omitted = append(omitted, name)
		}
	}
	sort.Strings(omitted)
	return omitted
}

// personaMCPOmissionWarning names the MCP tools a persona's allowlist
// drops, with the two remedies. Empty when nothing is dropped.
func personaMCPOmissionWarning(personaName string, personaTools []string, mcpNames map[string]bool) string {
	omitted := personaMCPOmissions(personaTools, mcpNames)
	if len(omitted) == 0 {
		return ""
	}
	return fmt.Sprintf("warning: persona %q tools list omits %d MCP tool(s) — they are not available in this session: %s (add the names to the role file's tools list, or remove the list to keep every MCP tool)",
		personaName, len(omitted), strings.Join(omitted, ", "))
}

// warnPersonaMCPOmissions prints the warning when there is one.
func warnPersonaMCPOmissions(personaName string, personaTools []string, mcpNames map[string]bool) {
	if warning := personaMCPOmissionWarning(personaName, personaTools, mcpNames); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
}
