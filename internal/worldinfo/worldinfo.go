// Package worldinfo derives the session's workspace and dispatch-world
// posture for display: what substrate the session runs on, what bounds a
// sub-agent dispatch, and which world the configured request resolves to on
// this host.
//
// It is the session-level counterpart of internal/tools' per-dispatch
// resolution. The derivation here deliberately mirrors the runtime ladder in
// internal/tools/retained.go (resolveDispatchWorld) rather than doctor's
// historical degradation flags: the surfaces must describe what a dispatch
// would actually get, and doctor consumes this package for the same reason.
// The parity test in internal/tools pins the two constructions together.
//
// Everything here is a cheap, non-mutating probe: a config read and the
// syscall-jail LSM check. No daemon round-trip happens at display time.
//
// Secrets rule: the containment root and image name are reportable; the
// containerd socket address, deny-pattern lists, and credentials are not, and
// are absent from Status by construction.
package worldinfo

import (
	"runtime"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jail"
)

// Status is the derived session-level posture. It is display data only:
// every surface must tolerate the zero value (hide the section / omit
// fields), which is what a session that never populated it carries.
type Status struct {
	// Backend is the session workspace backend: "local" or "containerd"
	// (the configured default applied).
	Backend string
	// Isolated reports that the session runs inside its own containerd
	// sandbox (sessionSandbox != nil in the wiring).
	Isolated bool
	// Root is the containment root: the --workspace path in a local
	// session, or the configured in-container root in an isolated one.
	// Empty means the host is unrestricted.
	Root string
	// Image is the containerd image backing the session sandbox or a
	// per-dispatch container world. Empty when no image is configured.
	Image string
	// Mode is the dispatch lane: "retained" or "shared" (default applied).
	Mode string
	// Containment is the configured sub-agent placement: "auto", "off", or
	// "paddock" (default applied).
	Containment string
	// JailOK reports that this host can establish the syscall jail.
	JailOK bool
	// Network is the configured egress posture: "allow_all", "deny_all",
	// or "broker" (default applied).
	Network string
	// NetworkHosts is the broker allowlist size; > 0 only when Network is
	// "broker".
	NetworkHosts int
	// WorldReq is the configured dispatch world: "auto", "worktree", or
	// "container" (default applied).
	WorldReq string
	// ProposalWorld is the resolved world for the proposal lane. In a
	// container session proposals are routed shared, so it reads "shared".
	ProposalWorld string
	// ReportWorld is the resolved world for the report lane.
	ReportWorld string
	// Degraded reports that the session's default lane could not get the
	// requested world.
	Degraded bool
	// Reason explains a degraded (or unavailable) resolution.
	Reason string
}

// IsZero reports whether the status was never populated. Surfaces use it to
// hide the World section instead of rendering an all-empty block.
func (s Status) IsZero() bool { return s == Status{} }

// Resolution is the raw per-lane outcome. Doctor uses it to keep the exact
// resolution errors in its presentation, and the parity test compares it
// against the runtime ladder.
type Resolution struct {
	// Requested is the normalized configured world ("auto" applied).
	Requested string
	// Shared reports that no dispatch world applies (mode: shared).
	Shared bool
	// Proposal and Report are the resolved lanes; Err is non-nil when the
	// request has no legal world.
	Proposal    subagent.WorldResolution
	ProposalErr error
	Report      subagent.WorldResolution
	ReportErr   error
}

// Derive builds the display status for a session. workspaceRoot is the
// --workspace path (ignored in an isolated session, whose root is the
// sandbox's own); isolated mirrors sessionSandbox != nil.
func Derive(cfg *config.Config, workspaceRoot string, isolated bool) Status {
	if cfg == nil {
		return Status{}
	}
	res := Resolve(cfg, isolated)

	s := Status{
		Backend:       backendOf(cfg),
		Isolated:      isolated,
		Root:          rootOf(cfg, workspaceRoot, isolated),
		Image:         strings.TrimSpace(cfg.Workspace.Containerd.Image),
		Mode:          Mode(cfg),
		Containment:   Containment(cfg),
		JailOK:        JailAvailable(),
		Network:       Network(cfg),
		WorldReq:      RequestedWorld(cfg),
		ProposalWorld: worldOrUnavailable(res.Proposal, res.ProposalErr),
		ReportWorld:   worldOrUnavailable(res.Report, res.ReportErr),
	}
	if s.Network == "broker" {
		s.NetworkHosts = len(cfg.Agent.SubAgent.NetworkHosts)
	}

	// The session-level degradation is the default lane's: the proposal
	// lane when it carries a world, otherwise the report lane (a container
	// session's proposals are routed shared and never degrade there). A
	// report-lane error still replaces a shared proposal so the reason
	// reaches the surfaces instead of leaving "unavailable" unexplained.
	primary, primaryErr := res.Proposal, res.ProposalErr
	if primaryErr == nil && primary.Resolved == subagent.WorldShared {
		primary, primaryErr = res.Report, res.ReportErr
	}
	switch {
	case primaryErr != nil:
		s.Degraded = true
		s.Reason = primaryErr.Error()
	case primary.Degraded:
		s.Degraded = true
		s.Reason = primary.Reason
	}
	return s
}

