package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/goatherd-runtime/sandbox/containment"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jail"
	"github.com/buchenberg/yaah/internal/tools"
)

// recordConfinementFact appends the resolved jail placement to a sub-agent's
// scope (Plan 7 Phase 3), so a reader can tell a jailed run from an advisory one.
// Best-effort: a tracing failure must not fail the dispatch. It is a no-op when
// either argument is nil (tracing off, or no confinement to record).
func recordConfinementFact(ctx context.Context, scope *goatherd.Scope, conf *subAgentConfinement) {
	if scope == nil || conf == nil {
		return
	}
	if _, err := scope.AppendFact(ctx, "yaah.subagent.confinement.v1", "confinement", conf.payload()); err != nil {
		slog.Debug("confinement fact: append failed", "scope", scope.ID(), "err", err)
	}
}

// subAgentConfinement records how one dispatch resolved its jail placement, for
// the sub-agent's trace (Plan 7 Phase 3): a reader can tell a jailed run from an
// advisory one, and see the single writable root the kernel bounded it to.
type subAgentConfinement struct {
	Requested string // the configured agents.subagent.containment value
	Resolved  string // "paddock" (jailed) or "advisory" (host path)
	Backend   string // backend name when jailed (e.g. "landlock", "winacl")
	Tier      string // enforcement tier, e.g. "native-syscall-deny"
	Enforced  bool   // a usable backend will actually establish the jail
	Writable  string // the bound world — the sole writable root
	Network   string // the egress posture that rode the jail ("allow_all", …)
	Broker    int    // the pinned egress port, when the posture is "broker"
	// ws is the workspace whose jail this describes. Kept so the post-run check
	// can ask what the lane actually had, rather than what it hoped for.
	ws tools.Workspace
}

func (c *subAgentConfinement) payload() map[string]any {
	return map[string]any{
		"requested":     c.Requested,
		"resolved":      c.Resolved,
		"backend":       c.Backend,
		"tier":          c.Tier,
		"enforced":      c.Enforced,
		"writable_root": c.Writable,
		"network":       c.Network,
		"broker_port":   c.Broker,
	}
}

// degraded reports whether the jail this record describes was lost at run time —
// the backend passed its availability check but could not establish containment —
// and why. It is false for a nil workspace (an advisory dispatch never had one).
func (c *subAgentConfinement) degraded() (string, bool) {
	if c == nil || c.ws == nil {
		return "", false
	}
	return tools.WorkspaceDegraded(c.ws)
}

// recordConfinementDegradedFact appends the post-run truth when a dispatch that
// resolved to a jail ended up running unconfined. The pre-run fact says what was
// resolved; this one says what the body actually ran under, so a reader never
// mistakes a degraded lane for a jailed one.
func recordConfinementDegradedFact(ctx context.Context, scope *goatherd.Scope, conf *subAgentConfinement) {
	if scope == nil || conf == nil {
		return
	}
	reason, ok := conf.degraded()
	if !ok {
		return
	}
	payload := conf.payload()
	payload["resolved"] = "advisory"
	payload["enforced"] = false
	payload["degraded_reason"] = reason
	if _, err := scope.AppendFact(ctx, "yaah.subagent.confinement.degraded.v1", "confinement_degraded", payload); err != nil {
		slog.Debug("confinement degraded fact: append failed", "scope", scope.ID(), "err", err)
	}
}

