package config

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/budget"
)

// ValidationError collects one or more configuration problems. The error
// message joins them with "; " so a single fail-fast message reaches the
// user at startup.
//
// Validate returns ValidationError as a value (not a pointer). Callers
// matching with errors.As must target the value form:
//
//	var ve config.ValidationError
//	errors.As(err, &ve)
type ValidationError struct {
	Errors []string
}

func (e ValidationError) Error() string {
	if len(e.Errors) == 1 {
		return e.Errors[0]
	}
	return fmt.Sprintf("%d config errors: %s", len(e.Errors), strings.Join(e.Errors, "; "))
}

// Validate checks the config for structural problems that would cause
// runtime failures. It returns nil when the config is usable, or a
// ValidationError listing every problem found.
//
// This is a fail-fast guard: misconfigurations surface here at startup
// rather than as cryptic errors during the first agent prompt. The
// `yaah doctor` command performs deeper diagnostics; Validate covers
// only the essentials that would brick a session.
func Validate(cfg *Config) error {
	if cfg == nil {
		return ValidationError{Errors: []string{"config is nil"}}
	}

	var errs []string

	// Approval mode must be one of the known values.
	switch cfg.Agent.Default.Approval {
	case "", "allow", "ask", "deny":
	default:
		errs = append(errs, fmt.Sprintf(
			"agents.default.approval %q is invalid (must be allow, ask, or deny)",
			cfg.Agent.Default.Approval))
	}

	// Evidence guard: a halt below the nudge threshold is unreachable.
	guardNudge := cfg.Agent.Default.EvidenceGuardNudge
	if guardNudge == 0 {
		guardNudge = 6
	}
	if cfg.Agent.Default.EvidenceGuardNudge < 0 && cfg.Agent.Default.EvidenceGuardHalt > 0 {
		errs = append(errs, "agents.default.evidence_guard_halt is unreachable while evidence_guard_nudge is negative (disabled)")
	}
	if guardNudge > 0 && cfg.Agent.Default.EvidenceGuardHalt > 0 &&
		cfg.Agent.Default.EvidenceGuardHalt < guardNudge {
		errs = append(errs, "agents.default.evidence_guard_halt must be 0 (off) or at least evidence_guard_nudge")
	}

	// MCP approval policy follows the same enum.
	switch cfg.Agent.Default.MCPApproval {
	case "", "allow", "ask", "deny":
	default:
		errs = append(errs, fmt.Sprintf(
			"agents.default.mcp_approval %q is invalid (must be allow, ask, or deny)",
			cfg.Agent.Default.MCPApproval))
	}

	// Compaction threshold must be in [0, 1]. Zero means "use default"
	// and is accepted; only negative values or values > 1 are rejected.
	if t := cfg.Agent.Default.CompactionThreshold; t < 0 || t > 1 {
		errs = append(errs, fmt.Sprintf(
			"agents.default.compaction_threshold %f is out of range (must be 0–1)",
			t))
	}
	if t := cfg.Agent.Default.RawCompactionThreshold; t < 0 || t > 1 {
		errs = append(errs, fmt.Sprintf(
			"agents.default.raw_compaction_threshold %f is out of range (must be 0–1)",
			t))
	}

	// Context window must be non-negative. Zero means "use model default"
	// at the provider level; only negative values are rejected.
	if cfg.Agent.Default.ContextWindow < 0 {
		errs = append(errs, fmt.Sprintf(
			"agents.default.context_window %d is negative",
			cfg.Agent.Default.ContextWindow))
	}

	// Max iterations must be non-negative. Zero means "use default";
	// only negative values are rejected.
	if cfg.Agent.Default.MaxLoopCycles < 0 {
		errs = append(errs, "agents.default.max_iterations is negative")
	}

	// Estimate factor must be non-negative. Zero means "use default";
	// only negative values are rejected.
	if cfg.Agent.Default.EstimateFactor < 0 {
		errs = append(errs, "agents.default.estimate_factor is negative")
	}

	// Fallback provider, if configured, must exist in the providers map.
	if cfg.Agent.Fallback.Provider != "" {
		if _, ok := cfg.Providers[cfg.Agent.Fallback.Provider]; !ok {
			errs = append(errs, fmt.Sprintf(
				"agents.fallback.provider %q is not defined in providers",
				cfg.Agent.Fallback.Provider))
		}
	}

	// Sub-agent provider, if configured, must exist in the providers map.
	if cfg.Agent.SubAgent.Provider != "" {
		if _, ok := cfg.Providers[cfg.Agent.SubAgent.Provider]; !ok {
			errs = append(errs, fmt.Sprintf(
				"agents.subagent.provider %q is not defined in providers",
				cfg.Agent.SubAgent.Provider))
		}
	}

	// Containment must be a known placement; an unknown value would otherwise
	// silently mean "off" (unconfined), which is the wrong failure direction.
	switch strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.Containment)) {
	case "", "off", "none", "advisory", "auto", "paddock", "jail":
	default:
		errs = append(errs, fmt.Sprintf(
			"agents.subagent.containment %q is not one of: off (or none), advisory, auto, paddock (or jail)",
			cfg.Agent.SubAgent.Containment))
	}

	// Egress (Plan 7 Phase 3): the mode must be known, and hosts are only
	// meaningful for "broker" — naming hosts under another mode would look like
	// a policy that is not in effect.
	switch strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.Network)) {
	case "", "allow_all", "deny_all", "broker":
	default:
		errs = append(errs, fmt.Sprintf(
			"agents.subagent.network %q is not one of: allow_all, deny_all, broker",
			cfg.Agent.SubAgent.Network))
	}
	if hosts := cfg.Agent.SubAgent.NetworkHosts; len(hosts) > 0 {
		if m := strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.Network)); m != "broker" {
			errs = append(errs, "agents.subagent.network_hosts is set but network is not \"broker\" — the allowlist would have no effect")
		}
		for i, h := range hosts {
			switch {
			case strings.TrimSpace(h) == "":
				errs = append(errs, fmt.Sprintf("agents.subagent.network_hosts[%d] is empty", i))
			case strings.ContainsAny(h, "*?/"):
				// The broker matches EXACT host names, so a pattern would be a
				// silent no-op — the failure mode where a rule looks present and
				// admits nothing.
				errs = append(errs, fmt.Sprintf(
					"agents.subagent.network_hosts[%d] %q looks like a pattern; the allowlist matches exact host names only (e.g. api.example.com)", i, h))
			}
		}
	}

	// Mode selects the dispatch lane; an unknown value must not silently pick
	// one (the default is retained).
	switch strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.Mode)) {
	case "", "retained", "shared":
	default:
		errs = append(errs, fmt.Sprintf(
			"agents.subagent.mode %q is not one of: retained, shared",
			cfg.Agent.SubAgent.Mode))
	}

	// World selects the dispatch world (yaah-ihfh.3). An unknown value must
	// not silently resolve to the host lane.
	switch strings.ToLower(strings.TrimSpace(cfg.Agent.SubAgent.World)) {
	case "", "auto", "worktree", "container":
	default:
		errs = append(errs, fmt.Sprintf(
			"agents.subagent.world %q is not one of: auto, worktree, container",
			cfg.Agent.SubAgent.World))
	}
	for role, rc := range cfg.Agent.SubAgent.Roles {
		switch strings.ToLower(strings.TrimSpace(rc.World)) {
		case "", "auto", "worktree", "container":
		default:
			errs = append(errs, fmt.Sprintf(
				"agents.subagent.roles.%s.world %q is not one of: auto, worktree, container",
				role, rc.World))
		}
	}

	// Sub-agent budget floors must be sane: non-negative, not above the
	// matching max in the same role config, and satisfiable within the
	// schema hard ceiling (subagent-turn-budget-floors §4.6).
	if cfg.Agent.SubAgent.DefaultMinToolTurns < 0 {
		errs = append(errs, "agents.subagent.default_min_turns must not be negative")
	}
	if cfg.Agent.SubAgent.DefaultMinToolTurns >= budget.SchemaMaxIterations {
		errs = append(errs, fmt.Sprintf(
			"agents.subagent.default_min_turns %d is unsatisfiable (hard ceiling %d)",
			cfg.Agent.SubAgent.DefaultMinToolTurns, budget.SchemaMaxIterations))
	}
	for role, rc := range cfg.Agent.SubAgent.Roles {
		if rc.MinToolTurns < 0 {
			errs = append(errs, fmt.Sprintf(
				"agents.subagent.roles.%s.min_turns must not be negative", role))
		}
		if rc.MinLoopCycles < 0 {
			errs = append(errs, fmt.Sprintf(
				"agents.subagent.roles.%s.min_iterations must not be negative", role))
		}
		if rc.MaxToolTurns > 0 && rc.MinToolTurns > rc.MaxToolTurns {
			errs = append(errs, fmt.Sprintf(
				"agents.subagent.roles.%s.min_turns %d exceeds max_turns %d",
				role, rc.MinToolTurns, rc.MaxToolTurns))
		}
		if rc.MaxLoopCycles > 0 && rc.MinLoopCycles > rc.MaxLoopCycles {
			errs = append(errs, fmt.Sprintf(
				"agents.subagent.roles.%s.min_iterations %d exceeds max_iterations %d",
				role, rc.MinLoopCycles, rc.MaxLoopCycles))
		}
		if rc.MinToolTurns >= budget.SchemaMaxIterations {
			errs = append(errs, fmt.Sprintf(
				"agents.subagent.roles.%s.min_turns %d is unsatisfiable (hard ceiling %d)",
				role, rc.MinToolTurns, budget.SchemaMaxIterations))
		}
	}

	// Quality gate validator roles must be non-empty strings.
	for role, validators := range cfg.Agent.QualityGates {
		if len(validators) == 0 {
			errs = append(errs, fmt.Sprintf(
				"agents.quality_gates.%s has no validator roles",
				role))
		}
	}

	// Workspace deny patterns must be valid filepath.Match globs — a
	// malformed pattern would otherwise fail open at match time and
	// leave the files the operator meant to protect accessible.
	for i, pattern := range cfg.Agent.Default.WorkspaceDenyPatterns {
		if _, err := filepath.Match(pattern, "probe"); err != nil {
			errs = append(errs, fmt.Sprintf(
				"agents.default.workspace_deny_patterns[%d] %q is not a valid glob pattern",
				i, pattern))
		}
	}

	// Workspace backend selection and its containerd settings.
	errs = append(errs, validateWorkspace(&cfg.Workspace, runtime.GOOS)...)
	errs = append(errs, validateSupervisor(cfg.Agent.Default)...)

	// MCP server configs: transport must be stdio or http.
	for name, srv := range cfg.MCPServers {
		switch srv.Transport {
		case "", "stdio", "http":
		default:
			errs = append(errs, fmt.Sprintf(
				"mcp_servers.%s.transport %q is invalid (must be stdio or http)",
				name, srv.Transport))
		}
		// Note: we do NOT hard-fail on missing command/url here. A
		// degenerate MCP entry (e.g. a placeholder) is a no-op at load
		// time; hard-failing would block session startup for configs
		// that previously started fine. The MCP loader handles this
		// with a clearer error when the server is actually used.
	}

	if len(errs) == 0 {
		return nil
	}
	return ValidationError{Errors: errs}
}

