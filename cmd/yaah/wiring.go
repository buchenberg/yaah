package yaah

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/goatherd-runtime/sandbox/egress"

	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/tools"
)

func newAgentSession() (*agentSession, error) {
	return newAgentSessionWithOptions(sessionOptionsFromFlags(), false, false)
}

// defaultWorkspaceDenyPatterns blocks common secret files inside the
// workspace when agents.default.workspace_deny_patterns is unset
// (review finding S6). An explicit config list overrides this; an
// explicit empty list disables deny patterns entirely.
var defaultWorkspaceDenyPatterns = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"id_rsa*",
	"id_ed25519*",
	"id_ecdsa*",
}

// workspaceSandboxStartTimeout bounds the isolated workspace's startup —
// the D5 image probe (its own throwaway sandbox), the session sandbox's
// Create, the D1 copy-in (one backend exec per file), and the baseline
// capture/rebind — so a wedged containerd daemon fails the session at
// startup instead of hanging it. Generous: a first-run image pull and a
// large working tree both happen inside this window.
const workspaceSandboxStartTimeout = 15 * time.Minute

// toolSpillDir is the single source for the tool-result spill directory.
// Both the parent loop (build_loop.go) and every sub-agent loop inherit
// it so oversized tool results land in one place. Sub-agents receive it
// via DelegateToolOpts.ToolSpillDir — internal/agent never reads config
// paths directly (finding C4).
func toolSpillDir() string {
	return filepath.Join(config.HomeDir(), "truncated")
}

// rolefileDirsMu guards the accumulated rolefile search directories.
// rebuildDefaultRoleRegistry appends a session's dirs and rebuilds the
// process-wide role registry as their union, so a later session build
// atomically swaps in a registry that still carries every dir earlier
// sessions contributed (multi-session ACP; a plain
// SetDefaultRoleRegistry per build would silently replace the old one).
var (
	rolefileDirsMu sync.Mutex
	rolefileDirs   []string
)

// rebuildDefaultRoleRegistry accumulates the session's rolefile search
// directories and rebuilds the process-wide role registry as built-ins
// plus the union of every directory seen so far, then swaps that in as
// the default.
func rebuildDefaultRoleRegistry(cwd string) {
	rolefileDirsMu.Lock()
	for _, dir := range runner.RoleSearchPaths(cwd) {
		dup := false
		for _, seen := range rolefileDirs {
			if seen == dir {
				dup = true
				break
			}
		}
		if !dup {
			rolefileDirs = append(rolefileDirs, dir)
		}
	}
	dirs := make([]string, len(rolefileDirs))
	copy(dirs, rolefileDirs)
	rolefileDirsMu.Unlock()

	// Same tolerance as the original per-build loader and the hot-reload
	// path: unreadable/on-missing dirs are skipped.
	reg := subagent.NewRoleRegistry()
	if files := runner.BuiltinRoleFiles(); files != nil {
		_ = reg.LoadBytes(files)
	}
	for _, dir := range dirs {
		// LoadDir itself tolerates missing dirs; other read errors are
		// skipped, as in the original per-build loader.
		_ = reg.LoadDir(dir)
	}
	subagent.SetDefaultRoleRegistry(reg)
}

// canonicalStoredCwd normalizes a session's working directory before it
// is persisted: the same canonicalization (absolute, symlink-resolved)
// the engine-level session registry and its filters apply, so stored
// rows and their filters always agree. Best-effort — an unresolvable
// path is stored as given rather than failing session creation.
func canonicalStoredCwd(cwd string) string {
	return cwdpath.CanonicalStored(cwd)
}

