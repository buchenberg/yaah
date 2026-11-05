package runner

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/jail"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
)

// buildSubAgentRegistry builds the tool registry for a sub-agent from
// its role profile. If the profile includes the task tool and
// remainingDepth > 0, a nested DelegateTool is registered so the sub-agent
// can spawn further workers. When remainingDepth == 0 the task tool is
// omitted entirely.
//
// An isolated session (opts.workspace set) installs the session's sandbox
// workspace — the sub-agent inherits the container (decision D2) — and
// skips any host-only tool the profile names; a SetWorkspace refusal
// surfaces as an error rather than a silently host-side sub-agent.
// subAgentUnavailableTools names the tools a role asked for that this
// environment cannot offer: a shell the machine does not have, or Go
// toolchain machinery in a workspace that is not a Go module.
//
// The role file states what the role needs; the environment decides what
// is there. Shipping a schema for a tool that cannot run costs tokens on
// every call and buys a failed call later, so the tool is left out and
// named in the prompt instead (yaah-9n0).
func subAgentUnavailableTools(opts taskRunnerOpts, profile subagent.RoleProfile, workdir string) []string {
	// The root of the dispatch, in the same order the rest of this file
	// uses: the workspace decides (in the isolated lane the workdir is
	// meaningless), then the assigned worktree, then the process cwd the
	// shared lane runs in.
	// The Go check needs a HOST path: the workspace's workdir is a
	// container path ("/workspace"), which the host cannot stat. The
	// container holds a copy of the session's project, so the project
	// root is the right proxy — the assigned worktree in the local
	// lane, the process cwd in the isolated one (where the workdir is
	// meaningless).
	root := workdir
	if opts.workspace != nil {
		root = ""
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	goAvailable := tools.GoToolAvailable(root)

	var unavailable []string
	for _, name := range profile.Tools {
		switch name {
		case "bash":
			// The local lane runs the shell on this machine, so the
			// host decides. The isolated lane delegates to the
			// workspace, which runs commands in the container.
			if opts.workspace == nil && !tools.ShellToolAvailable(name) {
				unavailable = append(unavailable, name)
			}
		case "powershell":
			// There is no PowerShell inside the Linux sandbox — the
			// tool itself refuses there — so the isolated lane must
			// not advertise it. The local lane needs one on PATH.
			if opts.workspace != nil || !tools.ShellToolAvailable(name) {
				unavailable = append(unavailable, name)
			}
		default:
			if tools.IsGoToolchainTool(name) && !goAvailable {
				unavailable = append(unavailable, name)
			}
		}
	}
	return unavailable
}

func buildSubAgentRegistry(opts taskRunnerOpts, profile subagent.RoleProfile, role subagent.SubAgentRole, remainingDepth int, workdir string) (*tools.Registry, *subAgentConfinement, error) {
	resolveTimeout := subAgentTimeoutResolver(opts.subCfg)
	registerTask := func(reg *tools.Registry) {
		reg.Register(&tools.DelegateTool{
			Runner:         makeTaskRunner(opts, remainingDepth-1),
			ResolveTimeout: resolveTimeout,
		})
	}

	wrapWithTracker := func(t tools.Tool) tools.Tool {
		if opts.tracker != nil {
			return tools.NewRecordingTool(t, opts.tracker)
		}
		return t
	}

	destructiveTools := map[string]bool{
		"write":      true,
		"edit":       true,
		"delete":     true,
		"rmdir":      true,
		"move":       true,
		"copy":       true,
		"bash":       true,
		"powershell": true,
	}

	registerLeaf := func(reg *tools.Registry, name string) bool {
		if t := tools.NewLeafTool(name); t != nil {
			if destructiveTools[name] {
				reg.Register(wrapWithTracker(t))
			} else {
				reg.Register(t)
			}
			return true
		}
		return false
	}

	// The egress requirement is a property of the DISPATCH, not of the lane that
	// happens to jail it, so it is decided here — before any lane gate. A
	// workdir-less dispatch (a shared-mode spawn, a model-supplied empty workdir)
	// never reaches the confinement path, and leaving the check inside it dropped
	// the posture silently and left the host-network tools registered.
	//
	// The isolated lane is exempt: its egress is the container's posture, which
	// this key does not govern (documented in the config reference).
	if opts.workspace == nil {
		hostBackend := jail.Backend()
		hostAvailable := false
		if hostBackend != nil {
			if ok, _ := hostBackend.Available(); ok {
				hostAvailable = true
			}
		}
		if err := requireEnforceableEgress(hostAvailable, hostBackend, opts.subNetwork,
			containmentPlacement(opts.subCfg.Containment), workdir, strings.TrimSpace(opts.subCfg.Containment)); err != nil {
			return nil, nil, err
		}
	}

	reg := tools.NewEmptyRegistry()
	var conf *subAgentConfinement
	isolated := opts.workspace != nil
	if !isolated && (opts.pathValidator != nil || workdir != "") {
		// An isolated sub-agent gets a validator rooted at its own checkout, so
		// its file tools are contained there and its shell runs there. Policy
		// (deny patterns, home-access, approval callback) is inherited; the
		// derived validator starts with a fresh approval cache.
		//
		// A workdir dispatch installs a rooted validator even when the session
		// carries none (the default local mode): without it params.Workdir
		// never reaches the tools, file paths and the shell resolve against
		// the process cwd, and a supervised session's fork variants all write
		// the ONE main tree — worktrees stay pristine, sealed deltas come out
		// empty, and choose has nothing to apply (#312/#318).
		pv := opts.pathValidator
		if workdir != "" {
			// An unrestricted *session* validator must not widen the
			// worktree lane. AllowHomeAccess=true on a root-less session
			// validator (cmd/yaah wiring for the default local mode)
			// encodes the legacy unrestricted posture of the main tree,
			// not a policy a sub-agent should inherit — historically the
			// session carried no validator at all here, and
			// nil.WithWorkspaceRoot derived a fresh, home-restricted
			// validator. Containment roots and deny patterns (a real
			// constrained session) still inherit.
			if pv != nil && pv.WorkspaceRoot == "" &&
				pv.AllowHomeAccess && len(pv.DenyPatterns) == 0 {
				pv = nil
			}
			pv = pv.WithWorkspaceRoot(workdir)
		}
		reg.SetPathValidator(pv)

		// Confinement seam (Plan 7 Phase 1): on the sub-agent workdir lane the
		// sub-agent's shell runs under the syscall jail when configured, with
		// the worktree as its sole writable root. Guarded on workdir: only that
		// lane has a bound world to jail against. The orchestrator's workspace
		// and a workdir-less (shared-host) sub-agent keep running unconfined.
		if workdir != "" {
			ws, rec, cerr := resolveSubAgentConfinement(pv, opts.subCfg, role, workdir, opts.subNetwork)
			if cerr != nil {
				return nil, nil, cerr
			}
			// Record the resolution even when there is no workspace: "auto"
			// degrading to advisory returns a nil workspace WITH a record, and
			// that advisory resolution is exactly what the trace must show.
			conf = rec
			if ws != nil {
				if err := reg.SetWorkspace(ws); err != nil {
					slog.Debug("sub-agent confinement unavailable", "workdir", workdir, "err", err)
					conf = nil
				}
			}
		}
	}
	// The jail governs the processes a dispatch starts, and these tools make
	// their requests in THIS process — so a restrictive egress posture omits
	// them rather than leaving a hole in it. This applies to both host and
	// container lanes: the container netns covers its shell children, not the
	// HTTP clients running in yaah's own process. The prompt names the omission
	// so the sub-agent does not read a missing tool as a broken profile.
	restrictiveEgress := restrictivePosture(opts.subNetwork)

	// The tools this environment cannot offer are left out (yaah-9n0).
	unavailable := subAgentUnavailableTools(opts, profile, workdir)
	unavailableSet := make(map[string]bool, len(unavailable))
	for _, name := range unavailable {
		unavailableSet[name] = true
	}

	for _, name := range profile.Tools {
		if name == "delegate" {
			if remainingDepth > 0 {
				registerTask(reg)
			}
			continue
		}
		if unavailableSet[name] {
			continue
		}
		if restrictiveEgress && tools.LeafToolUsesHostNetwork(name) {
			continue
		}
		if isolated && tools.LeafToolIsHostOnly(name) {
			// The role profile names a host-only tool; isolated mode
			// omits it for the same reason the curated session
			// registry does. The system prompt names the omission so
			// the sub-agent does not retry a call that cannot succeed.
			continue
		}
		// DB-backed tools are not in leafTools (they need the session's
		// memory store), so a role that declares them would otherwise get
		// them silently dropped. Register them here from the shared store:
		// memory_add/memory_search/memory_update are what the consolidator
		// role uses to write, dedupe, and merge durable atoms.
		switch name {
		case "memory_add":
			if opts.db != nil {
				// Tag the write with the parent session so the atoms this
				// sub-agent distills are attributable to the conversation
				// that spawned it: the session-close pruning pass deletes
				// session-scoped atoms by session, and an unlinked atom
				// would outlive its session forever.
				reg.Register(&tools.MemoryAddTool{DB: opts.db, SessionID: opts.parentSession})
				reg.Register(&tools.MemoryPromoteTool{DB: opts.db, SessionID: opts.parentSession})
			}
			continue
		case "memory_search":
			if opts.db != nil {
				scope := ""
				if profile.DisplayName == "consolidator" {
					// The consolidator writes session-tier atoms; let it recall
					// them so it can dedupe against what it already distilled
					// this session.
					scope = memory.ScopeSession
				}
				reg.Register(&tools.MemorySearchTool{DB: opts.db, Scope: scope})
			}
			continue
		case "memory_update":
			if opts.db != nil && profile.DisplayName == "consolidator" {
				// Only the consolidator merges memory today; it updates the
				// session-tier atoms it already distilled rather than
				// re-adding near-duplicates. Restrict to session scope AND
				// the parent session ID so it cannot rewrite global or
				// other-session atoms.
				reg.Register(&tools.MemoryUpdateTool{DB: opts.db, Scope: memory.ScopeSession, SessionID: opts.parentSession})
			}
			continue
		}
		if !registerLeaf(reg, name) {
			// A role file names a tool this harness does not have (a typo, a
			// renamed tool, or a session-only tool). It is dropped rather than
			// failing the dispatch, but say so: silently shipping a role
			// without the tool it asked for is how a belt drifts for months.
			slog.Warn("sub-agent role names an unknown tool; it was not registered",
				"role", profile.Specialty, "tool", name)
		}
	}
	if isolated {
		// Isolated session (plan §1.3, decision D2): the sub-agent
		// inherits the session's sandbox workspace, so its file tools
		// and shell run inside the container instead of on the host.
		// SetWorkspace runs after registration — the same ordering as
		// the session registry in the wiring — so the gate validates
		// the tool set the sub-agent actually gets: a profile that
		// named an unmigrated or host-only tool that slipped past the
		// filter fails the dispatch loudly instead of silently
		// running that tool host-side. workdir has no meaning here —
		// the container's root is the workspace — so it is ignored
		// rather than remapped.
		if serr := reg.SetWorkspace(opts.workspace); serr != nil {
			return nil, nil, fmt.Errorf("isolated sub-agent workspace: %w", serr)
		}
	}
	return reg, conf, nil
}
