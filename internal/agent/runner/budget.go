package runner

import (
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// subAgentTimeoutResolver returns a DelegateTool timeout resolver that folds
// in the actual per-call role, so role-profile and per-role config
// timeouts are honoured rather than a single construction-time default.
func subAgentTimeoutResolver(subCfg config.SubAgentConfig) func(tools.SubAgentParams) time.Duration {
	return func(p tools.SubAgentParams) time.Duration {
		return resolveSubAgentTimeout(0, subCfg, subagent.SubAgentRole(p.Role))
	}
}

// resolveSubAgentTimeout picks the effective default timeout for a
// sub-agent DelegateTool. Precedence: per-call override (handled by the
// DelegateTool itself) > role-specific config > role profile default >
// global subagent default_timeout.
func resolveSubAgentTimeout(callSeconds int, subCfg config.SubAgentConfig, role subagent.SubAgentRole) time.Duration {
	if callSeconds > 0 {
		return time.Duration(callSeconds) * time.Second
	}
	if rc, ok := subCfg.Roles[string(role)]; ok && rc.Timeout > 0 {
		return time.Duration(rc.Timeout) * time.Second
	}
	if d := subagent.RoleProfileFor(role).Timeout; d > 0 {
		return d
	}
	if subCfg.DefaultTimeout > 0 {
		return time.Duration(subCfg.DefaultTimeout) * time.Second
	}
	return 0
}

// budgetSpecFor assembles the budget.Spec for a sub-agent dispatch from
// its four sources of truth: per-call arguments, per-role config, the
// role profile, and global subagent defaults. It is the single mapping
// between runner inputs and the budget package.
func budgetSpecFor(callIterations, callTurns int, profile subagent.RoleProfile, subCfg config.SubAgentConfig, role subagent.SubAgentRole) budget.Spec {
	rc := subCfg.Roles[string(role)]
	return budget.Spec{
		CallIterations:    callIterations,
		CallTurns:         callTurns,
		RoleMaxIterations: profile.MaxLoopCycles,
		RoleMinIterations: profile.MinLoopCycles,
		RoleMaxTurns:      profile.MaxToolTurns,
		RoleMinTurns:      profile.MinToolTurns,
		CfgMaxIterations:  rc.MaxLoopCycles,
		CfgMinIterations:  rc.MinLoopCycles,
		CfgMaxTurns:       rc.MaxToolTurns,
		CfgMinTurns:       rc.MinToolTurns,
		DefaultTurns:      subCfg.DefaultMaxToolTurns,
		DefaultMinTurns:   subCfg.DefaultMinToolTurns,
		HardCeiling:       budget.SchemaMaxIterations,
	}
}

// resolveSubAgentBudget resolves the effective iteration and turn
// budgets for a sub-agent dispatch in a single call so the two
// dimensions reconcile against each other (floors grow iterations
// rather than shrinking turns). Resolution lives in
// internal/agent/budget.
func resolveSubAgentBudget(callIterations, callTurns int, profile subagent.RoleProfile, subCfg config.SubAgentConfig, role subagent.SubAgentRole) budget.Budget {
	return budget.Resolve(budgetSpecFor(callIterations, callTurns, profile, subCfg, role))
}

// resolveOutputLimit picks the byte cap for sub-agent final output.
// Precedence: per-call override > role-specific config > opts fallback > default 50KB.
func resolveOutputLimit(callLimit int, subCfg config.SubAgentConfig, role subagent.SubAgentRole, optsDefault int) int {
	if callLimit > 0 {
		return callLimit
	}
	if rc, ok := subCfg.Roles[string(role)]; ok && rc.OutputLimit > 0 {
		return rc.OutputLimit
	}
	if optsDefault > 0 {
		return optsDefault
	}
	return 50 * 1024
}

func safeTruncateBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	i := maxBytes
	if i > len(s) {
		i = len(s)
	}
	for i > 0 && i < len(s) {
		if s[i] < 0x80 || s[i]&0xC0 == 0xC0 {
			break
		}
		i--
	}
	return s[:i]
}

// roleHasShell reports whether the role's tool list includes a shell
// (bash or powershell). An empty tool list means the role carries no
// tools at all — buildSubAgentRegistry registers nothing for it — so it
// has no shell either (#329). The legacy "empty means every tool"
// profile is gone: every shipped role file declares its tools.
func roleHasShell(tools []string) bool {
	for _, t := range tools {
		if t == "bash" || t == "powershell" {
			return true
		}
	}
	return false
}

func formatBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div := unit
	exp := 0
	prefixes := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	for nn := n / unit; nn >= unit && exp < len(prefixes)-1; nn /= unit {
		div *= unit
		exp++
	}
	prefix := prefixes[exp]
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), prefix)
}
