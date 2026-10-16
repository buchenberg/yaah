package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
)

// ExecRequest describes a command to run in a workspace.
type ExecRequest struct {
	Command string
	Args    []string
	// Cwd overrides the workspace working directory for this command.
	Cwd string
	// Stdin, when non-nil, is fed to the command.
	Stdin []byte
	// SeparateStreams keeps stdout and stderr apart. Leave false for combined
	// output, which preserves interleaving — what shells and build tools want.
	// Set it when the caller parses one stream, as diff does with stdout.
	SeparateStreams bool
}

// ExecResult is the outcome of a workspace command.
//
// ExitCode is -1 when the command could not be started at all, and >= 0 when it
// ran — including a non-zero exit. Callers that used to distinguish via
// *exec.ExitError must branch on this instead, because a sandbox reports an exit
// code without an error.
//
// Stdout carries *combined* output for a local workspace unless the request asked
// for separate streams, preserving the interleaving tools relied on before
// workspaces existed. A remote workspace keeps the streams apart, so callers that
// parse stdout must set SeparateStreams.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Workspace is where a tool's files live and where its commands run.
//
// Every filesystem and process operation a tool performs goes through this
// interface. That is what lets one tool implementation operate on the host during
// a normal session and inside an isolated substrate — a git worktree or a
// container — when a supervised run asks for one.
//
// I/O methods receive paths already resolved by ResolvePath, so containment is
// checked once at the boundary rather than re-derived per call.
type Workspace interface {
	// ResolvePath validates a caller-supplied path and returns the absolute path
	// to operate on, enforcing containment.
	ResolvePath(path string) (string, error)

	// WorkDir is the directory relative paths and commands resolve against.
	// Empty means the process working directory (the pre-workspace behaviour).
	WorkDir() string

	// Join joins path elements using this workspace's syntax. A sandbox is POSIX
	// regardless of the host OS, so tools must not use filepath.Join for
	// workspace paths.
	Join(elem ...string) string

	// Local reports whether this workspace is the host filesystem. Tools that
	// need host-only facilities (PowerShell, a host toolchain) use it to fail
	// clearly instead of silently operating on the wrong machine.
	Local() bool

	// Shell returns the program and flag for running a shell command string,
	// for example ("sh", "-c").
	Shell() (string, string)

	ReadFile(ctx context.Context, path string) ([]byte, error)

	// WriteFile is atomic: a reader never observes a partially written file, and
	// a crash mid-write cannot truncate the original.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error

	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	// Lstat is Stat without following a final symlink, matching os.Lstat. Walks
	// use it for their root so a symlink root is reported as a link and not
	// descended, which is what filepath.WalkDir's Lstat-based root check does.
	Lstat(ctx context.Context, path string) (fs.FileInfo, error)
	// ReadDir lists a directory. Entries are not sorted by the implementation;
	// callers that need a stable order must sort.
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
	Remove(ctx context.Context, path string) error
	MkdirAll(ctx context.Context, path string, perm fs.FileMode) error

	// Exec runs a command. A non-nil error reports that the command could not be
	// started or exited non-zero; callers that need the distinction inspect
	// ExecResult.ExitCode.
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
}

// WorkspaceSetter is implemented by tools that operate on a workspace. The
// registry injects the session's workspace automatically, so tools never
// construct one.
type WorkspaceSetter interface {
	SetWorkspace(ws Workspace)
}

// localWorkspace is the host filesystem: the default, and what every session
// used before workspaces existed.
//
// When a confiner is set, Exec runs commands under the syscall jail (Landlock on
// Linux) with the workspace as the sole writable root. The orchestrator's
// workspace never carries one, so its commands keep running unconfined; a
// confiner is attached only on the sub-agent workdir lane (that run's bound
// world).
type localWorkspace struct {
	pv *PathValidator

	// confiner, when non-nil, is the syscall-deny backend. nil is the host path:
	// commands run unconfined (the orchestrator's workspace, always).
	confiner containment.Backend
	// placement decides whether a confined run jails or falls back to advisory.
	placement confinement.Placement
	// network is the egress axis of the confinement spec.
	network containment.NetworkPolicy
}

var _ Workspace = (*localWorkspace)(nil)

func newLocalWorkspace(pv *PathValidator) *localWorkspace {
	return &localWorkspace{pv: pv}
}

