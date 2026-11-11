package tools

// Compatibility seam for the substrate extraction (bead yaah-5D85).
//
// The tool substrate — the policy and authority types the leaf tools are
// written against and that carry no tool implementation — now lives in
// internal/tools/substrate. The aliases and forwarders below keep the leaf
// tools reading unqualified names (and external callers reading tools.X)
// while the rest of the package is migrated. New code should import
// internal/tools/substrate directly; these names are transitional.

import (
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools/substrate"
	"os"
)

// Belt is the set of tools a session may call. See substrate.Belt.
type Belt = substrate.Belt

// NewBelt builds a belt from the configured allowlist.
func NewBelt(names []string) *Belt { return substrate.NewBelt(names) }

// DispatchGrants is a dispatch world's declared write authority. See
// substrate.DispatchGrants.
type DispatchGrants = substrate.DispatchGrants

// ResolveGrants lowers the configured grants to absolute roots under worldRoot.
func ResolveGrants(grants []config.GrantConfig, worldRoot string) (DispatchGrants, error) {
	return substrate.ResolveGrants(grants, worldRoot)
}

// Names from the workspace/path-validator port. Var aliases keep
// existing call sites unchanged while the migration settles.
type (
	ExecRequest         = substrate.ExecRequest
	ExecResult          = substrate.ExecResult
	Workspace           = substrate.Workspace
	WorkspaceSetter     = substrate.WorkspaceSetter
	PathValidator       = substrate.PathValidator
	PathRefusalError    = substrate.PathRefusalError
	PathValidatorSetter = substrate.PathValidatorSetter
	HarnessPaths        = substrate.HarnessPaths
	HarnessRefusalError = substrate.HarnessRefusalError
)

var (
	WorkspaceDegraded         = substrate.WorkspaceDegraded
	NewLocalWorkspace         = substrate.NewLocalWorkspace
	NewConfinedLocalWorkspace = substrate.NewConfinedLocalWorkspace
	WorkspaceConfined         = substrate.WorkspaceConfined
	NewPathValidator          = substrate.NewPathValidator
	NewHarnessPaths           = substrate.NewHarnessPaths
)

// Forwarders for helpers that moved with the workspace port. Leaf tool
// code keeps calling the unqualified names.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return substrate.AtomicWriteFile(path, data, perm)
}

func mergeEnv(base, overrides []string) []string {
	return substrate.MergeEnv(base, overrides)
}

type sandboxWorkspace = substrate.SandboxWorkspace

func newSandboxWorkspace(sb goatherdsandbox.Sandbox, root string) *sandboxWorkspace {
	return substrate.NewSandboxWorkspace(sb, root)
}

func workspaceOf(ws Workspace, pv *PathValidator) Workspace {
	return substrate.WorkspaceOf(ws, pv)
}

func shellCommand(ws Workspace, command string) ExecRequest {
	return substrate.ShellCommand(ws, command)
}

func requireLocal(ws Workspace, tool string) error {
	return substrate.RequireLocal(ws, tool)
}

// Lowercase aliases for helpers that moved with the workspace port; leaf and
// dispatch-lane code keeps calling the unqualified names.
var (
	isRootPath             = substrate.IsRootPath
	isCrossDeviceErr       = substrate.IsCrossDeviceErr
	errCrossDevice         = substrate.ErrCrossDevice
	isCrossDeviceMoveError = substrate.IsCrossDeviceMoveError
	sameHostPath           = substrate.SameHostPath
	walkWorkspace          = substrate.WalkWorkspace
)

type partialMoveError = substrate.PartialMoveError

func newLocalWorkspace(pv *PathValidator) Workspace {
	return substrate.NewLocalWorkspace(pv)
}

type localWorkspace = substrate.LocalWorkspace

func confinementHint(stderr, root string) string {
	return substrate.ConfinementHint(stderr, root)
}
