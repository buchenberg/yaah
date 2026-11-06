package substrate

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
)

// SandboxWorkspace runs a tool's filesystem and process operations inside an
// isolated substrate via shepherd's Sandbox interface.
//
// There is no host path to open: the workspace lives in a snapshot or container,
// so every read, write, stat, and command is executed in-band through the
// sandbox. That is also why this exists at all — pointing the tools at a
// host-visible mount would isolate the files but leave the *processes* on the
// host, which is the thing a container is for.
//
// Wiring status: cmd/yaah selects this when workspace.backend is containerd
// (tools.SessionSandbox, the isolated-workspace plan §1.2) — one sandbox per
// session, image-probed, created, and bootstrapped (D1) before the first tool
// dispatch. The session's registry is curated (plan §3, Option A): the
// host-only tools are omitted so SetWorkspace's gate passes, the prompt
// carries the tool-surface delta (§2.2), and apply_out is the accept seam
// that lets changes out. Sub-agent inheritance of the sandbox is the plan's
// step 9 (decision D2).
type SandboxWorkspace struct {
	sb goatherdsandbox.Sandbox
	// root is the containment root inside the sandbox.
	root string
}

var _ Workspace = (*SandboxWorkspace)(nil)

// NewSandboxWorkspace binds a workspace to a sandbox and its container-side root.
func NewSandboxWorkspace(sb goatherdsandbox.Sandbox, root string) *SandboxWorkspace {
	if root == "" {
		root = "/workspace"
	}
	root = strings.TrimSuffix(root, "/")
	if root == "" {
		// The caller asked for "/" itself; trimming must not collapse it to
		// an empty prefix, which would silently disable containment.
		root = "/"
	}
	return &SandboxWorkspace{sb: sb, root: root}
}

// ResolvePath keeps absolute paths and resolves relative ones against the root,
// then rejects anything that escapes.
//
// Containment here is lexical (path.Clean), so a symlink created inside the
// sandbox could still point outside the root. That is a weaker guarantee than the
// host validator's symlink resolution, and it is bounded by the container: an
// escape lands elsewhere in the container's own filesystem, not on the host.
func (w *SandboxWorkspace) ResolvePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is required")
	}
	var abs string
	if strings.HasPrefix(p, "/") {
		abs = path.Clean(p)
	} else {
		abs = path.Clean(w.root + "/" + p)
	}
	// root "/" contains the whole container filesystem, where the prefix
	// comparison degenerates ("//"): every absolute path is in scope.
	if abs == w.root || w.root == "/" || strings.HasPrefix(abs, w.root+"/") {
		return abs, nil
	}
	return "", fmt.Errorf("path %q escapes the workspace root %q", p, w.root)
}

func (w *SandboxWorkspace) WorkDir() string { return w.root }

// Join always uses POSIX separators: the substrate is Linux by construction.
func (w *SandboxWorkspace) Join(elem ...string) string { return path.Join(elem...) }

// Local is false: this workspace is not the host filesystem.
func (w *SandboxWorkspace) Local() bool { return false }

// Shell is always POSIX inside a sandbox; the substrate is Linux by construction.
func (w *SandboxWorkspace) Shell() (string, string) { return "sh", "-c" }

func (w *SandboxWorkspace) ReadFile(ctx context.Context, p string) ([]byte, error) {
	return w.sb.ReadFile(ctx, p)
}

// sandboxWriteChunkBytes bounds how much file content one exec carries, in
// raw bytes before base64 expansion (~4/3). The ceiling is the kernel's
// per-argument limit, not the total ARG_MAX budget: Linux caps one argv
// string at MAX_ARG_STRLEN (32 pages = 128 KiB), and execve fails with
// E2BIG above it — a bound the in-memory fake cannot catch, because it
// never calls execve. 64 KiB raw encodes to ~85 KiB, comfortably under
// the cap with room for the encoded payload's own growth. (The kernel
// adapter's Config.WriteChunkBytes is larger because it splits its
// encoded payload across several arguments; the fake's shell cannot
// expand "$@", so this path keeps one argument per chunk.)
const sandboxWriteChunkBytes = 64 * 1024

