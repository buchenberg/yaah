package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	shepherd "github.com/buchenberg/shepherd-kernel-go"

	"github.com/buchenberg/yaah/internal/config"
)

// SandboxOpener constructs one unprovisioned shepherd.Sandbox from the
// workspace config. Wiring passes OpenContainerdWorkspaceSandbox (registry
// resolution — the step-4 seam); tests pass a closure over the in-memory
// fake, so no test ever touches the global backend registry.
type SandboxOpener func(w config.WorkspaceConfig) (shepherd.Sandbox, error)

// SessionSandbox owns the session's isolated workspace sandbox lifecycle
// (the isolated-workspace plan §1.2): one sandbox per session — not per tool
// call — created before the first tool dispatch and destroyed exactly once at
// session teardown.
//
// Ownership rule: from the moment StartSessionSandbox's opener hands over a
// sandbox, every path that does not return it destroys it. A leaked container
// is the most likely operational bug in isolated mode, so nothing may abandon
// one — including its own error paths.
type SessionSandbox struct {
	sb shepherd.Sandbox
	ws *sandboxWorkspace

	destroyOnce sync.Once
	destroyErr  error
}

// StartSessionSandbox probes the configured image contract (D5) on a
// throwaway sandbox, then constructs and creates the session's sandbox.
//
// The probe runs first and on its own sandbox — a wrong image fails the
// session at startup, before it consumes a task, rather than at the first
// tool call mid-run. ProbeImageContract owns the probe sandbox's full
// lifecycle; this function owns the session sandbox's.
func StartSessionSandbox(ctx context.Context, w config.WorkspaceConfig, open SandboxOpener) (*SessionSandbox, error) {
	probeSB, err := open(w)
	if err != nil {
		return nil, fmt.Errorf("workspace: open probe sandbox: %w", err)
	}
	if err := ProbeImageContract(ctx, probeSB); err != nil {
		return nil, err // the probe destroys its own sandbox on every path
	}

	sb, err := open(w)
	if err != nil {
		return nil, fmt.Errorf("workspace: open session sandbox: %w", err)
	}
	// Create before the first tool dispatch, so a broken daemon fails the
	// session here instead of a tool call mid-run. Workdir rides the spec
	// so test backends provision the containment root the way the
	// containerd adapter provisions its Config.Workdir; a failed Create
	// still destroys: a partially-provisioned container is a container,
	// and this cleanup must run even when the caller's context expired
	// mid-Create.
	if err := sb.Create(ctx, shepherd.SandboxSpec{Workdir: w.Root}); err != nil {
		err = fmt.Errorf("workspace: create session sandbox: %w", err)
		if derr := sb.Destroy(context.WithoutCancel(ctx)); derr != nil {
			return nil, errors.Join(err, fmt.Errorf("workspace: destroy after failed create: %w", derr))
		}
		return nil, err
	}
	return &SessionSandbox{
		sb: sb,
		// The config layer already validated and defaulted Root; the
		// workspace constructor still guards the empty/"/" cases itself.
		ws: newSandboxWorkspace(sb, w.Root),
	}, nil
}

// Backend names the sandbox backend, for diagnostics and doctor output.
func (s *SessionSandbox) Backend() string { return s.sb.Backend() }

// Workspace returns the tool-facing substrate bound to the session sandbox.
// Tools dispatch through it; they never see the sandbox itself.
func (s *SessionSandbox) Workspace() Workspace { return s.ws }

// Destroy releases the session sandbox. It is idempotent and safe from
// concurrent teardown paths (a failed startup that already destroyed, then
// session close): the first call does the work, later calls return its
// result without touching the backend again.
func (s *SessionSandbox) Destroy(ctx context.Context) error {
	s.destroyOnce.Do(func() {
		s.destroyErr = s.sb.Destroy(ctx)
		if s.destroyErr != nil {
			s.destroyErr = fmt.Errorf("workspace: destroy session sandbox: %w", s.destroyErr)
		}
	})
	return s.destroyErr
}
