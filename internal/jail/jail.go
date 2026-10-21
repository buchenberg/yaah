// Package jail selects yaah's per-platform containment backend.
//
// shepherd-runtime ships one containment.Backend per mechanism, each in its
// own nested module, and a host passes the one that fits into the sandbox
// constructor. The runtime never imports a member module, and neither could
// a build-tag-seamed call site pick between them — build tags are prohibited
// in this repo — so selection is a runtime.GOOS map over the members' own
// cross-platform imports: landlock on Linux, winacl on Windows, nothing
// elsewhere (advisory tier only). Every member's Detect returns nil when its
// mechanism is absent, so a present-but-broken platform still degrades to
// advisory rather than pretending to jail.
package jail

import (
	"runtime"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/landlock"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/winacl"
)

// detectors maps a GOOS to the member module's Detect. Entries carry the
// platform-agnostic functions; both nested modules compile on every GOOS
// and report nil when their mechanism is unavailable. A GOOS with no entry
// has no backend: dispatches run the advisory tier.
var detectors = map[string]func() containment.Backend{
	"linux":   landlock.Detect,
	"windows": winacl.Detect,
}

// Backend returns the containment backend for the running platform, or nil
// where the host has none (advisory tier only).
func Backend() containment.Backend {
	d, ok := detectors[runtime.GOOS]
	if !ok || d == nil {
		return nil
	}
	return d()
}

// MaybeRunTrampoline handles the confine-then-exec trampoline sentinels used
// by the Landlock backend: the confining process re-enters this binary with
// a sentinel argv slot. Only Linux uses the trampoline path (winacl confines
// by launching a restricted token directly), so delegation is unconditional —
// off-Linux the sentinels never appear in argv.
func MaybeRunTrampoline(args []string) (int, bool) { return landlock.MaybeRunTrampoline(args) }
