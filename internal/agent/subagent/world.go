// Package subagent owns the dispatch-world ladder (yaah-ihfh.3): which
// substrate a dispatch runs in, and whether stepping down is legal.
//
// The reference model separates the world (where work lives) from the
// authority (what bounds it). A dispatch world is therefore a pure placement
// decision — it creates nothing. Resolution here answers only "which world,
// and is the fallback honest?"; the retained lane owns provisioning.
//
// Degradation is requirement-preserving: a fallback is legal only when it
// still enforces everything the dispatch declared. See
// docs/shepherd-parity.md §Requirement-preserving degradation.
package subagent

import (
	"errors"
	"fmt"
	"strings"
)

// WorldKind identifies a dispatch world. The empty string and "auto" are
// requests, never resolutions.
type WorldKind string

const (
	WorldAuto      WorldKind = "auto"
	WorldWorktree  WorldKind = "worktree"
	WorldContainer WorldKind = "container"
	// WorldShared is a resolution only: a shared-lane dispatch has no world.
	WorldShared WorldKind = "shared"
)

// WorldRequest is what the dispatch asked for.
type WorldRequest struct {
	// Requested is the configured world: "auto", "worktree", or "container".
	// Empty means auto.
	Requested string

	// Lane is the dispatch lane: "proposal", "report", or "shared".
	Lane string

	// Retained reports whether the dispatch must produce a settleable output.
	Retained bool

	// Containment is agents.subagent.containment (auto / off / paddock).
	Containment string

	// Network is agents.subagent.network (allow_all / deny_all / broker).
	Network string

	// RestrictiveGrants reports whether the dispatch declares grants a
	// permissive world cannot enforce. False until per-binding grants land.
	RestrictiveGrants bool
}

// WorldAvailability is what this host and build can actually provide.
type WorldAvailability struct {
	// Container reports that a per-dispatch container world can be created
	// right now (the build implements it and the host can provide it).
	Container bool
	// ContainerOffered reports that this build implements per-dispatch
	// container worlds at all. It separates "not shipped yet" (auto falls back
	// silently, because nothing was promised) from "shipped but this host
	// cannot provide it" (auto degrades with a warning).
	ContainerOffered bool
	// ContainerRetains reports that a container world can be sealed and
	// settled. False until yaah's container settlement path (git parent ↔
	// croft child) is wired; capture itself is digest-stable since croft-dlb.
	ContainerRetains bool
	// ContainerReason explains Container=false.
	ContainerReason string

	// Worktree reports that a per-dispatch git worktree can be created.
	Worktree bool
	// WorktreeReason explains Worktree=false.
	WorktreeReason string

	// Jail reports that a syscall jail is usable on this host.
	Jail bool
}

// WorldResolution is the recorded outcome.
type WorldResolution struct {
	Requested WorldKind
	Resolved  WorldKind
	Degraded  bool
	Reason    string
}

// ErrWorldUnavailable is returned when no legal world satisfies the request.
// The message names the requirement that ruled out each candidate.
var ErrWorldUnavailable = errors.New("no legal dispatch world")

// ResolveWorld picks the dispatch world. It is pure over its inputs so the
// ladder and the requirement rules are testable without a daemon or a repo.
func ResolveWorld(req WorldRequest, av WorldAvailability) (WorldResolution, error) {
	requested := normalizeKind(req.Requested)
	if requested == "" {
		requested = WorldAuto
	}
	if requested != WorldAuto && requested != WorldWorktree && requested != WorldContainer {
		return WorldResolution{}, fmt.Errorf("unknown dispatch world %q (want auto, worktree, or container)", req.Requested)
	}

	res := WorldResolution{Requested: requested}

	// A shared dispatch runs on the session's tree by definition: there is no
	// world to fork. Asking for one is a configuration contradiction, not a
	// degradation.
	if req.Lane == "shared" {
		if requested == WorldContainer {
			return res, fmt.Errorf("%w: agents.subagent.mode shared has no world — container is unreachable for a shared-lane role", ErrWorldUnavailable)
		}
		res.Resolved = WorldShared
		return res, nil
	}

	containerWhy := containerRefusal(req, av)
	worktreeWhy := worktreeRefusal(req, av)

	switch requested {
	case WorldContainer:
		if containerWhy == "" {
			res.Resolved = WorldContainer
			return res, nil
		}
		return res, fmt.Errorf("%w: container required, but %s", ErrWorldUnavailable, containerWhy)

	case WorldWorktree:
		if worktreeWhy == "" {
			res.Resolved = WorldWorktree
			return res, nil
		}
		return res, fmt.Errorf("%w: worktree required, but %s", ErrWorldUnavailable, worktreeWhy)

	default: // auto — best available world, degradation recorded
		if containerWhy == "" {
			res.Resolved = WorldContainer
			return res, nil
		}
		if worktreeWhy == "" {
			res.Resolved = WorldWorktree
			// A build that does not offer container worlds promised nothing,
			// so falling back to the worktree is not a degradation — warning
			// on every dispatch would be noise. Once the lane ships, a host
			// that cannot provide it is a real, recorded degradation.
			if av.ContainerOffered {
				res.Degraded = true
				res.Reason = "container world unavailable: " + containerWhy
			}
			return res, nil
		}
		return res, fmt.Errorf("%w: container: %s; worktree: %s", ErrWorldUnavailable, containerWhy, worktreeWhy)
	}
}

// containerRefusal returns why a container world is illegal for this request,
// or "" when it is legal.
func containerRefusal(req WorldRequest, av WorldAvailability) string {
	if !av.Container {
		if av.ContainerReason != "" {
			return av.ContainerReason
		}
		return "container worlds are unavailable on this host"
	}
	if req.Retained && !av.ContainerRetains {
		return "container settlement is not wired yet: the parent world is git and the dispatch world is croft"
	}
	// Restrictive grants are enforceable in a container only once the world
	// adapter can lower them to mounts/roots. Until then the honest answer is
	// that the container cannot promise the declared authority.
	if req.RestrictiveGrants {
		return "per-binding grants are not lowered onto container worlds yet"
	}
	return ""
}

// worktreeRefusal returns why a worktree world is illegal for this request, or
// "" when it is legal.
func worktreeRefusal(req WorldRequest, av WorldAvailability) string {
	if !av.Worktree {
		if av.WorktreeReason != "" {
			return av.WorktreeReason
		}
		return "a per-dispatch worktree is unavailable in this session"
	}
	if req.RestrictiveGrants && !av.Jail {
		return "restrictive grants need the syscall jail, which this host cannot establish"
	}
	if normalizeContainment(req.Containment) == "paddock" && !av.Jail {
		return "containment is paddock (fail-closed) but this host cannot jail"
	}
	if restrictiveNetwork(req.Network) && !av.Jail {
		return "a restrictive network posture needs the syscall jail, which this host cannot establish"
	}
	return ""
}

func normalizeKind(s string) WorldKind {
	return WorldKind(strings.ToLower(strings.TrimSpace(s)))
}

func normalizeContainment(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return "auto"
	case "off", "none", "advisory":
		return "off"
	case "paddock", "jail":
		return "paddock"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func restrictiveNetwork(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "allow_all":
		return false
	default:
		return true
	}
}