// validateWorkspace checks the workspace stanza's obligations (the
// isolated-workspace plan §1.1). goos is a parameter rather than a direct
// runtime.GOOS read so the platform rule is testable from any OS.
//
// An empty backend is accepted: Load normalizes it to the local default, and
// a hand-built Config that skipped Load defaults the same way at use time.
func validateWorkspace(w *WorkspaceConfig, goos string) []string {
	var errs []string

	switch w.Backend {
	case "", BackendLocal, BackendContainerd:
	default:
		errs = append(errs, fmt.Sprintf(
			"workspace.backend %q is invalid (must be %s or %s)",
			w.Backend, BackendLocal, BackendContainerd))
	}

	if w.Backend != BackendContainerd {
		return errs
	}

	// The containerd backend needs a containerd daemon, which does not run
	// off-Linux. WSL2 qualifies exactly when yaah itself runs inside it
	// (GOOS=linux); a Windows-native yaah.exe cannot select this backend,
	// so the refusal keys on the client's platform, not on daemon presence.
	// Refuse at load, not at the first tool call mid-task.
	if goos != "linux" {
		errs = append(errs, fmt.Sprintf(
			"workspace.backend %q requires a Linux containerd daemon; %s cannot run one (native Linux or WSL2 required)",
			BackendContainerd, goos))
	}

	// A sandbox with no base filesystem has nothing to run.
	if w.Containerd.Image == "" {
		errs = append(errs, "workspace.containerd.image is required when workspace.backend is containerd")
	}

	// An empty fifo_dir survives load only when the yaah home did not
	// resolve absolutely (normalizeWorkspace refuses to inject a relative
	// default). Empty means the adapter's own root-only default — the
	// exact condition the fifo_dir default exists to avoid — so refuse it
	// here with the cause and both fixes named. Wiring validates before
	// the first sandbox starts, so this lands at session start, ahead of
	// the FIFO creation it would otherwise break at.
	if w.Containerd.FIFODir == "" {
		errs = append(errs, "workspace.containerd.fifo_dir is empty: the yaah home did not resolve to an absolute path; set an absolute $YAAH_HOME or an explicit workspace.containerd.fifo_dir")
	}

	// The FIFO directory is created client-side relative to the process
	// cwd if relative, which would scatter the task/exec FIFOs wherever
	// yaah happened to start; the socket address carries no such trap.
	if w.Containerd.FIFODir != "" && !filepath.IsAbs(w.Containerd.FIFODir) {
		errs = append(errs, fmt.Sprintf(
			"workspace.containerd.fifo_dir %q must be an absolute path", w.Containerd.FIFODir))
	}

	// The containment root must be an absolute path inside the sandbox —
	// a POSIX path, evaluated with the path package rather than filepath,
	// whose host-OS semantics would reject "/workspace" on Windows. The
	// workspace surface is POSIX by construction (workspace_sandbox.go uses
	// path.Join). "/" would silently scope the whole container — the
	// workspace surface exists to avoid exactly that — and a relative root
	// cannot resolve in-band.
	if w.Root == "" {
		errs = append(errs, "workspace.root is required when workspace.backend is containerd (default: /workspace)")
	} else {
		// Clean first: the sandbox collapses aliases such as "//" or "/a/.."
		// when it resolves the workdir, so the rule must judge the value the
		// container will actually use, not the literal one.
		cleaned := path.Clean(w.Root)
		if !path.IsAbs(cleaned) {
			errs = append(errs, fmt.Sprintf("workspace.root %q must be an absolute path inside the sandbox", w.Root))
		}
		if cleaned == "/" {
			errs = append(errs, fmt.Sprintf("workspace.root %q would scope the whole container; choose a dedicated root (default: /workspace)", w.Root))
		}
	}

	return errs
}
