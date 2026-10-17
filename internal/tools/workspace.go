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
	"strings"
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
	// confinedEnv, when non-empty, overrides the process environment for
	// confined commands — the redirect that points HOME/TMPDIR/caches into the
	// worktree's scratch.
	confinedEnv []string
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

// WithEnv returns a copy of the workspace with extra environment variables
// applied to every confined command (keys here override the process
// environment). It copies rather than mutating the receiver, so a workspace
// handed to more than one registry is never altered under another's feet.
func (w *localWorkspace) WithEnv(env []string) *localWorkspace {
	clone := *w
	clone.confinedEnv = env
	return &clone
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
		Env:         mergeEnv(os.Environ(), w.confinedEnv),
	}
	if dl, ok := ctx.Deadline(); ok {
		// A passed deadline gives a negative duration; leave Timeout unset
		// rather than passing a nonsensical value to the backend.
		if d := time.Until(dl); d > 0 {
			lreq.Timeout = d
		}
	}
	res, err := containment.LaunchConfined(w.confiner, spec, lreq)
	if err != nil {
		// A jail that could not be established is a launch failure, not a
		// result: return the error so callers see it, never a silent success.
		return ExecResult{ExitCode: -1, Stderr: err.Error()}, err
	}
	out := ExecResult{ExitCode: res.ExitCode}
	if req.SeparateStreams {
		out.Stdout, out.Stderr = res.Stdout, res.Stderr
	} else {
		out.Stdout = res.Stdout + res.Stderr
	}
	if res.ExitCode != 0 {
		// A write the kernel refused looks like any other failure to the model
		// ("Permission denied", no clue why), which is the jail's real usability
		// cost. Say what happened and where the boundary is.
		if hint := confinementHint(res.Stderr, root); hint != "" {
			if req.SeparateStreams {
				out.Stderr = strings.TrimRight(out.Stderr, "\n") + "\n" + hint
			} else {
				out.Stdout = strings.TrimRight(out.Stdout, "\n") + "\n" + hint
			}
		}
		// Match the host path's wording (exec's *exec.ExitError) so callers
		// that surface the message read the same either way.
		return out, fmt.Errorf("exit status %d", res.ExitCode)
	}
	return out, nil
}

// confinementHint explains a failure the jail caused, or "" when the failure
// does not look like one. The jail is the boundary, not a bug, and a sub-agent
// that cannot see why a command failed will retry it (or flail) instead of
// routing around it — so the message names the boundary and, for the git case,
// the rule that makes it deliberate.
//
// Detection of a refused write requires BOTH a permission failure and a denied
// path the message names that lies outside the writable root. "Permission
// denied" alone is not evidence of a boundary refusal — a `chmod 000` file or a
// non-executable script inside the worktree produces the same errno — so a
// failure with no attributable path gets no hint. A missed hint costs nothing
// (the raw error still surfaces); a wrong one sends the model to the wrong
// remedy. The one exception is a snap-packaged toolchain, whose failure names
// its own cause precisely enough to translate.
func confinementHint(stderr, root string) string {
	if root == "" {
		return ""
	}
	if hint := snapToolchainHint(stderr); hint != "" {
		return hint
	}
	if _, ok := deniedOutsideRoot(stderr, root); !ok {
		return ""
	}
	const gitWorkRule = " Your changes are still captured — the orchestrator commits when it accepts your work, so there is nothing for you to commit."
	// git names the offending path in quotes; a path under the repo but outside
	// this workspace is the retained-lane case (the worktree's index and object
	// store live in the original repository's .git).
	if strings.Contains(stderr, ".git/") || strings.Contains(stderr, ".git\\") {
		return "[yaah] This write was refused by the sub-agent sandbox. You work in an isolated worktree whose only writable root is " + root +
			"; git's index and object store live in the original repository, outside it, so git write commands (add, commit, stash, checkout, reset) cannot run here." + gitWorkRule
	}
	return "[yaah] This write was refused by the sub-agent sandbox: its only writable root is " + root +
		", and writes outside it are denied by the kernel. Housekeeping paths (HOME, TMPDIR, build caches) are redirected inside that root, so work there instead."
}

// snapToolchainHint translates the one jail failure that is a packaging
// problem, not a confinement problem: a command that resolves to a snap cannot
// start inside the sub-agent sandbox. snap-confine is setuid, and commands run
// with no_new_privs, so it cannot raise the capabilities it needs. The raw
// message names neither the packaging nor the remedy, so the model retries it
// or blames its own code.
//
// Detection anchors on the snap-confine token, or on snap-confine's exact
// capability wording. Generic capability text also appears in non-snap
// failures, and mis-captioning those sends the model to the wrong remedy — but
// the "required permitted capability … not found in current capabilities" pair
// is specific to snap-confine, so it is safe without the executable's name in
// the message (the abort reaches the tool through a shell that does not always
// prefix it).
func snapToolchainHint(stderr string) string {
	snapToken := strings.Contains(stderr, "snap-confine")
	capabilityAbort := strings.Contains(stderr, "required permitted capability") &&
		strings.Contains(stderr, "not found in current capabilities")
	if !snapToken && !capabilityAbort {
		return ""
	}
	return "[yaah] This command resolved to a snap package, which cannot start inside the sub-agent sandbox: snap-confine is setuid and needs capabilities the sandbox never grants it (commands run with no_new_privs, so it cannot raise them). " +
		"Install a tarball or distro build of this toolchain and put it ahead of /snap/bin on PATH."
}

// deniedOutsideRoot reports whether stderr carries a permission failure on a path
// outside root, returning that path. Only paths the message actually names count:
// an unnamed failure cannot be attributed to the boundary, and guessing would
// caption ordinary in-worktree EACCES failures as refusals.
func deniedOutsideRoot(stderr, root string) (string, bool) {
	cleanRoot := filepath.Clean(root)
	resolvedRoot := cleanRoot
	if r, err := filepath.EvalSymlinks(cleanRoot); err == nil && r != "" {
		resolvedRoot = r
	}
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.Contains(line, "Permission denied") && !strings.Contains(line, "Read-only file system") {
			continue
		}
		for _, cand := range pathCandidates(line) {
			if !filepath.IsAbs(cand) {
				continue
			}
			c := filepath.Clean(cand)
			// Textual comparison against the spelling yaah passed first: the
			// message is produced with that spelling, so a /var-rooted worktree
			// matches here even though the filesystem resolves it to /private/var.
			if withinRoot(c, cleanRoot) {
				continue
			}
			// Then resolved, for a path whose symlinks do resolve (an escaping
			// link, or a candidate that already exists).
			if r, err := filepath.EvalSymlinks(c); err == nil && r != "" && withinRoot(r, resolvedRoot) {
				continue
			}
			return cand, true
		}
	}
	return "", false
}

// withinRoot reports whether p is root or lies beneath it, separator-aware so
// "/work/wt-other" is not "inside" "/work/wt". Host path rules apply because the
// message is produced on this host: the hint is only reachable where a kernel
// jail actually runs (Linux today), since every other placement either degrades
// to advisory or refuses before the body starts.
func withinRoot(p, root string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// pathCandidates extracts the path-like tokens from one stderr line: quoted
// segments (git quotes the path it could not create) and whitespace-delimited
// tokens containing a separator.
func pathCandidates(line string) []string {
	var out []string
	rest := line
	for {
		open := strings.IndexAny(rest, `'"`)
		if open < 0 {
			break
		}
		q := rest[open]
		end := strings.IndexByte(rest[open+1:], q)
		if end < 0 {
			// No closing quote: skip this one and keep scanning. Abandoning the
			// line would drop real refusals, because an apostrophe in prose
			// ("error: can't open '/etc/foo'") pairs with the quote that opens
			// the path and leaves nothing after it.
			rest = rest[open+1:]
			continue
		}
		out = append(out, rest[open+1:open+1+end])
		rest = rest[open+1+end+1:]
	}
	for _, f := range strings.Fields(line) {
		if strings.ContainsAny(f, `/\`) {
			// Strip the punctuation a message wraps a path in (a leading quote
			// from an apostrophe-paired segment, a trailing colon or comma).
			out = append(out, strings.Trim(f, `:;,.'"`))
		}
	}
	return out
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