// NewConfinedLocalWorkspace returns a local workspace whose commands run under
// backend's syscall jail, with the workspace's working directory as the sole
// writable root. placement resolves whether the jail is used: PlacementPaddock
// fails closed when the host cannot jail, PlacementAuto falls back to advisory,
// PlacementAdvisory never jails. The orchestrator's workspace is never built
// this way.
func NewConfinedLocalWorkspace(pv *PathValidator, backend containment.Backend, placement confinement.Placement, network containment.NetworkPolicy) *localWorkspace {
	return &localWorkspace{pv: pv, confiner: backend, placement: placement, network: network}
}

// Confined reports whether this workspace carries a syscall-jail backend.
func (w *localWorkspace) Confined() bool { return w.confiner != nil }

func (w *localWorkspace) ResolvePath(path string) (string, error) {
	return resolvePathWithPV(w.pv, path)
}

func (w *localWorkspace) WorkDir() string {
	if w.pv == nil {
		return ""
	}
	return w.pv.WorkDir
}

func (w *localWorkspace) Local() bool { return true }

func (w *localWorkspace) Join(elem ...string) string { return filepath.Join(elem...) }

func (w *localWorkspace) Shell() (string, string) {
	if runtime.GOOS != "windows" {
		return "sh", "-c"
	}
	if _, err := exec.LookPath("pwsh"); err == nil {
		return "pwsh", "-Command"
	}
	return "powershell", "-Command"
}

func (w *localWorkspace) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile delegates to atomicWriteFile so the crash-safety guarantee
// (temp file plus rename in the same directory) is preserved exactly.
func (w *localWorkspace) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	return atomicWriteFile(path, data, perm)
}

func (w *localWorkspace) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

// Lstat mirrors os.Lstat: the link itself, not its target.
func (w *localWorkspace) Lstat(_ context.Context, path string) (fs.FileInfo, error) {
	return os.Lstat(path)
}

func (w *localWorkspace) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
}

func (w *localWorkspace) Remove(_ context.Context, path string) error {
	return os.Remove(path)
}

func (w *localWorkspace) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

// Exec runs a command in the workspace. On the orchestrator's host workspace (no
// confiner) it runs directly; on a confined workspace it runs under the syscall
// jail, rooted at the workspace's working directory.
func (w *localWorkspace) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if w.confiner == nil {
		if w.placement == confinement.PlacementPaddock {
			// Fail closed: a paddock placement with no backend refuses rather
			// than running unconfined.
			return ExecResult{ExitCode: -1}, fmt.Errorf(
				"%w: no syscall-jail backend on this host", containment.ErrPaddockNotEstablished)
		}
		return w.advisoryExec(ctx, req)
	}

	available := true
	if ok, _ := w.confiner.Available(); !ok {
		available = false
	}
	confine, err := confinement.ResolvePlacement(w.placement, available)
	if err != nil {
		return ExecResult{ExitCode: -1}, err
	}
	if !confine {
		return w.advisoryExec(ctx, req)
	}
	return w.confinedExec(ctx, req)
}

// advisoryExec is the unconfined host path — the behaviour every session had
// before workspaces, and the fallback when no jail is available.
func (w *localWorkspace) advisoryExec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	dir := req.Cwd
	if dir == "" {
		dir = w.WorkDir()
	}
	cmd.Dir = dir
	if req.Stdin != nil {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

	var res ExecResult
	var err error
	if req.SeparateStreams {
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		res.Stdout = stdout.String()
		res.Stderr = stderr.String()
	} else {
		var out []byte
		out, err = cmd.CombinedOutput()
		res.Stdout = string(out)
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	return res, err
}

// confinedExec runs the command under the syscall jail, with the workspace's
// working directory as the sole writable root. Output is combined unless the
// request asked for separate streams, matching the host path. A jail that could
// not be established is reported as a launch failure (ExitCode -1) — the command
// never ran, so there is no unconfined fallback.
func (w *localWorkspace) confinedExec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	root := w.WorkDir()
	spec := containment.ConfinementSpec{
		WritableRoots: []string{root},
		Network:       w.network,
	}
	lreq := containment.LaunchRequest{
		Command:     append([]string{req.Command}, req.Args...),
		WorkingRoot: root,
		Cwd:         req.Cwd,
		Stdin:       req.Stdin,
	}
	if dl, ok := ctx.Deadline(); ok {
		lreq.Timeout = time.Until(dl)
	}
	res, err := containment.LaunchConfined(w.confiner, spec, lreq)
	if err != nil {
		return ExecResult{ExitCode: -1, Stderr: err.Error()}, nil
	}
	out := ExecResult{ExitCode: res.ExitCode}
	if req.SeparateStreams {
		out.Stdout, out.Stderr = res.Stdout, res.Stderr
	} else {
		out.Stdout = res.Stdout + res.Stderr
	}
	if res.ExitCode != 0 {
		return out, fmt.Errorf("command exited with status %d", res.ExitCode)
	}
	return out, nil
}

