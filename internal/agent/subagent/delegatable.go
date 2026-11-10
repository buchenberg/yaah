package subagent

// DelegatableRole reports whether the named role may appear on the
// dispatch-facing surfaces — the delegate schema's role enum, delegate
// role validation, list_subagents output, and the delegate role
// descriptions (yaah-21uk.4). Only primary-only roles are excluded: a
// role with mode "primary" is an orchestrator persona, and the
// orchestrator must never be offered a dispatch of itself or another
// primary persona. "subagent" (explicit; the missing-key default is
// "all" since parity W3) and "all" remain delegatable.
//
// Unknown roles and sessions with no populated registry are delegatable
// here: RoleProfileFor resolves them to the zero-value profile, and
// unknown names are rejected by dispatch validation elsewhere. This
// helper filters capability, not existence — the registry itself keeps
// every role, and role management surfaces stay unfiltered.
func DelegatableRole(name string) bool {
	return delegatableMode(RoleProfileFor(SubAgentRole(name)).Mode)
}

// DelegatableRoles filters role names down to the delegatable ones,
// preserving order. See DelegatableRole.
func DelegatableRoles(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if DelegatableRole(name) {
			out = append(out, name)
		}
	}
	return out
}

// delegatableMode reports whether a normalized role mode is dispatchable
// by the orchestrator. The vocabulary is validated at load
// (normalizeRoleMode), so anything that is not exactly "primary" —
// including the zero value of a profile built without a registry — is
// delegatable: an unrecognized mode must never hide a role.
func delegatableMode(mode string) bool {
	return mode != "primary"
}