// The three scripts of the chunked write. Only $1..$3 positionals appear:
// every value is a quoted argument, and the base64 alphabet carries no
// shell metacharacters, so file content and paths are never spliced into
// script text.
const (
	// writeFirstScript creates the target's parent directory, creates the
	// temp file beside the target, lands the first chunk, and prints the
	// temp's name on stdout. The mkdir -p mirrors the host
	// AtomicWriteFile's MkdirAll, so a write into a new nested path
	// succeeds on both substrates; the parent arrives as a quoted
	// positional, never spliced into the script. The EXIT trap removes the
	// temp and the chunk scratch file on any mid-script failure, and is
	// stood down before the success path survives.
	writeFirstScript = `mkdir -p -- "$3" || exit 1
t=$(mktemp -- "$1.XXXXXX") || exit 1
trap 'rm -f -- "$t" "$t.b64"' EXIT
printf '%s' "$2" > "$t.b64" || exit 1
base64 -d "$t.b64" >> "$t" || exit 1
rm -f -- "$t.b64"
trap - EXIT
printf '%s' "$t"`
	// writeChunkScript appends one chunk to the temp file. The base64
	// scratch file carries the decode without a pipeline — the in-memory
	// test double's shell interprets no pipes, and a real sh gains
	// nothing from one here.
	writeChunkScript = `printf '%s' "$2" > "$1.b64" || exit 1
base64 -d "$1.b64" >> "$1" || exit 1
rm -f -- "$1.b64"`
	// writeFinalScript applies the mode and renames the temp onto the
	// target — the atomicity: a crash before this exec leaves the
	// target untouched.
	writeFinalScript = `chmod "$1" "$2" || exit 1
mv -f -- "$2" "$3" || exit 1
rm -f -- "$2.b64"`
)