// workspaceOf returns a tool's injected workspace, falling back to a local
// workspace over its validator. The fallback keeps a tool usable when only a
// PathValidator was set — which unit tests do — and means migrating a tool needs
// no change to existing construction sites.
func workspaceOf(ws Workspace, pv *PathValidator) Workspace {
	if ws != nil {
		return ws
	}
	return newLocalWorkspace(pv)
}

// shellCommand builds an ExecRequest that runs a shell command string using the
// workspace's own shell, so callers never choose a shell themselves.
func shellCommand(ws Workspace, command string) ExecRequest {
	shell, flag := ws.Shell()
	return ExecRequest{
		Command: shell,
		Args:    []string{flag, command},
	}
}

// requireLocal reports a clear error when a tool that only works on the host is
// pointed at an isolated workspace. Failing loudly beats silently running the
// command somewhere the caller did not intend.
func requireLocal(ws Workspace, tool string) error {
	if ws != nil && !ws.Local() {
		return fmt.Errorf("%s: not available in an isolated workspace", tool)
	}
	return nil
}

// walkWorkspace walks a workspace tree depth-first, mirroring
// filepath.WalkDir's contract: fn is called for the root and every entry;
// returning fs.SkipDir from a directory skips its contents, and returning it
// from a file entry skips the remaining files in that directory. An unreadable
// directory is reported through fn rather than aborting the walk, so callers
// keep the error-handling shape they had with WalkDir.
func walkWorkspace(
	ctx context.Context,
	ws Workspace,
	root string,
	fn func(path string, d fs.DirEntry, err error) error,
) error {
	// Lstat, not Stat: filepath.WalkDir stats its root with os.Lstat, so a root
	// that is a symlink arrives as a ModeSymlink entry and is not descended.
	// Following it here would defeat the walkers' symlink guard for the root
	// and walk a target (possibly outside a lexically-contained workspace) that
	// the pre-workspace code refused.
	info, err := ws.Lstat(ctx, root)
	if err != nil {
		return fn(root, nil, err)
	}
	// WalkDir invokes fn for the root before descending. Without this, a root
	// that is a file is never offered to fn, and callers that ignore walk
	// errors report "no matches" for a path that plainly matches.
	if err := fn(root, infoDirEntry{info: info}, nil); err != nil {
		if errors.Is(err, fs.SkipDir) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return walkChildren(ctx, ws, root, fn)
}

// walkChildren visits root's entries depth-first. It is separate from
// walkWorkspace so recursing does not re-stat a directory the caller already
// classified.
func walkChildren(
	ctx context.Context,
	ws Workspace,
	root string,
	fn func(path string, d fs.DirEntry, err error) error,
) error {
	entries, err := ws.ReadDir(ctx, root)
	if err != nil {
		return fn(root, nil, err)
	}
	for _, e := range entries {
		p := ws.Join(root, e.Name())
		if err := fn(p, e, nil); err != nil {
			if !errors.Is(err, fs.SkipDir) {
				return err
			}
			if !e.IsDir() {
				return nil
			}
			continue
		}
		if e.IsDir() {
			if err := walkChildren(ctx, ws, p, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// infoDirEntry adapts an fs.FileInfo to fs.DirEntry for a walk root, which has
// no directory entry of its own. Every method answers from the stat the walk
// already performed.
type infoDirEntry struct{ info fs.FileInfo }

func (e infoDirEntry) Name() string               { return e.info.Name() }
func (e infoDirEntry) IsDir() bool                { return e.info.IsDir() }
func (e infoDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e infoDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }
