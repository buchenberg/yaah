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
// beside the target, then rename. Atomicity is a property of the workspace, not of
// a shared helper, so each implementation provides it for its own substrate.
func (w *sandboxWorkspace) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode) error {
	script := `d=$(dirname -- "$1"); mkdir -p -- "$d" || exit 1
t="$1.tmp.$$"
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
func (w *sandboxWorkspace) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	const format = "%F|%s|%a|%Y"
	out, err := w.runCapture(ctx, `stat -c "$2" -- "$1"`, p, format)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("sandbox stat %s: unexpected stat output %q", p, out)
	}

	size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	perm, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 8, 32)
	epoch, _ := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64)

	return sandboxFileInfo{
		name:  path.Base(p),
		size:  size,
		mode:  fs.FileMode(perm),
		isDir: strings.Contains(parts[0], "directory"),
		mod:   time.Unix(epoch, 0),
	}, nil
}

func (w *sandboxWorkspace) Remove(ctx context.Context, p string) error {
	return w.run(ctx, `rm -f -- "$1"`, p)
}

// ReadDir lists a directory with `ls -1Ap`, which lists all entries except
// "." and ".." and appends a slash to directories. Dotfiles must appear:
// os.ReadDir returns them on the host, and silently hiding ".env" or
// ".github" would make glob and grep miss files the caller asked about.
// fs.DirEntry needs only a name and an IsDir, so that is enough and avoids
// parsing a full listing format. Symlinks are NOT resolved: a symlink to a
// directory reports IsDir false, which is accurate for the entry itself.
func (w *sandboxWorkspace) ReadDir(ctx context.Context, p string) ([]fs.DirEntry, error) {
	out, err := w.runCapture(ctx, `ls -1Ap -- "$1"`, p)
	if err != nil {
		return nil, err
	}
	var entries []fs.DirEntry
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		entries = append(entries, sandboxDirEntry{
			name:  strings.TrimSuffix(line, "/"),
			isDir: strings.HasSuffix(line, "/"),
		})
	}
	return entries, nil
}

func (w *sandboxWorkspace) MkdirAll(ctx context.Context, p string, perm fs.FileMode) error {
	return w.run(ctx, `mkdir -p -- "$1" && chmod "$2" "$1"`, p, strconv.FormatUint(uint64(perm.Perm()), 8))
}

// Exec runs a command in the sandbox. The working directory defaults to the
// workspace root so relative paths behave as they do locally. A transport
// failure reports ExitCode -1, matching the Workspace interface contract that
// callers (staticcheck, diff) use to distinguish "never ran" from "ran and
// failed".
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

// sandboxDirEntry is the subset of fs.DirEntry callers use. Info is nil because
// obtaining it would need another in-band stat per entry, which is a round trip
// per file; callers that need details stat the path themselves.
type sandboxDirEntry struct {
	name  string
	isDir bool
}

func (e sandboxDirEntry) Name() string               { return e.name }
func (e sandboxDirEntry) IsDir() bool                { return e.isDir }
func (e sandboxDirEntry) Type() fs.FileMode          { return 0 }
func (e sandboxDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }
