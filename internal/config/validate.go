package config

import (
	"fmt"
	"path/filepath"
	"strings"
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