// Resolve applies the runtime ladder to the configured default world for
// both dispatch lanes. It resolves nothing that the runtime would not:
// mode shared has no world at all, and a container session routes its
// proposal lane shared (see cmd/yaah's LaneForRole).
func Resolve(cfg *config.Config, isolated bool) Resolution {
	if cfg == nil {
		return Resolution{}
	}
	sc := cfg.Agent.SubAgent
	av := Availability(cfg, isolated)
	containment := Containment(cfg)
	network := Network(cfg)
	grants := len(sc.Grants) > 0
	requested := RequestedWorld(cfg)

	out := Resolution{Requested: requested}
	resolve := func(lane string, retained bool) (subagent.WorldResolution, error) {
		return subagent.ResolveWorld(subagent.WorldRequest{
			Requested:         requested,
			Lane:              lane,
			Retained:          retained,
			Containment:       containment,
			Network:           network,
			RestrictiveGrants: grants,
		}, av)
	}

	if Mode(cfg) == "shared" {
		out.Shared = true
		out.Proposal, out.ProposalErr = resolve("shared", false)
		out.Report, out.ReportErr = resolve("shared", false)
		return out
	}

	if ContainerSession(cfg, isolated) {
		// Wiring routes every non-report role shared; only report roles
		// reach the retained runtime and resolve a sibling container.
		out.Proposal = subagent.WorldResolution{
			Requested: subagent.WorldKind(requested),
			Resolved:  subagent.WorldShared,
		}
		out.Report, out.ReportErr = resolve("report", false)
		return out
	}

	out.Proposal, out.ProposalErr = resolve("proposal", true)
	out.Report, out.ReportErr = resolve("report", false)
	return out
}

// Availability reports what this host and session can provide, constructed
// exactly as internal/tools' resolveDispatchWorld constructs it. A
// per-dispatch container is offered on Linux with an image configured; a
// container session (isolated + offered) has no host worktree and its
// proposals stay unretained.
func Availability(cfg *config.Config, isolated bool) subagent.WorldAvailability {
	offered := cfg != nil && runtime.GOOS == "linux" && strings.TrimSpace(cfg.Workspace.Containerd.Image) != ""
	containerSession := offered && isolated
	containerRetains := offered && !containerSession
	retainsReason := ""
	if containerSession {
		retainsReason = "container-session proposals remain unretained; report lanes use sibling containers"
	} else if !containerRetains {
		retainsReason = "container settlement is unavailable in this session: no per-dispatch container setup is wired"
	}
	worktree := !containerSession
	worktreeReason := ""
	if containerSession {
		worktreeReason = "a container session has no host worktree; report lanes use a sibling container"
	}
	return subagent.WorldAvailability{
		Container:              offered,
		ContainerOffered:       offered,
		ContainerRetains:       containerRetains,
		ContainerRetainsReason: retainsReason,
		ContainerReason:        "per-dispatch container worlds need workspace.containerd.image on a Linux host, and a wired dispatch setup",
		Worktree:               worktree,
		WorktreeReason:         worktreeReason,
		Jail:                   JailAvailable(),
	}
}

// ContainerSession reports whether the session runs inside a containerd
// sandbox that also offers per-dispatch containers. The wiring's
// containerSession is exactly this condition.
func ContainerSession(cfg *config.Config, isolated bool) bool {
	return isolated && cfg != nil && runtime.GOOS == "linux" &&
		strings.TrimSpace(cfg.Workspace.Containerd.Image) != ""
}

// Mode returns the normalized dispatch mode: "retained" (default) or
// "shared".
func Mode(cfg *config.Config) string {
	if cfg == nil {
		return "retained"
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Agent.SubAgent.Mode), "shared") {
		return "shared"
	}
	return "retained"
}

// RequestedWorld returns the normalized configured dispatch world, with the
// "auto" default applied.
func RequestedWorld(cfg *config.Config) string {
	if cfg == nil {
		return "auto"
	}
	w := strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.World))
	if w == "" {
		return "auto"
	}
	return w
}

// Containment returns the normalized sub-agent placement, with the "auto"
// default applied.
func Containment(cfg *config.Config) string {
	if cfg == nil {
		return "auto"
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.Containment)) {
	case "", "auto":
		return "auto"
	case "off", "none", "advisory":
		return "off"
	case "paddock", "jail":
		return "paddock"
	default:
		return strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.Containment))
	}
}

// Network returns the normalized egress posture, with the "allow_all"
// default applied.
func Network(cfg *config.Config) string {
	if cfg == nil {
		return "allow_all"
	}
	n := strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.Network))
	if n == "" {
		return "allow_all"
	}
	return n
}

// JailAvailable reports whether this host can actually establish a syscall
// jail. Availability, not presence: a compiled-in-but-disabled LSM degrades.
// It is the one probe shared by doctor and every UI surface.
func JailAvailable() bool {
	backend := jail.Backend()
	if backend == nil {
		return false
	}
	ok, _ := backend.Available()
	return ok
}

// worldOrUnavailable renders a lane's resolution, naming an illegal request
// rather than silently showing another world.
func worldOrUnavailable(res subagent.WorldResolution, err error) string {
	if err != nil {
		return "unavailable"
	}
	return string(res.Resolved)
}

// backendOf applies the local default to the configured workspace backend.
func backendOf(cfg *config.Config) string {
	b := strings.ToLower(strings.TrimSpace(cfg.Workspace.Backend))
	if b == "" {
		return config.BackendLocal
	}
	return b
}

// rootOf picks the containment root the session actually runs under: the
// in-sandbox root when isolated, the --workspace path otherwise. Empty means
// unrestricted.
func rootOf(cfg *config.Config, workspaceRoot string, isolated bool) string {
	if !isolated {
		return strings.TrimSpace(workspaceRoot)
	}
	r := strings.TrimSpace(cfg.Workspace.Root)
	if r == "" {
		// Load normalizes this; a hand-built config still gets the
		// documented default rather than an empty root.
		return "/workspace"
	}
	return r
}
