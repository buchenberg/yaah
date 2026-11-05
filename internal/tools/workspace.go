package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/goatherd-runtime/sandbox/containment"
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
	// Remove deletes a file or an empty directory, matching os.Remove. A
	// non-empty directory is an error.
	Remove(ctx context.Context, path string) error
	MkdirAll(ctx context.Context, path string, perm fs.FileMode) error

	// Move renames src to dst, preserving the file's bytes and permission mode.
	// Both paths arrive already resolved. An implementation MAY replace an
	// existing dst: the no-clobber rule belongs to the move/copy tools, which
	// refuse an existing destination unless force is set, so a substrate must not
	// add a second, divergent one here. Directories are the caller's to refuse.
	Move(ctx context.Context, src, dst string) error
	// Copy duplicates src at dst with src's bytes and permission mode, leaving
	// src in place. Same contract as Move.
	Copy(ctx context.Context, src, dst string) error

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
// When a confiner is set, Exec runs commands under the containment jail
// (Landlock's syscall filtering on Linux, winacl's write-restricted access
// checks on Windows) with the workspace as the sole writable root. The orchestrator's
// workspace never carries one, so its commands keep running unconfined; a
// confiner is attached only on the sub-agent workdir lane (that run's bound
// world).
type localWorkspace struct {
	pv *PathValidator

	// confiner, when non-nil, is the syscall-deny backend. nil is the host path:
	// commands run unconfined (the orchestrator's workspace, always).
	confiner containment.Backend
	// placement decides whether a confined run jails or falls back to advisory.
	placement containment.Placement
	// network is the egress axis of the confinement spec.
	network confinement.NetworkPolicy
	// writableRoots narrows the jail's writable set (yaah-ihfh.2) and narrowed
	// records that a set was declared. The two are separate because an
	// all-readonly declaration is legitimately empty — and must mean "nothing
	// writable", not "the legacy whole-world root".
	writableRoots []string
	narrowed      bool
	// confinedEnv, when non-empty, overrides the process environment for
	// confined commands — the redirect that points HOME/TMPDIR/caches into the
	// worktree's scratch.
	confinedEnv []string
	// degrade, when non-nil, records the one-time decision to run this
	// workspace unconfined because the bound backend could not establish the
	// jail it promised. Clones share the pointer, so the record is per
	// workspace rather than per call.
	degrade *confinementDowngrade
}

var _ Workspace = (*localWorkspace)(nil)

func newLocalWorkspace(pv *PathValidator) *localWorkspace {
	return &localWorkspace{pv: pv}
}

// confinementDowngrade is the record of a workspace that could not be jailed
// and runs unconfined instead. One workspace value is cloned per tool call, so
// the record lives behind a pointer and is shared across the clones: the
// fallback is decided once, reported once, and visible to whoever asks later.
type confinementDowngrade struct {
	once   sync.Once
	mu     sync.Mutex
	reason string
}

// record stores the first reason and reports it; later calls return "". The
// first failure is the informative one — the rest are the same condition. A nil
// record (a workspace built without confinement) accepts the call and reports
// nothing, so a caller need not test the pointer first.
func (d *confinementDowngrade) record(reason string) (first string) {
	if d == nil {
		return ""
	}
	d.once.Do(func() {
		d.mu.Lock()
		d.reason = reason
		d.mu.Unlock()
		first = reason
	})
	return first
}

// get reports the recorded reason, if a downgrade happened.
func (d *confinementDowngrade) get() (string, bool) {
	if d == nil {
		return "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reason, d.reason != ""
}

// ConfinementDegraded reports whether commands in this workspace run unconfined
// because the backend could not establish its jail, and why.
func (w *localWorkspace) ConfinementDegraded() (string, bool) { return w.degrade.get() }

// degradedReporter is implemented by workspaces that can lose their jail at
// run time. Kept separate from Workspace so a remote or container substrate,
// which cannot degrade silently, simply does not implement it.
type degradedReporter interface {
	ConfinementDegraded() (string, bool)
}

// WorkspaceDegraded reports whether ws lost its jail and is running unconfined,
// and why. It reports false for workspaces that never requested confinement.
func WorkspaceDegraded(ws Workspace) (string, bool) {
	d, ok := ws.(degradedReporter)
	if !ok {
		return "", false
	}
	return d.ConfinementDegraded()
}

// NewLocalWorkspace exposes the host workspace for callers that construct a
// tool registry outside the composition root (tests, protocol adapters).
func NewLocalWorkspace(pv *PathValidator) Workspace {
	return newLocalWorkspace(pv)
}

// NewConfinedLocalWorkspace returns a local workspace whose commands run under
// backend's syscall jail, with the workspace's working directory as the sole
// writable root. placement resolves whether the jail is used: PlacementPaddock
// fails closed when the host cannot jail, PlacementAuto falls back to advisory,
// PlacementAdvisory never jails. The orchestrator's workspace is never built
// this way.
func NewConfinedLocalWorkspace(pv *PathValidator, backend containment.Backend, placement containment.Placement, network confinement.NetworkPolicy) *localWorkspace {
	return &localWorkspace{
		pv:        pv,
		confiner:  backend,
		placement: placement,
		network:   network,
		degrade:   &confinementDowngrade{},
	}
}

// WithWritableRoots narrows the jail to the given roots (yaah-ihfh.2). A nil
// or empty slice is meaningful: the caller declared a grant set with no
// writable root (all-readonly), so writes in the world are denied and only the
// housekeeping scratch stays writable. The legacy single root is the *absence*
// of a call — not an empty call. Roots are absolute and already checked to
// live inside the world (ResolveGrants).
func (w *localWorkspace) WithWritableRoots(roots []string) *localWorkspace {
	clone := *w
	clone.writableRoots = append([]string(nil), roots...)
	clone.narrowed = true
	return &clone
}

// Confined reports whether this local workspace runs commands under a
// confinement backend, or is configured to fail closed without one. A client
// workspace overlay must not bypass either posture.
func (w *localWorkspace) Confined() bool {
	if _, ok := w.degrade.get(); ok {
		// The jail was requested and could not be established, so commands run on
		// the host path: reporting "jailed" here would be a lie.
		return false
	}
	return w.confiner != nil || w.placement == containment.PlacementPaddock
}

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

// WorkspaceConfined reports whether a workspace declares confinement. The
// optional interface keeps sandbox implementations free to omit it.
func WorkspaceConfined(ws Workspace) bool {
	if c, ok := ws.(interface{ Confined() bool }); ok {
		return c.Confined()
	}
	return false
}

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
	// os.Remove already removes a file or an empty directory, which is the
	// Workspace contract rmdir and delete both rely on.
	return os.Remove(path)
}

func (w *localWorkspace) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

// partialMoveError reports a cross-filesystem move that copied the file but
// could not remove the source. The destination exists, so callers that track
// changes must record it even though Move returned an error; the message says
// so, because a plain retry without force will be refused as a clobber.
type partialMoveError struct {
	Src string
	Dst string
	Err error
}

func (e *partialMoveError) Error() string {
	return fmt.Sprintf("destination %s was created, but the source %s could not be removed: %v; remove one of the two files before retrying", e.Dst, e.Src, e.Err)
}

func (e *partialMoveError) Unwrap() error { return e.Err }

// Move renames a file on the host. os.Rename moves the directory entry, so the
// content, the mode, and (within one filesystem) ownership survive without the
// bytes ever being read — the cheapest exact move available. Across filesystems
// (EXDEV) it falls back to Copy plus Remove, matching the sandbox mv's
// transparent behavior at the cost of actually reading the bytes.
func (w *localWorkspace) Move(ctx context.Context, src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if cerr := w.Copy(ctx, src, dst); cerr != nil {
		return fmt.Errorf("move: cross-filesystem fallback: %w", cerr)
	}
	if rerr := os.Remove(src); rerr != nil {
		return &partialMoveError{Src: src, Dst: dst, Err: rerr}
	}
	return nil
}