// resolveSubAgentConfinement builds the syscall-jailed workspace for a sub-agent
// workdir dispatch when agents.subagent.containment asks for one, and describes
// what it resolved to (for the trace). It returns a nil workspace to leave the
// host path. "" / "auto" (the default) jail on the platform backend (Landlock
// on Linux, winacl on Windows) and degrade to advisory elsewhere; "off" (or
// "none"/"advisory") mean no jail;
// "paddock" (alias "jail") fails closed. The writable root is the worktree — the
// sub-agent's bound world — so the kernel boundary and the run's own boundary
// agree.
func resolveSubAgentConfinement(pv *tools.PathValidator, cfg config.SubAgentConfig, role subagent.SubAgentRole, workdir string, network confinement.NetworkPolicy) (tools.Workspace, *subAgentConfinement, error) {
	requested := strings.TrimSpace(cfg.Containment)
	placement := containmentPlacement(requested)

	// Normalize the zero policy to the config default before anything reads it:
	// an unset caller means "no posture configured", and handing an empty mode to
	// the runtime would mean deny_all — a jail that refuses every connect because
	// nobody set a key. The broker port goes with the mode: a port on a non-broker
	// posture is a record saying two things at once.
	if network.Mode == "" {
		network.Mode = confinement.NetAllowAll
	}
	if network.Mode != confinement.NetBroker {
		network.BrokerPort = 0
	}

	// Availability, not mere presence: a backend that is present but unusable
	// (an LSM that is compiled in but disabled, say) means Exec will degrade.
	backend := jail.Backend()
	available := false
	if backend != nil {
		if ok, _ := backend.Available(); ok {
			available = true
		}
	}

	// Resolve the declared grants before any early return: a restrictive set
	// is unenforceable without a jail, and `containment: off` must refuse it
	// rather than run unconfined with the whole host writable.
	grantSet := cfg.Grants
	if rc, ok := cfg.Roles[string(role)]; ok && len(rc.Grants) > 0 {
		grantSet = rc.Grants
	}
	var grants tools.DispatchGrants
	if workdir != "" {
		var gerr error
		grants, gerr = tools.ResolveGrants(grantSet, workdir)
		if gerr != nil {
			return nil, nil, gerr
		}
	}
	if grants.Restrictive() && placement == "" {
		return nil, nil, fmt.Errorf("grants: restrictive grants need a paddock, but containment is %q — set containment: auto or paddock", requested)
	}

	if placement == "" || workdir == "" {
		return nil, nil, nil
	}

	if grants.Restrictive() && !available && placement != containment.PlacementPaddock {
		return nil, nil, fmt.Errorf("grants: restrictive grants need a paddock (syscall-deny), which this host cannot establish")
	}

	rec, confine := confinementRecord(requested, placement, available, backend, workdir, network)
	if grants.Restrictive() && rec != nil {
		rec.Writable = strings.Join(grants.Writable, ",")
	}
	if !confine {
		// "auto" degrades visibly to advisory: the host path *is* advisory.
		return nil, rec, nil
	}

	// The write jail, with the configured egress posture. A restrictive posture
	// the host cannot enforce fails closed inside the backend, so it is surfaced
	// rather than silently downgraded.
	ws := tools.NewConfinedLocalWorkspace(pv, backend, placement, network)
	if grants.Restrictive() {
		// Only a declared set narrows the roots; an undeclared set keeps the
		// legacy single-root workspace (and its hint path). A declared
		// all-readonly set passes an empty list, which WithWritableRoots
		// treats as "nothing writable".
		ws = ws.WithWritableRoots(grants.Writable)
	}

	// Redirect housekeeping writes into the worktree (the one writable root):
	// HOME, TMPDIR, and the build caches. The scratch is scrubbed before the
	// workspace is sealed, so it never reaches a proposal's delta.
	//
	// A failure here fails the dispatch. The redirect is not decoration: without
	// it the jailed body's HOME/TMPDIR writes land outside the writable root and
	// are denied, so the run would fail confusingly — and dropping the jail
	// instead would silently lose containment. The refusal is therefore surfaced
	// rather than downgraded. (An operator who wants no jail sets
	// `containment: off`.)
	env, err := tools.ConfinementScratchEnv(workdir)
	if err != nil {
		return nil, nil, fmt.Errorf("confinement redirect for %s: %w", workdir, err)
	}
	env = append(env, brokerProxyEnv(network)...)
	final := ws.WithEnv(env)
	rec.ws = final
	return final, rec, nil
}

// confinementRecord is the pure decision behind what a dispatch records, split
// out (like doctor's containmentCheck) so the two cases a live backend cannot
// produce are testable: a backend that is present but unusable with "auto"
// (which must record advisory, NOT the paddock it will not enter), and the same
// with "paddock" (which must record paddock with Enforced false — the dispatch
// refuses rather than running unconfined, so "advisory" would be a lie).
//
// confine reports whether a confined workspace should be built: true whenever
// the placement is paddock (so Exec can fail closed) or a usable backend exists.
func confinementRecord(requested string, placement containment.Placement, available bool, backend containment.Backend, workdir string, network confinement.NetworkPolicy) (*subAgentConfinement, bool) {
	if !available && placement != containment.PlacementPaddock {
		// No writable root is claimed — nothing bounded it.
		return &subAgentConfinement{Requested: requested, Resolved: "advisory"}, false
	}
	rec := &subAgentConfinement{Requested: requested, Resolved: "paddock", Writable: workdir}
	if available && backend != nil {
		// An egress posture rides the jail, so it is recorded with it. An
		// advisory run has no jail to put a rule in, and its record says nothing
		// about the network rather than claiming one.
		rec.Network = networkMode(network)
		rec.Broker = network.BrokerPort
		rec.Enforced = true
		rec.Backend = backend.Name()
		rec.Tier = backend.EnforcementTier()
	}
	return rec, true
}

// SubAgentNetworkPolicy lowers agents.subagent.network to the confinement policy
// a confined dispatch carries. Exported because the composition root owns the
// broker and therefore knows its port, while the mapping belongs next to the
// code that applies it — two copies drifting is exactly how a config key quietly
// stops meaning what it says. The broker port is required for "broker": a policy
// that cannot be applied is not applied quietly.
func SubAgentNetworkPolicy(mode string, brokerPort int) (confinement.NetworkPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "allow_all":
		return confinement.NetworkPolicy{Mode: confinement.NetAllowAll}, nil
	case "deny_all":
		return confinement.NetworkPolicy{Mode: confinement.NetDenyAll}, nil
	case "broker":
		if brokerPort <= 0 {
			return confinement.NetworkPolicy{}, fmt.Errorf(
				"agents.subagent.network is %q but no egress broker is running", mode)
		}
		return confinement.NetworkPolicy{Mode: confinement.NetBroker, BrokerPort: brokerPort}, nil
	default:
		return confinement.NetworkPolicy{}, fmt.Errorf(
			"agents.subagent.network %q is not one of: allow_all, deny_all, broker", mode)
	}
}

