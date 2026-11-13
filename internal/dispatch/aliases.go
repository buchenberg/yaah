package dispatch

import (
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/tools/substrate"
)

// Seam aliases: the workspace port lives in internal/tools/substrate and the
// sub-agent I/O contract in internal/jobs; these keep the dispatch lane's call
// sites readable without re-qualifying every use.
type (
	// Workspace is the substrate workspace port.
	Workspace = substrate.Workspace
	// PathValidator is the substrate path validator.
	PathValidator = substrate.PathValidator
	// SandboxOpener opens a sandbox for a session world.
	SandboxOpener = substrate.SandboxOpener
	// SubAgentWorld is the jobs I/O contract descriptor.
	SubAgentWorld = jobs.SubAgentWorld
	// TaskRunner is the sub-agent run function signature.
	TaskRunner = jobs.TaskRunner
	// SubAgentParams is the sub-agent dispatch parameter set.
	SubAgentParams = jobs.SubAgentParams
)

var (
	drivePath                      = substrate.DrivePath
	WithSubAgentWorldPtr           = jobs.WithSubAgentWorldPtr
	ErrStuckChild                  = jobs.ErrStuckChild
	ErrDispatchStartup             = jobs.ErrDispatchStartup
	deniedBy                       = substrate.DeniedBy
	NewPathValidator               = substrate.NewPathValidator
	atomicWriteFile                = substrate.AtomicWriteFile
	withinRoot                     = substrate.WithinRoot
	keepTail                       = substrate.KeepTail
	truncationMarker               = substrate.TruncationMarker
	ScrubConfinementScratch        = substrate.ScrubConfinementScratch
	DispatchWorkspace              = substrate.DispatchWorkspace
	OpenContainerdWorkspaceSandbox = substrate.OpenContainerdWorkspaceSandbox
	StartSessionSandbox            = substrate.StartSessionSandbox
	WithDispatchWorkspace          = substrate.WithDispatchWorkspace
	WriteSubAgentWorld             = jobs.WriteSubAgentWorld
	NotifySubAgentReady            = jobs.NotifySubAgentReady
)

func newLocalWorkspace(pv *PathValidator) Workspace {
	return substrate.NewLocalWorkspace(pv)
}

// ProposalContentDigest is the exported face the tools layer calls through
// its dispatch_aliases seam.
func ProposalContentDigest(data []byte) string { return proposalContentDigest(data) }