// WriteFile mirrors the host implementation's crash safety: write a temp file
// beside the target, then rename. The temp name comes from mktemp, so it is
// created O_EXCL and code inside the sandbox cannot pre-create or symlink a
// predictable name, and the trap removes it if the write fails part-way.
// Parent directories ARE created (mkdir -p on the target's parent, passed as
// a quoted positional): the host AtomicWriteFile MkdirAlls the same directory
// before its temp file, so both substrates accept a write into a new nested
// path and neither diverges on a missing directory.
//
// The content travels as base64 in positional arguments, not on stdin: the
// runc-v2 shim keeps a write end on an exec's stdin FIFO for the process's
// whole lifetime, so a stdin-based write never observes EOF and blocks until
// the context expires against the containerd backend — the kernel's
// Sandbox.WriteFile made the same move, for the same reason, and this path
// must match it to be usable there. Payloads beyond sandboxWriteChunkBytes
// append across several execs before the single renaming exec.
func (w *SandboxWorkspace) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode) error {
	mode := strconv.FormatUint(uint64(perm.Perm()), 8)
	first := min(sandboxWriteChunkBytes, len(data))

	res, err := w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", writeFirstScript, "shepherd", p, base64.StdEncoding.EncodeToString(data[:first]), path.Dir(p)},
		Cwd:     w.root,
	})
	if err != nil {
		return fmt.Errorf("sandbox write %s: %w", p, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("sandbox write %s: exit %d: %s", p, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	// mktemp's name, printed by the first exec; trailing whitespace cannot
	// be part of it, and trimming is robust to any sh that adds a newline.
	temp := strings.TrimSpace(res.Stdout)

	// A failed exec after the first leaves the temp behind unless the
	// caller removes it; the scratch file is each chunk exec's own to
	// clean, so the belt-and-braces rm names both.
	cleanup := func() {
		_, _ = w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", `rm -f -- "$1" "$1.b64"`, "shepherd", temp},
			Cwd:     w.root,
		})
	}

	for off := first; off < len(data); off += sandboxWriteChunkBytes {
		end := min(off+sandboxWriteChunkBytes, len(data))
		res, err := w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", writeChunkScript, "shepherd", temp, base64.StdEncoding.EncodeToString(data[off:end])},
			Cwd:     w.root,
		})
		if err != nil {
			cleanup()
			return fmt.Errorf("sandbox write %s: chunk %d: %w", p, off, err)
		}
		if res.ExitCode != 0 {
			cleanup()
			return fmt.Errorf("sandbox write %s: chunk %d: exit %d: %s", p, off, res.ExitCode, strings.TrimSpace(res.Stderr))
		}
	}

	res, err = w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", writeFinalScript, "shepherd", mode, temp, p},
		Cwd:     w.root,
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("sandbox write %s: %w", p, err)
	}
	if res.ExitCode != 0 {
		cleanup()
		return fmt.Errorf("sandbox write %s: exit %d: %s", p, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// appendCreateScript creates the target when missing with an append
// redirection, never a truncating >, and applies the caller's mode. A writer
// that loses a create race appends nothing rather than clearing the winner's
// first chunk. Existing files skip this script entirely, so their mode is
// preserved.
const appendCreateScript = `printf '%s' '' >> "$1" || exit 1
chmod "$2" "$1" || exit 1`

// appendChunkScript appends one decoded chunk. The scratch file avoids a
// pipeline, matching the write path's in-memory-test compatibility, and is
// unique per exec so two concurrent appends do not race on a shared .b64.
const appendChunkScript = `t=$(mktemp -- "$1.XXXXXX") || exit 1
trap 'rm -f -- "$t"' EXIT
printf '%s' "$2" > "$t" || exit 1
base64 -d "$t" >> "$1" || exit 1
rm -f -- "$t"
trap - EXIT`

// Append mirrors the host's O_APPEND behavior at the exec level: each chunk
// opens the target with >>. Concurrent writers do not lose data; chunks from
// two appends may interleave, which is the same guarantee a POSIX append
// gives a single write.
func (w *SandboxWorkspace) Append(ctx context.Context, p string, data []byte, perm fs.FileMode) error {
	mode := strconv.FormatUint(uint64(perm.Perm()), 8)
	if _, err := w.Stat(ctx, p); errors.Is(err, fs.ErrNotExist) {
		if err := w.run(ctx, appendCreateScript, p, mode); err != nil {
			return fmt.Errorf("sandbox append %s: %w", p, err)
		}
	} else if err != nil {
		return fmt.Errorf("sandbox append %s: %w", p, err)
	}
	for off := 0; off < len(data); off += sandboxWriteChunkBytes {
		end := min(off+sandboxWriteChunkBytes, len(data))
		if err := w.run(ctx, appendChunkScript, p, base64.StdEncoding.EncodeToString(data[off:end])); err != nil {
			return fmt.Errorf("sandbox append %s: chunk %d: %w", p, off, err)
		}
	}
	return nil
}

// ReadRange returns a bounded window. The kernel Sandbox exposes only a
// whole-file read, so this materializes the file once and slices it; the
// tools using it still bound what reaches the model. The host implementation
// seeks instead and never reads outside the requested window.
func (w *SandboxWorkspace) ReadRange(ctx context.Context, p string, offset, length int64) ([]byte, error) {
	if offset < 0 {
		return nil, fmt.Errorf("read range: offset must be non-negative")
	}
	if length <= 0 {
		return []byte{}, nil
	}
	data, err := w.sb.ReadFile(ctx, p)
	if err != nil {
		return nil, err
	}
	if offset >= int64(len(data)) {
		return []byte{}, nil
	}
	end := offset + length
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return append([]byte(nil), data[offset:end]...), nil
}

// Stat runs `stat` in the sandbox and maps the result to fs.FileInfo. shepherd's
// Sandbox interface has no Stat, and adding one just for this would push a
// POSIX-shaped detail into the kernel; a single in-band call keeps the kernel
// surface smaller. It follows symlinks, matching os.Stat.
func (w *SandboxWorkspace) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	return w.stat(ctx, p, true)
}

// Lstat is Stat without following the final symlink, matching os.Lstat, so a
// walk can recognise a symlink root and refuse to descend as filepath.WalkDir
// does.
func (w *SandboxWorkspace) Lstat(ctx context.Context, p string) (fs.FileInfo, error) {
	return w.stat(ctx, p, false)
}

// stat is the shared in-band stat. A missing path exits 42 before stat runs so
// the caller gets fs.ErrNotExist instead of stat's stderr. The presence test
// differs by mode: os.Stat rejects a dangling symlink (its target is missing),
// while os.Lstat reports the link, so Stat requires -e and Lstat also accepts
// -L.
func (w *SandboxWorkspace) stat(ctx context.Context, p string, follow bool) (fs.FileInfo, error) {
	const format = "%F|%s|%a|%Y"
	script := `[ -e "$1" ] || [ -L "$1" ] || exit 42
stat -c "$2" -- "$1"`
	if follow {
		script = `[ -e "$1" ] || exit 42
stat -L -c "$2" -- "$1"`
	}
	res, err := w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", script, "shepherd", p, format},
		Cwd:     w.root,
	})
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 42 {
		return nil, fs.ErrNotExist
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("sandbox stat %s: exit %d: %s", p, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	parts := strings.SplitN(strings.TrimSpace(res.Stdout), "|", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("sandbox stat %s: unexpected stat output %q", p, res.Stdout)
	}

	size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	permuint, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 8, 32)
	epoch, _ := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64)

	return sandboxFileInfo{
		name:  path.Base(p),
		size:  size,
		mode:  sandboxMode(permuint, parts[0]),
		isDir: strings.Contains(parts[0], "directory"),
		mod:   time.Unix(epoch, 0),
	}, nil
}