// Copy streams src into a temp file beside dst and renames it into place, so a
// crash mid-copy leaves the previous dst intact instead of a truncated one —
// the same crash-safety WriteFile has. The content is never buffered whole: a
// large file copies in fixed-size chunks. The source's mode, including
// setuid/setgid/sticky, is applied to the temp before the rename. Ownership and
// extended attributes are not portable across filesystems and are not carried.
func (w *localWorkspace) Copy(_ context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp*")
	if err != nil {
		return fmt.Errorf("copy: create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Chmod(info.Mode()); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("copy: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("copy: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return fmt.Errorf("copy: rename: %w", err)
	}
	return nil
}

// Exec runs a command in the workspace. On the orchestrator's host workspace (no
// confiner) it runs directly; on a confined workspace it runs under the syscall
// jail, rooted at the workspace's working directory.
func (w *localWorkspace) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if w.confiner == nil {
		if w.placement == containment.PlacementPaddock {
			// Fail closed: a paddock placement with no backend refuses rather
			// than running unconfined.
			return ExecResult{ExitCode: -1}, fmt.Errorf(
				"%w: no paddock backend on this host (syscall-deny unavailable)", containment.ErrPaddockNotEstablished)
		}
		return w.advisoryExec(ctx, req)
	}

	// A recorded downgrade is permanent for this workspace: the probe that
	// failed once fails on every call. Re-attempting confinement would pay for
	// the same failing probe per command, so take the advisory path directly
	// rather than re-incurring that cost for every tool in the lane. Per-call
	// clones share the downgrade pointer, so they all short-circuit here.
	if _, ok := w.degrade.get(); ok {
		return w.advisoryExec(ctx, req)
	}

	available := true
	if ok, _ := w.confiner.Available(); !ok {
		available = false
	}
	confine, err := containment.ResolvePlacement(w.placement, available)
	if err != nil {
		return ExecResult{ExitCode: -1}, err
	}
	if !confine {
		return w.advisoryExec(ctx, req)
	}
	res, err := w.confinedExec(ctx, req)
	if err == nil || !errors.Is(err, containment.ErrPaddockNotEstablished) {
		return res, err
	}
	// The backend claims to be available but cannot establish the jail it
	// promised — a probe that fails at confine time, which the availability
	// check cannot see. This is the failure mode where a dispatch lane used to
	// lose every execution tool at once, because containment: auto never got the
	// advisory fallback it documents.
	if blocker := w.degradeBlocker(); blocker != "" {
		return res, fmt.Errorf("%w — the dispatch lane's commands cannot run: "+
			"backend %q could not establish confinement, and the advisory fallback is "+
			"withheld because %s", err, w.confiner.Name(), blocker)
	}
	if first := w.degrade.record(err.Error()); first != "" {
		slog.Warn("containment unavailable: dispatch lane runs unconfined (containment: auto, advisory fallback recorded)",
			"backend", w.confiner.Name(),
			"placement", string(w.placement),
			"reason", first)
	}
	return w.advisoryExec(ctx, req)
}

// degradeBlocker names the reason the advisory fallback is unavailable, with the
// setting that would change it. It returns "" when degrading is admissible.
//
// The distinction matters for the operator: telling someone on the default
// `auto` placement to "set containment: auto" is a no-op, and the real objection
// there is a declared grant set or an egress rule.
func (w *localWorkspace) degradeBlocker() string {
	switch {
	case w.placement != containment.PlacementAuto:
		return fmt.Sprintf("placement %q refuses to run unconfined — set "+
			"agents.subagent.containment: auto to allow a recorded advisory fallback, or off "+
			"to run the lane unconfined deliberately", w.placement)
	case w.narrowed:
		return "a declared grants: set cannot be kept on the unconfined host path — drop " +
			"the restrictive grants:, or fix the host's confinement backend so the paddock can be " +
			"established"
	case w.network.Mode != "" && w.network.Mode != confinement.NetAllowAll:
		return fmt.Sprintf("network policy %q cannot be enforced without the paddock — set "+
			"agents.subagent.network: allow_all, or fix the host's confinement backend", w.network.Mode)
	}
	return ""
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
// never ran. Retrying unconfined is Exec's decision, not this function's: see
// degradeBlocker for when an advisory fallback is admissible.
func (w *localWorkspace) confinedExec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	root := w.WorkDir()
	roots := w.writableRoots
	if !w.narrowed {
		roots = []string{root}
	}
	// Housekeeping writes are redirected into <root>/.yaah-scratch
	// (ConfinementScratchEnv), so a narrowed grant set must still admit it or
	// every shell command that touches HOME/TMPDIR would fail.
	if fi, statErr := os.Stat(filepath.Join(root, ".yaah-scratch")); statErr == nil && fi.IsDir() {
		roots = append(append([]string(nil), roots...), filepath.Join(root, ".yaah-scratch"))
	}
	spec := confinement.ConfinementSpec{
		WritableRoots: roots,
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
		// A narrowed grant set has plural roots, so the legacy single-root
		// explanation would name the wrong boundary. Use the grant list
		// instead whenever one is set.
		var hint string
		if w.narrowed {
			hint = grantsHint(w.writableRoots, res.Stderr+out.Stdout)
		} else {
			hint = confinementHint(res.Stderr, root)
		}
		if hint != "" {
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
// path the message names that lies outside the writable root. The leading
// phrases cover the producers a confined body actually emits — POSIX's
// "Permission denied", Go's Windows PathError ("open C:\path: Access is
// denied."), and .NET's "Access to the path 'C:\path' is denied." — and none of
// them alone is evidence of a boundary refusal: a `chmod 000` file or
// a non-executable script inside the worktree produces the same errno — so a
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
		// Windows spellings vary by producer: a Go PathError prints
		// "open C:\path: Access is denied." with the path inline, while
		// .NET/PowerShell writes "Access to the path 'C:\path' is denied."
		// Only lines naming a path can be attributed, so the bare cmd.exe
		// "Access is denied." (no path) yields no hint rather than a
		// misattributed refusal.
		if !strings.Contains(line, "Permission denied") &&
			!strings.Contains(line, "Read-only file system") &&
			!strings.Contains(line, "Access is denied") &&
			!strings.Contains(line, "Access to the path") {
			continue
		}
		for _, cand := range pathCandidates(line) {
			if !absPath(cand) {
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
// jail actually runs (Linux via Landlock, Windows via winacl), since every other
// placement either degrades to advisory or refuses before the body starts.
//
// Windows-filesystem paths fold case: producers spell them inconsistently (an
// MSYS tool lowercasing the drive letter, shell vs Explorer directory
// capitalization), and NTFS folds them onto the same file — a refusal inside
// the worktree must not be reported as a boundary refusal just because the
// spelling differs. Folding triggers on two shapes: a drive-letter path on
// either side on every host (so the attribution behavior is pinned in tests on
// Linux CI, and an MSYS-spelled candidate folds off-Windows too), and every
// path on a Windows host — which also covers a worktree rooted at a UNC share,
// neither side of which is a drive path. A POSIX root keeps exact comparison,
// because Unix paths are case-sensitive and the message spells what the kernel
// named. An 8.3 short-name spelling stays inexact — it neither matches nor
// misattributes; the hint is simply not offered, per the "guessing is worse
// than none" rule above.
func withinRoot(p, root string) bool {
	if drivePath(p) || drivePath(root) || runtime.GOOS == "windows" {
		pp := strings.ReplaceAll(p, "/", `\`)
		rr := strings.ReplaceAll(root, "/", `\`)
		lowerP, lowerR := strings.ToLower(pp), strings.ToLower(rr)
		return lowerP == lowerR || strings.HasPrefix(lowerP, lowerR+`\`)
	}
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// drivePath reports whether s carries a Windows drive-letter prefix
// ("C:\…", "C:/…") — separator required after the colon, so "a:b:<c", "1:x",
// and drive-relative "C:foo" spellings never count. The shape case folding and
// host-independent absoluteness are decided on.
func drivePath(s string) bool {
	return len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/')
}

// absPath reports whether p is absolute for its own shape: host filepath rules,
// plus the Windows drive-letter shape, which is absolute regardless of the
// host GOOS — a drive-path candidate must reach the comparison on Linux CI too
// (filepath.IsAbs("C:\x") is false off-Windows, and the hint code is pure
// string matching that both hosts must agree on).
func absPath(p string) bool {
	return filepath.IsAbs(p) || drivePath(p)
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

// grantsHint explains a permission failure under a narrowed grant set: the
// dispatch's writable roots are a list now, not the whole world, and a bare
// "Permission denied" invites a retry. Returns "" for a failure that does not
// look like a refused write.
func grantsHint(roots []string, output string) string {
	for _, phrase := range []string{"Permission denied", "Access is denied", "Access to the path"} {
		if strings.Contains(output, phrase) {
			if len(roots) == 0 {
				return " This dispatch declares no writable root (read-only grants); only its housekeeping scratch is writable."
			}
			return " This dispatch may write only under: " + strings.Join(roots, ", ") + "."
		}
	}
	return ""
}
