// Package worldinfo formats the workspace / dispatch-world section of the
// info pane.
package worldinfo

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui/colors"
	worldstatus "github.com/buchenberg/yaah/internal/worldinfo"
)

// Format returns the World section. The zero Status means the session never
// populated the posture, so the section is omitted entirely (an empty string
// lets the caller append without a separator).
func Format(s worldstatus.Status, th *colors.Theme) string {
	if s.IsZero() {
		return ""
	}

	var b strings.Builder
	b.WriteString(th.TagBold(th.Heading, "World\n"))

	workspace := s.Backend
	if s.Isolated {
		workspace += " · isolated"
	}
	if s.Image != "" {
		workspace += " · image " + s.Image
	}
	fmt.Fprintf(&b, "  Workspace: %s\n", th.Tag(th.Detail, workspace))

	if s.Root != "" {
		fmt.Fprintf(&b, "  Root: %s\n", th.Tag(th.Detail, s.Root))
	} else {
		fmt.Fprintf(&b, "  Scope: %s\n", th.Tag(th.Detail, "unrestricted host"))
	}

	fmt.Fprintf(&b, "  Mode: %s\n", th.Tag(th.Detail, s.Mode))

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

	dispatch := s.WorldReq + " → " + s.ProposalWorld
	if s.Mode == "shared" {
		// A shared dispatch runs on the session tree by definition.
		dispatch = "shared · no dispatch world"
	} else if s.ReportWorld != s.ProposalWorld {
		dispatch = fmt.Sprintf("%s → proposal %s · report %s", s.WorldReq, s.ProposalWorld, s.ReportWorld)
	}
	fmt.Fprintf(&b, "  Dispatch: %s\n", th.Tag(th.Detail, dispatch))

	if s.Degraded && s.Reason != "" {
		fmt.Fprintf(&b, "  %s\n", th.Tag(th.Dim, s.Reason))
	}
	return b.String()
}