// sandboxMode turns `stat -c %a` octal permissions and the `%F` type string into
// an fs.FileMode. The high octal digit carries setuid/setgid/sticky and the type
// lives in bits above the permission bits, so a plain cast would both report a
// directory as a regular file and turn "4755" into a meaningless low bit.
//
// Every non-regular type `%F` can report gets type bits. Without them a FIFO,
// socket, or device node looks like a plain file to Mode().IsRegular(), and a
// tool that gates on regular files would then open, copy, or block on it. An
// unrecognized spelling (GNU's "weird file", platform-specific door/event
// port) is irregular rather than regular, for the same reason.
func sandboxMode(perm uint64, fileType string) fs.FileMode {
	mode := fs.FileMode(perm & 0o777)
	if perm&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if perm&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if perm&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	switch {
	case strings.Contains(fileType, "regular"):
		// No type bits.
	case strings.Contains(fileType, "directory"):
		mode |= fs.ModeDir
	case strings.Contains(fileType, "symbolic link"):
		mode |= fs.ModeSymlink
	case strings.Contains(fileType, "fifo"):
		mode |= fs.ModeNamedPipe
	case strings.Contains(fileType, "socket"):
		mode |= fs.ModeSocket
	case strings.Contains(fileType, "character special"):
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case strings.Contains(fileType, "block special"):
		mode |= fs.ModeDevice
	default:
		mode |= fs.ModeIrregular
	}
	return mode
}

// Remove deletes a file or an empty directory, matching os.Remove rather than
// rm -f: a directory is removed with rmdir (so a non-empty one fails with a
// clear "Directory not empty"), and a missing path reports fs.ErrNotExist
// instead of succeeding silently.
func (w *SandboxWorkspace) Remove(ctx context.Context, p string) error {
	info, err := w.Lstat(ctx, p)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return w.run(ctx, `rmdir -- "$1"`, p)
	}
	return w.run(ctx, `rm -f -- "$1"`, p)
}

// RemoveAll deletes a tree inside the sandbox with rm -rf. Like the host
// implementation it refuses the workspace root, so a path bug cannot wipe the
// container's working tree in one call.
func (w *SandboxWorkspace) RemoveAll(ctx context.Context, p string) error {
	clean := path.Clean(p)
	if clean == "/" || clean == w.root {
		return fmt.Errorf("refusing to remove the workspace root %s", clean)
	}
	return w.run(ctx, `rm -rf -- "$1"`, p)
}

// ReadDir lists a directory in-band. It uses `find -printf '%y\0%f\0'` rather
// than `ls`: find emits a NUL-delimited stream, so a filename containing a
// newline stays one entry instead of splitting into phantom entries, and it
// reports each entry's type character, which is what lets DirEntry.Type expose
// ModeDir/ModeSymlink the way os.ReadDir does. Dotfiles are included, and
// -mindepth 1 excludes "." and "..".
//
// This assumes a GNU userland, which Stat already does with `stat -c`; a
// substrate that has to run `go`, `git`, and `rg` is a full Linux image, not a
// distroless one, so find -printf is available.
func (w *SandboxWorkspace) ReadDir(ctx context.Context, p string) ([]fs.DirEntry, error) {
	out, err := w.runCapture(ctx, `find "$1" -mindepth 1 -maxdepth 1 -printf '%y\0%f\0'`, p)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var entries []fs.DirEntry
	for i := 0; i+1 < len(fields); i += 2 {
		typ, name := fields[i], fields[i+1]
		if name == "" {
			continue
		}
		entries = append(entries, sandboxDirEntry{name: name, mode: sandboxEntryMode(typ)})
	}
	return entries, nil
}

