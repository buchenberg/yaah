package tools

import "github.com/buchenberg/yaah/internal/dispatch"

// Forwarders to the retained-dispatch lane now living in internal/dispatch.
// The tool leaves in this package (delegate, proposal) keep their historical
// unqualified names through this seam.
type (
	RetainedRuntime  = dispatch.RetainedRuntime
	retainedRunError = dispatch.RetainedRunError
	ProposalTool     = dispatch.ProposalTool
	// proposalEnvelope is reachable through an exported alias so the tool
	// leaves' tests can decode retained results.
	proposalEnvelope = dispatch.ProposalEnvelope
)

var (
	pathKey               = dispatch.PathKey
	newRetainedRunError   = dispatch.NewRetainedRunError
	noteBudget            = dispatch.NoteBudget
	proposalContentDigest = dispatch.ProposalContentDigest
)
