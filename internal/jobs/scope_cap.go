package jobs

// DefaultMaxDispatchScopes caps the number of concurrently ACTIVE
// dispatch scopes a session's scope tree may hold (yaah-95ri.4). Above
// the cap the substrate refuses new dispatches with ErrScopeExhausted
// instead of provisioning a world that will contend with the leaked or
// merely busy scopes already present. 64 leaves generous headroom over
// any sane wave size while bounding the damage of a leak.
const DefaultMaxDispatchScopes = 64

// ResolveDispatchScopeCap maps a configured cap onto the default: a
// non-positive configured value means "use the default", not "no cap" —
// the leak the cap guards against is exactly the configuration that
// would result from leaving it unset.
func ResolveDispatchScopeCap(cap int) int {
	if cap <= 0 {
		return DefaultMaxDispatchScopes
	}
	return cap
}