// newAgentSessionWithOptions creates an agent session. All CLI-flag and
// serve-mode inputs arrive via opts — no hidden package state. When
// skipMCP is true, MCP server subprocesses are not started (they consume
// CPU and make tview input sluggish on Windows). When skipOtel is true,
// OTel exporters are not initialised.
//
// The body is the seven construction stages in order (wiring_stages.go:
// loadConfig, buildWorkspace, buildToolRegistry, buildContainment,
// buildMCPSkills, buildLoopInfra, assemble) plus the startup ownership guard
// that releases a partially-built session. Every resource a stage acquires is returned to this
// function rather than cleaned up inside the stage, so the guard can close it
// no matter which later stage failed.
func newAgentSessionWithOptions(opts SessionOptions, skipMCP, skipOtel bool) (*agentSession, error) {
	lc, err := loadConfig(opts, skipOtel)
	if err != nil {
		return nil, err
	}

	// Startup ownership: every resource the stages below create is ours until
	// the returned session takes it over. Any error return must close what was
	// opened — a failed startup that leaves OTel exporters, MCP subprocesses,
	// or a SQLite handle alive accumulates across retries in serve mode.
	// sessionHandedOff flips only on the success path; the closer below runs
	// on every other return.
	//
	// The stage results are declared here (not at their call sites) so the
	// guard can close a partially-built session no matter which later stage
	// failed: a stage assigns its result before it returns, error or not. Each
	// holder carries one startup resource the guard must release —
	// supervisorSvc (adoption plan B1) owns a drain goroutine and a bus
	// subscription, egressBroker (Plan 7 Phase 3) owns a listener and a
	// goroutine, and db is the SQLite handle.
	var (
		ws         *workspaceStage
		toolsStage *toolRegistryStage
		mcpStage   *mcpSkillsStage
		loopStage  *loopInfraStage
	)
	sessionHandedOff := false
	defer func() {
		if sessionHandedOff {
			return
		}
		if loopStage != nil && loopStage.supervisorSvc != nil {
			loopStage.supervisorSvc.Close()
		}
		if loopStage != nil && loopStage.egressBroker != nil {
			loopStage.egressBroker.Stop()
		}
		if lc.otelShutdown != nil {
			lc.otelShutdown(context.Background())
		}
		if mcpStage != nil {
			for _, c := range mcpStage.mcpClients {
				c.Close()
			}
		}
		if toolsStage != nil && toolsStage.db != nil {
			toolsStage.db.Close()
		}
	}()

	ws, err = buildWorkspace(lc.cfg, opts)
	if err != nil {
		return nil, err
	}
	if toolsStage, err = buildToolRegistry(lc.cfg, opts, ws); err != nil {
		return nil, err
	}
	// The containment substrate comes after the registry and its gates: starting
	// a containerd sandbox is the one expensive, externally visible side effect
	// of a session build, so a failed registry or gate build must not create a
	// container only to destroy it. buildContainment installs exactly one
	// substrate and hands it to the registry.
	err = buildContainment(lc.cfg, opts, ws, toolsStage)
	// The sandbox startup bound lives exactly as long as the build did when
	// this was one function body, including the failing-startup path, where the
	// original's defer still cancelled it. Guarded: a (nil, err) return from a
	// stage must not panic here, and ws is non-nil on every path they return.
	if ws != nil {
		defer ws.cancelStart()
	}
	if err != nil {
		return nil, err
	}
	// Every error return below destroys the sandbox instead of leaking a
	// container for the lifetime of the daemon's namespace — bounded, like
	// every other teardown, so a wedged daemon cannot also hang a failed
	// startup on its way out. ws.teardownHandled stands the guard down once
	// the sandbox is destroyed by an error branch (assemble's SetWorkspace
	// gate) or handed to the session (success — close() destroys it), so no
	// error return leaks a container and one failure is never reported on two
	// channels.
	if ws.sessionSandbox != nil {
		defer func() {
			if !ws.teardownHandled {
				destroyCtx, cancelDestroy := context.WithTimeout(context.Background(), workspaceSandboxDestroyTimeout)
				defer cancelDestroy()
				if derr := ws.sessionSandbox.Destroy(destroyCtx); derr != nil {
					fmt.Fprintf(os.Stderr, "warning: workspace sandbox teardown after failed session start: %v\n", derr)
				}
			}
		}()
	}

	if mcpStage, err = buildMCPSkills(lc.cfg, opts, ws, toolsStage, skipMCP); err != nil {
		return nil, err
	}
	if loopStage, err = buildLoopInfra(lc.cfg, opts, lc, ws, toolsStage, mcpStage); err != nil {
		return nil, err
	}
	sess, err := assemble(lc, opts, ws, toolsStage, mcpStage, loopStage)
	if err != nil {
		return nil, err
	}

	// The session owns everything created above from here: close() will
	// destroy the sandbox, and neither startup guard must touch the
	// session's resources again.
	ws.teardownHandled = true
	sessionHandedOff = true

	return sess, nil
}

