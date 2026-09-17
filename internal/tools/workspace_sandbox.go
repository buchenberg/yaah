package tools

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// sandboxWorkspace runs a tool's filesystem and process operations inside an
// isolated substrate via shepherd's Sandbox interface.
//
// There is no host path to open: the workspace lives in a snapshot or container,
// so every read, write, stat, and command is executed in-band through the
// sandbox. That is also why this exists at all — pointing the tools at a
// host-visible mount would isolate the files but leave the *processes* on the
// host, which is the thing a container is for.
//
// Wiring status: nothing selects this in production yet. Registry.SetWorkspace
// refuses an isolated workspace unless every registered filesystem tool is
// migrated, but no caller asks it for one — the supervised path still uses a
// host git worktree — so this type is exercised only by tests today. Selecting a
// containerd backend is the remaining step; until it lands, treat isolation as
// plumbed but not active.
type sandboxWorkspace struct {
	sb shepherd.Sandbox
	// root is the containment root inside the sandbox.
	root string
}

var _ Workspace = (*sandboxWorkspace)(nil)

// newSandboxWorkspace binds a workspace to a sandbox and its container-side root.
func newSandboxWorkspace(sb shepherd.Sandbox, root string) *sandboxWorkspace {
	if root == "" {
		root = "/workspace"
	}
	root = strings.TrimSuffix(root, "/")
	if root == "" {
		// The caller asked for "/" itself; trimming must not collapse it to
		// an empty prefix, which would silently disable containment.
		root = "/"
	}
	return &sandboxWorkspace{sb: sb, root: root}
}

// ResolvePath keeps absolute paths and resolves relative ones against the root,
// then rejects anything that escapes.
//
// Containment here is lexical (path.Clean), so a symlink created inside the
// sandbox could still point outside the root. That is a weaker guarantee than the
// host validator's symlink resolution, and it is bounded by the container: an
// escape lands elsewhere in the container's own filesystem, not on the host.
func (w *sandboxWorkspace) ResolvePath(p string) (string, error) {
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

func (w *sandboxWorkspace) WorkDir() string { return w.root }

// Join always uses POSIX separators: the substrate is Linux by construction.
func (w *sandboxWorkspace) Join(elem ...string) string { return path.Join(elem...) }

// Local is false: this workspace is not the host filesystem.
func (w *sandboxWorkspace) Local() bool { return false }

// Shell is always POSIX inside a sandbox; the substrate is Linux by construction.
func (w *sandboxWorkspace) Shell() (string, string) { return "sh", "-c" }

func (w *sandboxWorkspace) ReadFile(ctx context.Context, p string) ([]byte, error) {
	return w.sb.ReadFile(ctx, p)
}

// WriteFile mirrors the host implementation's crash safety: write a temp file
// beside the target, then rename. The temp name comes from mktemp, so it is
// created O_EXCL and code inside the sandbox cannot pre-create or symlink a
// predictable name, and the trap removes it if the write fails part-way. Parent
// directories are deliberately NOT created: the host atomicWriteFile fails when
// the target directory is missing, and matching that keeps a typo'd path from
// silently succeeding in one workspace and failing in the other.
func (w *sandboxWorkspace) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode) error {
	script := `t=$(mktemp -- "$1.XXXXXX") || exit 1
trap 'rm -f -- "$t"' EXIT
cat > "$t" || exit 1
chmod "$2" "$t" || exit 1
mv -f -- "$t" "$1"`

	res, err := w.sb.Exec(ctx, shepherd.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", script, "shepherd", p, strconv.FormatUint(uint64(perm.Perm()), 8)},
		Cwd:     w.root,
		Stdin:   data,
	})
	if err != nil {
		return fmt.Errorf("sandbox write %s: %w", p, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("sandbox write %s: exit %d: %s", p, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Stat runs `stat` in the sandbox and maps the result to fs.FileInfo. shepherd's
// Sandbox interface has no Stat, and adding one just for this would push a
// POSIX-shaped detail into the kernel; a single in-band call keeps the kernel
// surface smaller.
//
// It follows symlinks, matching os.Stat, and reports a missing path as
// fs.ErrNotExist so callers can use errors.Is rather than matching shell text.
func (w *sandboxWorkspace) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	const format = "%F|%s|%a|%Y"
	// A missing path exits 42 before stat runs, so the caller gets
	// fs.ErrNotExist instead of stat's stderr.
	script := `[ -e "$1" ] || exit 42
stat -L -c "$2" -- "$1"`
	res, err := w.sb.Exec(ctx, shepherd.ExecRequest{
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
	case strings.Contains(fileType, "directory"):
		mode |= fs.ModeDir
	case strings.Contains(fileType, "symbolic link"):
		mode |= fs.ModeSymlink
	}
	return mode
}

func (w *sandboxWorkspace) Remove(ctx context.Context, p string) error {
	return w.run(ctx, `rm -f -- "$1"`, p)
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
func (w *sandboxWorkspace) ReadDir(ctx context.Context, p string) ([]fs.DirEntry, error) {
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
// here would silently disable that guard.
func sandboxEntryMode(typ string) fs.FileMode {
	switch typ {
	case "d":
		return fs.ModeDir
	case "l":
		return fs.ModeSymlink
	default:
		return 0
	}
}

func (w *sandboxWorkspace) MkdirAll(ctx context.Context, p string, perm fs.FileMode) error {
	return w.run(ctx, `mkdir -p -- "$1" && chmod "$2" "$1"`, p, strconv.FormatUint(uint64(perm.Perm()), 8))
}

// Exec runs a command in the sandbox. The working directory defaults to the
// workspace root so relative paths behave as they do locally.
//
// Errors match localWorkspace.Exec: a command that never ran reports ExitCode
// -1, and a command that ran but exited non-zero returns an error carrying its
// status and output (the local implementation returns *exec.ExitError). Callers
// that need the code without the error read ExecResult.ExitCode, which stays
// >= 0 for any completed command. Without this, every err != nil check in the
// tool set would treat a failed command as a success in an isolated workspace.
func (w *sandboxWorkspace) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = w.root
	}
	res, err := w.sb.Exec(ctx, shepherd.ExecRequest{
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
		msg := strings.TrimSpace(out.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(out.Stdout)
		}
		return out, fmt.Errorf("exit %d: %s", out.ExitCode, msg)
	}
	return out, nil
}

// run executes a shell script with positional arguments and maps a non-zero exit
// to an error carrying the script's stderr.
func (w *sandboxWorkspace) run(ctx context.Context, script string, args ...string) error {
	_, err := w.runCapture(ctx, script, args...)
	return err
}

func (w *sandboxWorkspace) runCapture(ctx context.Context, script string, args ...string) (string, error) {
	res, err := w.sb.Exec(ctx, shepherd.ExecRequest{
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