// sandboxEntryMode maps find's `%y` type character to fs.FileMode type bits.
// Walkers test `d.Type()&fs.ModeSymlink != 0` to skip symlinks, so returning 0
// for an unrecognized letter would both disable that guard and let the entry
// look regular: regular `f` is the only spelling that maps to no type bits.
func sandboxEntryMode(typ string) fs.FileMode {
	switch typ {
	case "f":
		return 0
	case "d":
		return fs.ModeDir
	case "l":
		return fs.ModeSymlink
	case "p":
		return fs.ModeNamedPipe
	case "s":
		return fs.ModeSocket
	case "c":
		return fs.ModeDevice | fs.ModeCharDevice
	case "b":
		return fs.ModeDevice
	default:
		return fs.ModeIrregular
	}
}

// Move renames a file inside the sandbox with mv — the substrate's own rename,
// so the bytes never cross the Exec transport. force is passed because the tool
// above has already decided whether a clobber is allowed.
//
// Across filesystems mv copies and then unlinks. If the unlink fails after the
// copy succeeded, mv reports an error with the destination in place. That is
// the same partial outcome the host fallback classifies with PartialMoveError,
// so check whether the destination appeared or changed and report it the same
// way: RecordingTool then records the destination even though the call failed.
// A failure that left the destination untouched keeps the original error, so a
// plain permission error is not misdescribed as a partial move.
func (w *SandboxWorkspace) Move(ctx context.Context, src, dst string) error {
	before, beforeErr := w.Lstat(ctx, dst)
	res, execErr := w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `mv -f -- "$1" "$2"`, "shepherd", src, dst},
		Cwd:     w.root,
	})
	if execErr != nil {
		// A transport error can still follow an applied move; classify the
		// destination before returning so RecordingTool does not miss it.
		after, afterErr := w.Lstat(ctx, dst)
		if afterErr == nil &&
			(errors.Is(beforeErr, fs.ErrNotExist) || (beforeErr == nil && !sameSandboxFile(before, after))) {
			return &PartialMoveError{Src: src, Dst: dst, Err: execErr}
		}
		return execErr
	}
	moveErr := fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	if res.ExitCode == 0 {
		return nil
	}
	if beforeErr != nil && !errors.Is(beforeErr, fs.ErrNotExist) {
		return moveErr
	}
	after, afterErr := w.Lstat(ctx, dst)
	if errors.Is(afterErr, fs.ErrNotExist) && IsCrossDeviceMoveError(res.Stderr) {
		// mv refused across mounts and left no destination behind. Mark it
		// so move_tree can copy+remove; a partial mv stays an ordinary
		// error and must not be retried into a merge.
		return fmt.Errorf("%w: %v", ErrCrossDevice, moveErr)
	}
	if afterErr != nil {
		return moveErr
	}
	if errors.Is(beforeErr, fs.ErrNotExist) || !sameSandboxFile(before, after) {
		return &PartialMoveError{Src: src, Dst: dst, Err: moveErr}
	}
	return moveErr
}

// IsCrossDeviceMoveError inspects the diagnostic after the last ": ", not the
// whole message: mv includes both paths, and a path may itself contain the
// phrase. Matching the reason rather than the suffix keeps a trailing
// "(os error 18)" or a different implementation's wording recognisable.
func IsCrossDeviceMoveError(stderr string) bool {
	s := strings.ToLower(strings.TrimSpace(stderr))
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return strings.Contains(s, "cross-device link") ||
		strings.Contains(s, "cross device link") ||
		strings.Contains(s, "cross-filesystem") ||
		strings.Contains(s, "cross filesystem")
}

