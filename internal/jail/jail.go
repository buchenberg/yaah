// Package jail selects yaah's per-platform containment backend.
//
// shepherd-runtime ships one containment.Backend per mechanism, each in its
// own nested module, and a host passes the one that fits into the sandbox
// constructor. The runtime never imports a member, and build tags are
// prohibited in this repo, so selection is a runtime.GOOS lookup into a
// detector table: landlock on Linux, winacl on Windows, no entry elsewhere
// (advisory tier only).
//
// Availability is what Backend() reports, not presence: every member's
// Detect() returns nil unless the mechanism's Available() succeeds at call
// time, so a present-but-broken platform degrades to advisory here. Callers
// that must be certain still re-check Available() on the returned backend —
// kernel state can change between selection and use, and the placement
// record claims enforcement, not selection.
package jail

import (
	"runtime"

	"github.com/buchenberg/goatherd-runtime/sandbox/containment"
	"github.com/buchenberg/goatherd-runtime/sandbox/landlock"
	"github.com/buchenberg/goatherd-runtime/sandbox/winacl"
)

// detectors maps a GOOS to the member module's Detect. Read-only in
// production; backendFor is the seam tests inject tables into, so nothing
// here is reassigned after init.
var detectors = map[string]func() containment.Backend{
	"linux":   landlock.Detect,
	"windows": winacl.Detect,
}

// backendFor resolves the detector entry for goos and calls it, returning nil
// when the platform has no entry (or a nil entry). Pure over its arguments.
func backendFor(goos string, table map[string]func() containment.Backend) containment.Backend {
	d, ok := table[goos]
	if !ok || d == nil {
		return nil
	}
	return d()
}

// Backend returns the containment backend for the running platform, or nil
// where the host has none (advisory tier only).
func Backend() containment.Backend { return backendFor(runtime.GOOS, detectors) }

// MaybeRunTrampoline handles the confine-then-exec trampoline sentinels used
// by the Landlock backend: the confining process re-enters this binary with
// a sentinel argv slot. Only Linux uses the trampoline path (winacl confines
// by launching a restricted token directly), so delegation is unconditional —
// off-Linux the sentinels never appear in argv.
func MaybeRunTrampoline(args []string) (int, bool) { return landlock.MaybeRunTrampoline(args) }
