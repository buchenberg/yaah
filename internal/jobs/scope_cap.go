package jobs

import goatherd "github.com/buchenberg/goatherd-runtime"

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

// DispatchScopeCount counts the ACTIVE scopes that consume a session's
// dispatch capacity: every active scope except the ones startup recovery
// re-admitted from the trace (yaah-95ri.7). A resumed scope holds no
// loop and provisions no world — the run that owned it died with the
// previous process — so charging it to the cap lets a trace full of
// crashed dispatches refuse every dispatch of the new process, with no
// way out but the reaper's sweep.
//
// resumed is the recovery-adopted scope-ID set; nil means none. A nil
// scope in active is skipped: the input is the caller's slice, and a
// hole in it must not be reported as capacity.
func DispatchScopeCount(active []*goatherd.Scope, resumed map[string]bool) int {
	n := 0
	for _, scope := range active {
		if scope == nil || resumed[scope.ID()] {
			continue
		}
		n++
	}
	return n
}