// sameSandboxFile compares the metadata a sandbox stat exposes, used to tell a
// failed move that created or replaced the destination from one that changed
// nothing. It is only a heuristic for the force-overwrite case: when dst
// already existed with identical size, mode, and mtime, the partial move is
// invisible here (the recorder's content snapshot is the authoritative check).
func sameSandboxFile(a, b fs.FileInfo) bool {
	return a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Copy duplicates a file inside the sandbox with cp, then sets the source's
// mode on the destination with chmod --reference: the bytes stay in the
// substrate, so a large copy costs neither context nor a read/write round trip.
//
// chmod --reference rather than cp -p: -p also asks cp to preserve ownership
// and timestamps, and preserving ownership can fail on a bind-mounted source
// owned by another uid. GNU cp writes the destination and *then* exits non-zero
// on the failed preservation, so the tool would report a failed copy even
// though dst was created or truncated. A separate chmod keeps exactly the
// documented guarantee — the source's permission mode — and nothing more.
func (w *SandboxWorkspace) Copy(ctx context.Context, src, dst string) error {
	return w.run(ctx, `cp -f -- "$1" "$2" && chmod --reference="$1" -- "$2"`, src, dst)
}

func (w *SandboxWorkspace) MkdirAll(ctx context.Context, p string, perm fs.FileMode) error {
	return w.run(ctx, `mkdir -p -- "$1" && chmod "$2" "$1"`, p, strconv.FormatUint(uint64(perm.Perm()), 8))
}

// Exec runs a command in the sandbox. The working directory defaults to the
// workspace root so relative paths behave as they do locally.
//
// Errors match LocalWorkspace.Exec: a command that never ran reports ExitCode
// -1, and a command that ran but exited non-zero returns an error carrying its
// status only, like *exec.ExitError's "exit status N". Callers that need the
// code without the error read ExecResult.ExitCode, which stays
// >= 0 for any completed command. Without this, every err != nil check in the
// tool set would treat a failed command as a success in an isolated workspace.
func (w *SandboxWorkspace) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = w.root
	}
	res, err := w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
		Command: req.Command,
		Args:    req.Args,
		Cwd:     cwd,
		Stdin:   req.Stdin,
	})
	if err != nil {
		return ExecResult{ExitCode: -1}, err
	}
	out := ExecResult{ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr}
	if !req.SeparateStreams {
		// A remote workspace cannot interleave two transports the way
		// CombinedOutput does, so combine stdout then stderr. Callers reading
		// only Stdout then still see error text, as they would locally.
		out.Stdout = res.Stdout + res.Stderr
		out.Stderr = ""
	}
	if out.ExitCode != 0 {
		// Match LocalWorkspace, whose *exec.ExitError string is just
		// "exit status N" and carries no output. Callers already surface the
		// output themselves (bash, git and bisect append it; go_mod stores it),
		// so embedding it here would duplicate an unbounded string into every
		// error the sandbox returns.
		return out, fmt.Errorf("exit status %d", out.ExitCode)
	}
	return out, nil
}

// run executes a shell script with positional arguments and maps a non-zero exit
// to an error carrying the script's stderr.
func (w *SandboxWorkspace) run(ctx context.Context, script string, args ...string) error {
	_, err := w.runCapture(ctx, script, args...)
	return err
}

func (w *SandboxWorkspace) runCapture(ctx context.Context, script string, args ...string) (string, error) {
	res, err := w.sb.Exec(ctx, goatherdsandbox.ExecRequest{
		Command: "sh",
		Args:    append([]string{"-c", script, "shepherd"}, args...),
		Cwd:     w.root,
	})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

// sandboxFileInfo is the subset of fs.FileInfo the tools actually read.
type sandboxFileInfo struct {
	name  string
	size  int64
	mode  fs.FileMode
	isDir bool
	mod   time.Time
}

func (f sandboxFileInfo) Name() string       { return f.name }
func (f sandboxFileInfo) Size() int64        { return f.size }
func (f sandboxFileInfo) Mode() fs.FileMode  { return f.mode }
func (f sandboxFileInfo) ModTime() time.Time { return f.mod }
func (f sandboxFileInfo) IsDir() bool        { return f.isDir }
func (f sandboxFileInfo) Sys() any           { return nil }

// sandboxDirEntry is the subset of fs.DirEntry callers use. mode carries the
// entry's type bits (ModeDir / ModeSymlink) so Type matches os.ReadDir's
// lstat-based result; Info is nil because obtaining it would need another
// in-band stat per entry, and callers that need details stat the path
// themselves.
type sandboxDirEntry struct {
	name string
	mode fs.FileMode
}

func (e sandboxDirEntry) Name() string               { return e.name }
func (e sandboxDirEntry) IsDir() bool                { return e.mode.IsDir() }
func (e sandboxDirEntry) Type() fs.FileMode          { return e.mode.Type() }
func (e sandboxDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

// Root returns the containment root inside the sandbox.
func (w *SandboxWorkspace) Root() string { return w.root }
