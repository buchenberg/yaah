// Package worldinfo derives the session's workspace and dispatch-world
// posture for display: what substrate the session runs on, what bounds a
// sub-agent dispatch, and which world the configured request resolves to on
// this host.
//
// It is also the single source of the dispatch-world availability policy:
// AvailabilityFor builds the record every resolution resolves against, and
// the runtime ladder in internal/tools/retained.go (resolveDispatchWorld)
// calls it rather than carrying its own copy of the reason strings and the
// fallback rules. The derivation here deliberately follows that ladder instead
// of doctor's historical degradation flags: the surfaces must describe what a
// dispatch would actually get, and doctor consumes this package for the same
// reason. The parity test in internal/tools stays as regression armor on the
// inputs each side derives.
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
	// JailName is the containment backend this host provides
	// ("Seatbelt", "Landlock", "WinACL").
	JailName string
	// JailTier is the backend's enforcement tier ("native-syscall-deny").
	JailTier string
	// JailWhy is the backend's availability explanation — most useful when
	// JailOK is false (the refusal reason).
	JailWhy string
	// ContainmentResolved is the effective placement the configured
	// containment setting resolves to on this host: "auto" paddocks when the
	// jail is available and degrades to advisory when it is not, "paddock"
	// resolves to "refused" without one (dispatches fail closed), "off" stays
	// "off".
	ContainmentResolved string
	// Network is the configured egress posture: "allow_all", "deny_all",
	// or "broker" (default applied).
	Network string
	// NetworkHosts is the broker allowlist size; > 0 only when Network is
	// "broker".
	NetworkHosts int
	// WorldReq is the configured dispatch world: "auto", "worktree", or
	// "container" (default applied).
	WorldReq string
	// ProposalWorld is the resolved world for the proposal lane.
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
	jailName, jailTier, jailWhy, jailOK := probeJail()

	s := Status{
		Backend:             backendOf(cfg),
		Isolated:            isolated,
		Root:                rootOf(cfg, workspaceRoot, isolated),
		Image:               strings.TrimSpace(cfg.Workspace.Containerd.Image),
		Mode:                Mode(cfg),
		Containment:         Containment(cfg),
		JailOK:              jailOK,
		JailName:            jailName,
		JailTier:            jailTier,
		JailWhy:             jailWhy,
		ContainmentResolved: resolvedContainment(Containment(cfg), jailOK),
		Network:             Network(cfg),
		WorldReq:            RequestedWorld(cfg),
		ProposalWorld:       worldOrUnavailable(res.Proposal, res.ProposalErr),
		ReportWorld:         worldOrUnavailable(res.Report, res.ReportErr),
	}
	if s.Network == "broker" {
		s.NetworkHosts = len(cfg.Agent.SubAgent.NetworkHosts)
	}

	// The session-level degradation is the proposal lane's (the default
	// lane). A report-lane error only replaces a legitimate shared proposal,
	// so the reason reaches the surfaces instead of leaving "unavailable"
	// unexplained.
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
// mode shared has no world at all; a container session resolves both lanes
// against the sibling-container availability.
func Resolve(cfg *config.Config, isolated bool) Resolution {
	return resolveWithJail(cfg, isolated, JailAvailable())
}

// resolveWithJail is Resolve with the jail probe folded in, so a caller that
// already probed (Derive) can thread its single result through both the
// dispatch-world resolution and the containment posture.
func resolveWithJail(cfg *config.Config, isolated bool, jailOK bool) Resolution {
	if cfg == nil {
		return Resolution{}
	}
	sc := cfg.Agent.SubAgent
	av := availabilityWithJail(cfg, isolated, jailOK)
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

	out.Proposal, out.ProposalErr = resolve("proposal", true)
	out.Report, out.ReportErr = resolve("report", false)
	return out
}

// ContainerSetup is what a session can offer a dispatch as a container world.
// It is the input side of AvailabilityFor: a config-derived surface knows what
// the config and this host allow, the composition root knows which setup it
// actually wired, and both feed the same builder so no surface can describe a
// world a dispatch would not get.
type ContainerSetup struct {
	// Offered reports that a per-dispatch container world can be attempted:
	// this build ships the lane and this host can provide it.
	Offered bool
	// Retains reports that the wired setup can seal and settle a container
	// dispatch.
	Retains bool
	// Session reports that the session itself runs in a container, so a
	// dispatch's world is a sibling container rather than a host worktree.
	Session bool
}

// AvailabilityFor builds the availability record from a container setup and a
// probed jail capability. This is the only place the availability reason
// strings and the retain/worktree rules are written: internal/tools'
// per-dispatch ladder resolves against exactly this record.
func AvailabilityFor(setup ContainerSetup, jailOK bool) subagent.WorldAvailability {

	retainsReason := ""
	if !setup.Retains {
		retainsReason = "container settlement is unavailable in this session: no per-dispatch container setup is wired"
	}
	// A container session has no host worktree: its dispatches run as siblings
	// of the session container instead.
	worktree := !setup.Session
	worktreeReason := ""
	if setup.Session {
		worktreeReason = "a container session has no host worktree; dispatches use a sibling container"
	}
	return subagent.WorldAvailability{
		Container:              setup.Offered,
		ContainerOffered:       setup.Offered,
		ContainerRetains:       setup.Retains,
		ContainerRetainsReason: retainsReason,
		ContainerReason:        "per-dispatch container worlds need workspace.containerd.image on a Linux host, and a wired dispatch setup",
		Worktree:               worktree,
		WorktreeReason:         worktreeReason,
		Jail:                   jailOK,
	}
}