// restrictivePosture reports whether a policy denies or brokers egress — the
// modes whose rules a dispatch has to be jailed for. It reads the posture, not
// whether a jail was established: a refused jail still must not leave the
// host-network tools registered.
func restrictivePosture(network confinement.NetworkPolicy) bool {
	mode := networkMode(network)
	return mode != "" && mode != string(confinement.NetAllowAll)
}

// hostNetworkToolsIn lists the profile's tools that would reach the network from
// this process — omitted from a restrictive dispatch's registry, and named in its
// prompt so the omission is legible.
func hostNetworkToolsIn(profile subagent.RoleProfile) []string {
	var out []string
	for _, name := range profile.Tools {
		if tools.LeafToolUsesHostNetwork(name) {
			out = append(out, name)
		}
	}
	return out
}

// requireEnforceableEgress refuses a restrictive posture this host cannot apply.
//
// "allow_all" needs nothing. "deny_all" and "broker" are kernel rules: they need
// a usable backend AND its network support (Landlock ABI >= 4 on Linux; winacl
// has no network axis). Without both, the
// dispatch would either fail deep inside the jail with a kernel message, or — far
// worse on the advisory fallback — run with open egress while the config, the
// trace and the sub-agent's own prompt said otherwise.
func requireEnforceableEgress(available bool, backend containment.Backend, network confinement.NetworkPolicy, placement containment.Placement, workdir, requested string) error {
	mode := networkMode(network)
	if mode == string(confinement.NetAllowAll) {
		return nil
	}
	// "This dispatch is not jailed at all" is the primary reason the posture
	// cannot be applied, and it is checked here rather than left to an early
	// return — a dropped posture is a rule the user believes is in force.
	if workdir == "" {
		return fmt.Errorf(
			"agents.subagent.network is %q, but this dispatch has no workdir to confine against, so the posture cannot be applied: "+
				"set network: allow_all to accept open egress", mode)
	}
	if placement == "" {
		configured := requested
		if configured == "" {
			configured = "off"
		}
		return fmt.Errorf(
			"agents.subagent.network is %q, which needs a paddock, but agents.subagent.containment is %q: "+
				"set containment: auto (or paddock) to enforce it, or network: allow_all to accept open egress",
			mode, configured)
	}
	if placement == containment.PlacementPaddock && !available {
		// paddock already fails closed at Exec; let that message stand.
		return nil
	}
	if !available {
		return fmt.Errorf(
			"agents.subagent.network is %q, which needs a paddock, but this host has no usable paddock backend: "+
				"the dispatch would run with open egress. Set network: allow_all to accept that, or run where confinement is available",
			mode)
	}
	if !backend.SupportsNetwork(network.Mode) {
		return fmt.Errorf(
			"agents.subagent.network is %q, which this host's paddock cannot enforce (no network-rule support on this backend: Landlock needs ABI >= 4 on Linux, winacl has none): "+
				"set network: allow_all, or remove the paddock requirement", mode)
	}
	return nil
}

// brokerProxyEnv routes well-behaved clients through the broker. A pin without
// routing is half a control: the jail would permit only the broker's port while
// every ordinary HTTPS fetch still went straight to :443 and was refused — so the
// broker is advertised the way HTTP proxies are advertised, and clients that
// honour the convention (curl, git-http, npm, pip, Go's net/http) reach it
// without being told twice.
func brokerProxyEnv(network confinement.NetworkPolicy) []string {
	if network.Mode != confinement.NetBroker || network.BrokerPort <= 0 {
		return nil
	}
	addr := fmt.Sprintf("http://127.0.0.1:%d", network.BrokerPort)
	return []string{
		"HTTP_PROXY=" + addr,
		"HTTPS_PROXY=" + addr,
		"http_proxy=" + addr,
		"https_proxy=" + addr,
		// The broker itself is on loopback; never proxy the loopback hop.
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
	}
}

// networkMode names a policy's posture for the trace and the prompt. It is read
// after resolveSubAgentConfinement has normalized an unset mode, so it reports the
// posture actually applied rather than a default nobody chose.
func networkMode(np confinement.NetworkPolicy) string {
	if np.Mode == "" {
		return string(confinement.NetAllowAll)
	}
	return string(np.Mode)
}

// containmentPlacement maps agents.subagent.containment to a placement, or "" for
// "run on the host" (the explicit opt-out: "off"/"none"/"advisory"). An unset
// setting is the default and means auto.
func containmentPlacement(v string) containment.Placement {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "auto":
		// "auto" is the default (Plan 7 Phase 3): jail where the host can, and
		// degrade visibly to advisory where it cannot. The opt-out is explicit
		// — "off" (or "none"/"advisory") — so the unconfined host path is a
		// choice, not an omission.
		return containment.PlacementAuto
	case "paddock", "jail":
		return containment.PlacementPaddock
	default: // "off", "none", "advisory"
		return ""
	}
}
