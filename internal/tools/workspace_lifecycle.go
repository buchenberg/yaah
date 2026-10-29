package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/config"
)

// SandboxOpener constructs one unprovisioned goatherdsandbox.Sandbox from the
// workspace config. Wiring passes OpenContainerdWorkspaceSandbox (direct
// construction); tests pass a closure over the in-memory fake, so no test
// ever touches a real daemon.
type SandboxOpener func(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error)

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
	sb goatherdsandbox.Sandbox
	ws *sandboxWorkspace
	// baseline is the state captured right after the D1 bootstrap (copy-in
	// plus a rebind Apply), the anchor for the session's diffs and
	// apply-out. Nil when the session started without a bootstrap.
	baseline *shepherdtrace.WorkspaceState

	destroyOnce sync.Once
	destroyErr  error

	// captureMu serializes snapshot captures among dispatches. Session tools
	// still run concurrently, so a capture can interrupt a foreground command;
	// callers use it at dispatch boundaries.
	captureMu sync.Mutex
}

// StartSessionSandbox probes the configured image contract (D5) on a
// throwaway sandbox, then constructs and creates the session's sandbox.
//
// The probe runs first and on its own sandbox — a wrong image fails the
// session at startup, before it consumes a task, rather than at the first
// tool call mid-run. ProbeImageContract owns the probe sandbox's full
// lifecycle; this function owns the session sandbox's.
//
// A non-empty src bootstraps the session (D1, plan §2.3): the host working
// tree is copied into the sandbox root and a baseline state is captured, so
// the session can diff during and apply out on accept. An empty src skips
// the bootstrap — the sandbox is up, but there is no baseline to diff
// against and ApplyOut refuses.
//
// The deny patterns are the copy-in's copy of the local workspace's
// policy: files matching them never enter the container, so the agent can
// never read them there either. Wiring resolves the config default before
// calling; nil means no patterns.
func StartSessionSandbox(ctx context.Context, w config.WorkspaceConfig, open SandboxOpener, src string, deny []string) (*SessionSandbox, error) {
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
	if err := sb.Create(ctx, goatherdsandbox.SandboxSpec{Workdir: w.Root}); err != nil {
		err = fmt.Errorf("workspace: create session sandbox: %w", err)
		if derr := sb.Destroy(context.WithoutCancel(ctx)); derr != nil {
			return nil, errors.Join(err, fmt.Errorf("workspace: destroy after failed create: %w", derr))
		}
		return nil, err
	}
	ss := &SessionSandbox{
		sb: sb,
		// The config layer already validated and defaulted Root; the
		// workspace constructor still guards the empty/"/" cases itself.
		ws: newSandboxWorkspace(sb, w.Root),
	}
	// The D1 bootstrap (plan §2.3): copy the host working tree in, capture
	// the baseline, rebind to it. A failed bootstrap destroys — a
	// half-bootstrapped container is still a container.
	if src != "" {
		if berr := bootstrapSession(ctx, ss, src, deny); berr != nil {
			if derr := ss.Destroy(context.WithoutCancel(ctx)); derr != nil {
				return nil, errors.Join(berr, fmt.Errorf("workspace: destroy after failed bootstrap: %w", derr))
			}
			return nil, berr
		}
	}
	return ss, nil
}

// Backend names the sandbox backend, for diagnostics and doctor output.
func (s *SessionSandbox) Backend() string { return s.sb.Backend() }

// Workspace returns the tool-facing substrate bound to the session sandbox.
// Tools dispatch through it; they never see the sandbox itself.
func (s *SessionSandbox) Workspace() Workspace { return s.ws }

// Capture records the session container's current state, serialized against
// other dispatch captures. The session task may be stopped and restarted by
// the snapshot backend, so callers use this at a dispatch boundary rather than
// in the middle of a foreground tool call.
func (s *SessionSandbox) Capture(ctx context.Context) (shepherdtrace.WorkspaceState, error) {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if s.sb == nil {
		return shepherdtrace.WorkspaceState{}, errors.New("workspace: session sandbox is not initialized")
	}
	return s.sb.Capture(ctx)
}

// Releasable reports whether this session's backend can drop a captured
// state's lease. Callers probe this before Capture, which commits and pins a
// layer.
func (s *SessionSandbox) Releasable() bool {
	if s.sb == nil {
		return false
	}
	_, ok := s.sb.(interface {
		ReleaseState(context.Context, shepherdtrace.WorkspaceState) error
	})
	return ok
}

// ReleaseCapture drops a state captured by Capture, serialized with captures
// so a release cannot race another dispatch's snapshot operation.
func (s *SessionSandbox) ReleaseCapture(ctx context.Context, state shepherdtrace.WorkspaceState) error {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if s.sb == nil {
		return errors.New("workspace: session sandbox is not initialized")
	}
	releaser, ok := s.sb.(interface {
		ReleaseState(context.Context, shepherdtrace.WorkspaceState) error
	})
	if !ok {
		return fmt.Errorf("workspace: session backend %q does not expose ReleaseState", s.sb.Backend())
	}
	// The caller passes a cancel-stripped context so a completed dispatch can
	// still clean up. Bound the backend call here: a wedged release must not
	// hold captureMu past every later dispatch's own timeout.
	releaseCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return releaser.ReleaseState(releaseCtx, state)
}

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