// Availability reports what this host and session can provide, derived from
// the config. A per-dispatch container is offered on Linux with an image
// configured; a container session (isolated + offered) has no host worktree
// but can retain into the session container.
func Availability(cfg *config.Config, isolated bool) subagent.WorldAvailability {
	return availabilityWithJail(cfg, isolated, JailAvailable())
}

// availabilityWithJail is Availability with the jail probe folded in.
func availabilityWithJail(cfg *config.Config, isolated bool, jailOK bool) subagent.WorldAvailability {
	return AvailabilityFor(containerSetup(cfg, isolated), jailOK)
}

// containerSetup derives the container facts this session's config and host
// allow. A wired session both offers and settles a per-dispatch container.
func containerSetup(cfg *config.Config, isolated bool) ContainerSetup {
	offered := containerOffered(cfg)
	return ContainerSetup{
		Offered: offered,
		Retains: offered,
		Session: ContainerSession(cfg, isolated),
	}
}

// ContainerSession reports whether the session runs inside a containerd
// sandbox that also offers per-dispatch containers. The wiring's
// containerSession is exactly this condition.
func ContainerSession(cfg *config.Config, isolated bool) bool {
	return isolated && containerOffered(cfg)
}

// containerOffered reports whether this build and host can create a
// per-dispatch container world at all.
func containerOffered(cfg *config.Config) bool {
	return cfg != nil && runtime.GOOS == "linux" &&
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
	_, _, _, ok := probeJail()
	return ok
}

// probeJail inspects the host's containment backend once for a derived
// status: name and enforcement tier identify what is installed; Available's
// explanation says why it can or cannot run.
func probeJail() (name, tier, why string, ok bool) {
	backend := jail.Backend()
	if backend == nil {
		return "", "", "no paddock backend on this host", false
	}
	name = jailDisplayName(backend.Name())
	tier = backend.EnforcementTier()
	ok, why = backend.Available()
	return name, tier, why, ok
}

// jailDisplayName humanizes a containment backend name for display.
func jailDisplayName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "seatbelt":
		return "Seatbelt"
	case "landlock":
		return "Landlock"
	case "winacl":
		return "WinACL"
	default:
		return name
	}
}

// resolvedContainment maps a normalized containment setting and jail
// availability onto the effective placement at dispatch time, in the same
// vocabulary the dispatch ledger records ("paddock", "advisory"): "auto"
// paddocks when the host can jail and degrades to advisory when it cannot;
// "paddock" refuses without a jail (dispatches fail closed); "off" never
// jails.
func resolvedContainment(setting string, jailOK bool) string {
	switch setting {
	case "off":
		return "off"
	case "paddock":
		if jailOK {
			return "paddock"
		}
		return "refused"
	case "auto", "":
		if jailOK {
			return "paddock"
		}
		return "advisory"
	default:
		return "off"
	}
}

// ContainmentLine renders the effective containment for the resolved dispatch
// world — the mechanism, not the configured setting: "croft" when dispatches
// are contained by the croft container world, else the placement the setting
// resolves to ("paddock", "advisory", "off"), with the fail-closed refusal
// spelled out.
func (s Status) ContainmentLine() string {
	if s.ProposalWorld == "container" {
		return "croft"
	}
	switch s.ContainmentResolved {
	case "":
		return s.Containment
	case "refused":
		return "refused (no paddock on this host — dispatches fail closed)"
	default:
		return s.ContainmentResolved
	}
}

// ContainmentMechanism names the containment mechanism in force for the
// resolved proposal lane by its implementation, always as a single word:
// "Croft" when dispatches are contained by the croft container world, else
// the host jail backend ("WinACL", "Landlock", "Seatbelt") when the paddock
// placement is in force. resolvedContainment only places "paddock" when the
// jail probe passed, so the backend is established on that branch; a backend
// name that is not a single word falls back to the placement word
// "paddock". "Refused" covers the fail-closed placement (paddock requested,
// host cannot jail). Every other placement keeps ContainmentLine's one-word
// vocabulary ("advisory", "off").
func (s Status) ContainmentMechanism() string {
	if s.ProposalWorld == "container" {
		return "Croft"
	}
	if s.ContainmentResolved == "paddock" {
		if fields := strings.Fields(s.JailName); len(fields) == 1 {
			return fields[0]
		}
		return "paddock"
	}
	if s.ContainmentResolved == "refused" {
		return "Refused"
	}
	return s.ContainmentLine()
}

// PaddockLine renders the host paddock ("Seatbelt · native-syscall-deny"),
// returning "" when the resolved world makes it irrelevant: the croft
// container lane carries its own boundary, a substitute for the paddock.
func (s Status) PaddockLine() string {
	if s.ProposalWorld == "container" {
		return ""
	}
	return s.JailLine()
}

// JailLine renders the jail this host provides: backend name and enforcement
// tier when available ("Seatbelt · native-syscall-deny"), the refusal reason
// when not ("unavailable · <why>").
func (s Status) JailLine() string {
	if s.JailOK {
		name := s.JailName
		if name == "" {
			name = "paddock"
		}
		if s.JailTier != "" {
			return name + " · " + s.JailTier
		}
		return name
	}
	if s.JailWhy != "" {
		return "unavailable · " + s.JailWhy
	}
	return "unavailable on this host"
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
