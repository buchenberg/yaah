package dispatch

// live_env_test.go duplicates the live-containerd env knobs that live in
// internal/tools/workspace_activation_live_test.go: test helpers cannot cross
// package boundaries. Keep the two copies in sync.

import (
	"os"
	"strings"
)

const (
	liveAddrEnv        = "YAAH_LIVE_CONTAINERD_ADDR"
	liveImageEnv       = "YAAH_LIVE_CONTAINERD_IMAGE"
	liveNamespaceEnv   = "YAAH_LIVE_CONTAINERD_NAMESPACE"
	liveSnapshotterEnv = "YAAH_LIVE_CONTAINERD_SNAPSHOTTER"
	liveRootEnv        = "YAAH_LIVE_CONTAINERD_ROOT"
	liveTimeoutEnv     = "YAAH_LIVE_CONTAINERD_TIMEOUT"
)

// liveActivationEnv resolves one knob with its fallback.
func liveActivationEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
