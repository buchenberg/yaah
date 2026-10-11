package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Backend identifiers for WorkspaceConfig.Backend.
const (
	// BackendLocal runs tools on the host filesystem, as yaah always has.
	// It is the default and the byte-for-byte legacy path.
	BackendLocal = "local"
	// BackendContainerd runs tools inside a shepherd containerd sandbox:
	// a real Linux filesystem boundary per session. The yaah client must
	// run where the daemon runs — natively on Linux, or inside WSL2 (where
	// GOOS=linux); a Windows/macOS-native yaah cannot select this backend
	// and config validation refuses it at load. Pointing a non-Linux client
	// at a remote daemon is not part of this config surface.
	BackendContainerd = "containerd"
)

// WorkspaceConfig selects where tool execution happens. No workspace stanza
// means the local backend and the legacy behaviour; the stanza exists so the
// isolated-workspace plan's config surface (§1.1) has a single home.
type WorkspaceConfig struct {
	// Backend is "local" (default) or "containerd".
	Backend string `yaml:"backend"`

	// Root is the containment root inside the sandbox — the in-container
	// directory every relative tool path resolves against. It maps to the
	// kernel adapter's Workdir. Default "/workspace". Only meaningful for
	// the containerd backend; validated to be absolute and not "/" when it
	// is in effect (a "/" root would silently scope the whole container).
	Root string `yaml:"root"`

	// Containerd holds the containerd backend's connection and image
	// settings. Only meaningful when Backend is "containerd".
	Containerd ContainerdWorkspaceConfig `yaml:"containerd"`
}

// ContainerdWorkspaceConfig is the containerd backend's settings block.
// The fields map one-to-one onto the kernel adapter's containerd.Config.
type ContainerdWorkspaceConfig struct {
	// Address is the containerd socket or endpoint. Empty uses containerd's
	// own default resolution.
	Address string `yaml:"address"`

	// Namespace isolates this backend's containers and snapshots from other
	// containerd users on the same host. Empty defaults to "default".
	Namespace string `yaml:"namespace"`

	// Image is the base image provisioned for the session. Required when
	// the backend is containerd: a sandbox with no base filesystem has
	// nothing to run. The image must satisfy the D5 contract (sh, GNU
	// stat, GNU find, git, rg, base64, and a Go toolchain) —
	// ProbeImageContract verifies it at startup.
	Image string `yaml:"image"`

	// Snapshotter selects the containerd snapshotter plugin. Empty uses the
	// adapter's default ("overlayfs").
	Snapshotter string `yaml:"snapshotter"`

	// FIFODir is where the containerd *client* creates the FIFOs that carry
	// a task's and exec's stdio. containerd's own default (/run/containerd/
	// fifo) is root-owned, so an unprivileged yaah cannot create its FIFOs
	// there; an empty value normalizes to a per-user directory under the
	// system temp dir, which root can also use (the daemon-side shim opens
	// the FIFOs by path and runs as root, so either location serves it).
	FIFODir string `yaml:"fifo_dir"`
}

// normalizeWorkspace applies the documented defaults so a stanza that only
// sets a backend still yields a fully-formed config. It is idempotent.
func normalizeWorkspace(w *WorkspaceConfig) {
	if w.Backend == "" {
		w.Backend = BackendLocal
	}
	w.Backend = strings.TrimSpace(w.Backend)
	if w.Containerd.Namespace == "" {
		w.Containerd.Namespace = "default"
	}
	// Root defaults to the containment root the workspace surface assumes.
	// It is only consumed by the containerd backend, but normalizing it for
	// both keeps the field's meaning independent of the backend in effect.
	// Cleaned so the configured, validated, and resolved values agree:
	// "//workspace" and "/workspace" are one root, not two.
	if w.Root == "" {
		w.Root = "/workspace"
	} else {
		w.Root = path.Clean(w.Root)
	}
	// A socket path is the kind of value a deployment keeps in the
	// environment; provider fields get the same treatment.
	w.Containerd.Address = SubstituteEnv(w.Containerd.Address)
	w.Containerd.FIFODir = SubstituteEnv(w.Containerd.FIFODir)
	// The client — not the daemon — creates the task/exec stdio FIFOs, so
	// the directory must be writable by yaah's own uid. containerd's default
	// (/run/containerd/fifo) is root-only; a per-user temp dir works for both
	// an unprivileged session and a root one (the shim, which reads the
	// FIFOs, is root and bypasses the 0700 the client's MkdirAll leaves).
	// The configured value passes through as-is, like the socket address.
	if w.Containerd.FIFODir == "" {
		w.Containerd.FIFODir = filepath.Join(
			os.TempDir(), fmt.Sprintf("yaah-fifo-%d", os.Getuid()))
	}
}