// supervisorSvcGuardCheck extracts the blocking pre-dispatch check
// (adoption plan B2) from the session's supervisor service. A nil
// service — tracing off, the engine disabled, or a startup that never
// reached the shepherd branch — means no guard: every sub-agent call
// dispatches, byte-identical to a tracing-off session.
func supervisorSvcGuardCheck(svc *supervision.Service) func(ownerID, toolName, argsJSON string) string {
	if svc == nil {
		return nil
	}
	return svc.GuardCheck()
}

// wireSubAgentNetwork lowers agents.subagent.network into the confinement policy
// a confined dispatch carries, starting the session's egress broker when the mode
// calls for one. The broker is returned so its owner can stop it; nil otherwise.
//
// The two restrictive modes are only meaningful with confinement: the rule lives
// in the jail, so a configuration that would not jail the dispatch is REFUSED at
// dispatch time (and reported by doctor) rather than silently dropped. Starting
// the broker here is unconditional so the port is known before any dispatch; the
// dispatch is where the contradiction is caught, with the config in hand.
func wireSubAgentNetwork(sc config.SubAgentConfig) (confinement.NetworkPolicy, *egress.Broker, error) {
	// The broker comes first: its port is what the policy pins to. The mapping
	// itself lives in the runner (one copy, next to the code that applies it).
	if !strings.EqualFold(strings.TrimSpace(sc.Network), "broker") {
		np, err := runner.SubAgentNetworkPolicy(sc.Network, 0)
		return np, nil, err
	}

	broker := egress.NewBroker(egress.AllowHosts(normalizeBrokerHosts(sc.NetworkHosts)...))
	port, err := broker.Start()
	if err != nil {
		return confinement.NetworkPolicy{}, nil, fmt.Errorf("agents.subagent.network: start the egress broker: %w", err)
	}
	np, err := runner.SubAgentNetworkPolicy(sc.Network, port)
	if err != nil {
		broker.Stop()
		return confinement.NetworkPolicy{}, nil, err
	}
	slog.Info("sub-agent egress broker started", "port", port, "allow_hosts", len(sc.NetworkHosts))
	return np, broker, nil
}

// normalizeBrokerHosts puts allowlist entries into the form the broker matches
// against (see egress.normalizeHost), so a padded, upper-cased or
// trailing-dot entry does exactly what it looks like. Without this an exact-host
// allowlist silently never matches.
func normalizeBrokerHosts(hosts []string) []string {
	return tools.NormalizeBrokerHosts(hosts)
}

// retainedLaneActive decides whether NewDelegateTool should build the retained
// runtime. A local session additionally needs a capturable root sandbox; a
// container session's root is pure-causal, and its report-lane siblings come
// from the session container, so retainCapable does not apply there.
func retainedLaneActive(mode string, retainCapable, containerSession bool) bool {
	if !retainedMode(mode) {
		return false
	}
	return containerSession || retainCapable
}

// retainedMode reports whether agents.subagent.mode selects the retained-run
// lane for delegate (Plan 7 Phase 2). The default (empty) is retained —
// the parity default; "shared" is the explicit opt-out.
func retainedMode(mode string) bool {
	return !strings.EqualFold(strings.TrimSpace(mode), "shared")
}
