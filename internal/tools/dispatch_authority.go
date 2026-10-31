package tools

import (
	"fmt"
	"path"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

// dispatchAuthority is the container form of a dispatch's declared authority:
// writable roots, whether the workspace is masked read-only, and the network
// posture. It is resolved once per container dispatch from the same grants and
// network config the worktree lane uses, so the two worlds enforce one
// declaration.
//
// A zero authority means "no restriction beyond the container boundary": the
// whole workspace stays writable and the backend keeps its network default.
// That is the historical posture, not an all-read-only one.
type dispatchAuthority struct {
	writableRoots []string
	maskWorkspace bool
	network       string
	brokerHosts   []string
}

// shaped reports whether the workspace needs mount shaping. A read-only mask
// with no writable roots is a valid shape; empty WritableRoots without the mask
// is the unshaped legacy view.
func (a dispatchAuthority) shaped() bool {
	return a.maskWorkspace || len(a.writableRoots) > 0
}

// apply returns a copy of w with the authority lowered onto it. The input is
// the session's immutable workspace stanza; a dispatch must never mutate it.
func (a dispatchAuthority) apply(w config.WorkspaceConfig) config.WorkspaceConfig {
	w.WritableRoots = append([]string(nil), a.writableRoots...)
	w.MaskWorkspace = a.maskWorkspace
	w.Network = a.network
	w.BrokerHosts = append([]string(nil), a.brokerHosts...)
	return w
}

// resolveDispatchAuthority lowers the configured grants and network posture to
// the container's vocabulary. Grants are world-relative: they are resolved
// against the container's Root (for example /workspace), not the host tree.
func resolveDispatchAuthority(rt RetainedRuntime, role string) (dispatchAuthority, error) {
	var a dispatchAuthority

	grants := dispatchGrants(rt, role)
	if len(grants) > 0 {
		worldRoot := "/workspace"
		if rt.DispatchWorlds != nil && rt.DispatchWorlds.Config.Root != "" {
			worldRoot = rt.DispatchWorlds.Config.Root
		}
		resolved, err := resolveContainerGrants(grants, worldRoot)
		if err != nil {
			return dispatchAuthority{}, fmt.Errorf("lower container grants: %w", err)
		}
		a.writableRoots = resolved.Writable
		// A declared set with no writable root is all-read-only. The token
		// is what distinguishes it from the historical unshaped posture.
		a.maskWorkspace = len(resolved.Writable) == 0
	}

	network, hosts, err := lowerContainerNetwork(rt.Network, rt.NetworkHosts)
	if err != nil {
		return dispatchAuthority{}, err
	}
	a.network, a.brokerHosts = network, hosts
	return a, nil
}

// worktreeFallbackAllowed reports whether the worktree world can still enforce
// the dispatch's declared authority after a container preflight failure. It
// asks the same ladder, with the request forced to worktree, so the fallback
// cannot bypass the jail requirement for grants or a restrictive network.
func worktreeFallbackAllowed(rt RetainedRuntime, role string, report bool) bool {
	fallback := rt
	fallback.World = string(subagent.WorldWorktree)
	fallback.WorldForRole = nil
	res, err := resolveDispatchWorld(fallback, role, report)
	return err == nil && res.Resolved == subagent.WorldWorktree
}

// sessionContainerFallbackAllowed reports whether running a report in the
// session container still preserves the dispatch's declared authority. The
// session container is created from the session's workspace stanza, not the
// dispatch's shaped config: it cannot enforce grants or a broker allowlist.
// deny_all is preserved because the session container is loopback-only, and
// allow_all is not a requirement (a stricter world is legal). An all-read-only
// set shapes, so it is refused too.
func sessionContainerFallbackAllowed(rt RetainedRuntime, role string) bool {
	authority, err := resolveDispatchAuthority(rt, role)
	if err != nil {
		return false
	}
	return !authority.shaped() && authority.network != "broker"
}

// dispatchGrants returns the role's grant set, falling back to the session's
// when the role has no override.
func dispatchGrants(rt RetainedRuntime, role string) []config.GrantConfig {
	grants := rt.Grants
	if rt.GrantsForRole != nil {
		if g := rt.GrantsForRole(role); len(g) > 0 {
			grants = g
		}
	}
	return grants
}

// resolveContainerGrants is ResolveGrants for container paths. The container
// root is a POSIX path even when the host is not (the container backend is
// Linux-only), so this must not use filepath's host-specific separators.
func resolveContainerGrants(grants []config.GrantConfig, worldRoot string) (DispatchGrants, error) {
	root := path.Clean(worldRoot)
	if !path.IsAbs(root) {
		return DispatchGrants{}, fmt.Errorf("world root %q is not absolute", worldRoot)
	}
	if len(grants) == 0 {
		return DispatchGrants{Writable: []string{root}}, nil
	}
	g := DispatchGrants{Declared: true}
	for _, grant := range grants {
		rel := path.Clean(strings.TrimSpace(grant.Path))
		if path.IsAbs(rel) {
			return DispatchGrants{}, fmt.Errorf("%s: grants are world-relative, not absolute", grant.Path)
		}
		abs := path.Join(root, rel)
		if abs == root {
			return DispatchGrants{}, fmt.Errorf("%s: a grant may not name the world root; omit grants for whole-world access", grant.Path)
		}
		if !strings.HasPrefix(abs, root+"/") {
			return DispatchGrants{}, fmt.Errorf("%s: escapes the dispatch world", grant.Path)
		}
		switch strings.ToLower(strings.TrimSpace(grant.Access)) {
		case "readwrite":
			g.Writable = append(g.Writable, abs)
		case "readonly":
			g.ReadOnly = append(g.ReadOnly, abs)
		default:
			return DispatchGrants{}, fmt.Errorf("%s: unknown access %q", grant.Path, grant.Access)
		}
	}
	return g, nil
}

// NormalizeBrokerHosts puts allowlist entries into the form both egress
// brokers match: trimmed, lower-cased, and without a trailing dot. A padded or
// upper-cased entry must not behave differently in a container world than it
// does through the host broker.
func NormalizeBrokerHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

// lowerContainerNetwork maps agents.subagent.network to croft's posture
// vocabulary. allow_all is host networking by definition; deny_all is the
// container's own namespace with loopback only; broker is the netns-local
// allowlist proxy. Empty keeps the documented allow_all default.
func lowerContainerNetwork(mode string, hosts []string) (string, []string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "allow_all":
		return "host", nil, nil
	case "deny_all":
		return "none", nil, nil
	case "broker":
		return "broker", NormalizeBrokerHosts(hosts), nil
	default:
		return "", nil, fmt.Errorf("agents.subagent.network %q is not one of: allow_all, deny_all, broker", mode)
	}
}
