package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
)

// ExecRequest describes a command to run in a workspace.
type ExecRequest struct {
	Command string
	Args    []string
	// Cwd overrides the workspace working directory for this command.
	Cwd string
	// Stdin, when non-nil, is fed to the command.
	Stdin []byte
}

// ExecResult is the outcome of a workspace command.
//
// Stdout carries *combined* output for a local workspace, preserving the stream
// interleaving tools relied on before workspaces existed. A remote workspace
// cannot interleave two transports' streams, so it reports Stdout and Stderr
// separately and leaves Stdout uncontaminated.
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
type localWorkspace struct {
	pv *PathValidator
}

var _ Workspace = (*localWorkspace)(nil)

func newLocalWorkspace(pv *PathValidator) *localWorkspace {
	return &localWorkspace{pv: pv}
}

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

func (w *localWorkspace) Remove(_ context.Context, path string) error {
	return os.Remove(path)
}

func (w *localWorkspace) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

// Exec runs a command on the host with combined output, matching what the shell
// tools produced before workspaces existed.
func (w *localWorkspace) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	dir := req.Cwd
	if dir == "" {
		dir = w.WorkDir()
	}
	cmd.Dir = dir
	if req.Stdin != nil {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

	out, err := cmd.CombinedOutput()
	res := ExecResult{Stdout: string(out)}
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
