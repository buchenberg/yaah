// Package worldinfo formats the workspace / dispatch-world rows of the info
// pane. The former single "World" section is dissolved: its workspace rows
// are folded into the Agent section and its dispatch/containment rows into
// the Sub-agents section.
package worldinfo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui/colors"
	worldstatus "github.com/buchenberg/yaah/internal/worldinfo"
)

// AgentRows returns the workspace rows folded into the Agent section: the
// dispatch backend (workspace) and the containment scope (root, or the
// unrestricted host when no root is configured). The zero Status means the
// session never populated the posture, so the rows are omitted entirely.
func AgentRows(s worldstatus.Status, th *colors.Theme) string {
	if s.IsZero() {
		return ""
	}

	var b strings.Builder
	workspace := s.Backend
	if s.Isolated {
		workspace += " · isolated"
	}
	fmt.Fprintf(&b, "  Workspace: %s\n", th.Tag(th.Detail, workspace))

	if s.Root != "" {
		fmt.Fprintf(&b, "  Scope: %s\n", th.Tag(th.Detail, s.Root))
	} else {
		fmt.Fprintf(&b, "  Scope: %s\n", th.Tag(th.Detail, "unrestricted host"))
	}
	return b.String()
}

// SubAgentRows returns the dispatch/containment rows folded into the
// Sub-agents section: containment (incl. jail availability), dispatch world,
// and network posture. The zero Status means the session never populated the
// posture, so the rows are omitted entirely.
func SubAgentRows(s worldstatus.Status, th *colors.Theme) string {
	if s.IsZero() {
		return ""
	}

	var b strings.Builder
	jail := "unavailable"
	if s.JailOK {
		jail = "available"
	}
	fmt.Fprintf(&b, "  Containment: %s\n",
		th.Tag(th.Detail, s.Containment+" · jail: "+jail))

	network := s.Network
	if s.Network == "broker" {
		network += fmt.Sprintf(" · %d allowed host(s)", s.NetworkHosts)
	}
	fmt.Fprintf(&b, "  Network: %s\n", th.Tag(th.Detail, network))

	// The lane prefix is derived from s.Mode so the row stays correct if a new
	// mode is added; every non-shared branch carries it consistently.
	lane := s.Mode + " · "

	var dispatch string
	switch {
	case s.Mode == "shared":
		// A shared dispatch runs on the session tree by definition.
		dispatch = "shared · no dispatch world"
	case s.ReportWorld != s.ProposalWorld:
		dispatch = fmt.Sprintf("%s%s → proposal %s · report %s", lane, s.WorldReq, s.ProposalWorld, s.ReportWorld)
	case s.WorldReq == s.ProposalWorld:
		// Both lanes resolve to the requested world verbatim; collapse the
		// "req → world" ladder (no "container → container" noise) but keep
		// the lane prefix for visibility.
		dispatch = lane + s.ProposalWorld
	default:
		dispatch = lane + s.WorldReq + " → " + s.ProposalWorld
	}
	fmt.Fprintf(&b, "  Dispatch: %s\n", th.Tag(th.Detail, dispatch))

	if s.Degraded && s.Reason != "" {
		fmt.Fprintf(&b, "  %s\n", th.Tag(th.Dim, s.Reason))
	}
	return b.String()
}
