// Package sandboxfake provides an in-memory shepherd.Sandbox test double.
//
// The isolated-workspace activation path (config → sandbox construction →
// Registry.SetWorkspace → tool dispatch) must be testable on any host OS, with
// no containerd daemon and no POSIX userland. The fake therefore runs no
// processes: it interprets the small `sh -c` subset that yaah's
// sandboxWorkspace emits — test/[ , stat -c, find -printf, mktemp, cat >,
// chmod, mv, rm, mkdir, trap, exit — against a map-backed tree (shell.go).
//
// The lifecycle is real: Create must run before Exec and file IO, and Destroy
// ends the sandbox, mirroring a container's lifecycle so leak-detection tests
// have something to detect. ExecHook replaces the interpreter wholesale, for
// tests that pin wire-level behavior a working filesystem cannot produce
// (transport failures, canned exit codes, malformed output).
package sandboxfake

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// Lifecycle state errors. They are values: tests assert with errors.Is.
var (
	ErrNotCreated       = errors.New("sandboxfake: sandbox not created")
	ErrDestroyed        = errors.New("sandboxfake: sandbox destroyed")
	ErrAlreadyCreated   = errors.New("sandboxfake: sandbox already created")
	ErrAlreadyDestroyed = errors.New("sandboxfake: sandbox already destroyed")
)

// Sandbox is an in-memory shepherd.Sandbox. It is safe for concurrent use.
//
// Paths are POSIX inside the fake, matching the substrate sandboxWorkspace
// assumes: separators are / and the tree is rooted at /, seeded with a single
// directory entry. Nothing ever touches the host filesystem.
type Sandbox struct {
	// ExecHook, when non-nil, is called instead of the shell interpreter for
	// every Exec, after the request is recorded. It lets a test pin exact
	// wire-level results (exit codes, stdout, transport errors) that a
	// working in-memory filesystem cannot produce on demand.
	ExecHook func(shepherd.ExecRequest) (shepherd.ExecResult, error)

	mu        sync.Mutex
	files     map[string]*entry
	created   bool
	destroyed bool
	rev       int // capture revision counter
	tmp       int // mktemp uniqueness counter
	execs     []shepherd.ExecRequest
}

var _ shepherd.Sandbox = (*Sandbox)(nil)

// entry is one node of the fake filesystem.
type entry struct {
	isDir   bool
	isLink  bool
	target  string      // symlink target, when isLink
	data    []byte      // file content
	mode    fs.FileMode // permission + setuid/setgid/sticky bits
	modTime time.Time
}

// New returns an empty sandbox with a lone root directory. Create must be
// called before any Exec or file IO.
func New() *Sandbox {
	return &Sandbox{
		files: map[string]*entry{
			"/": {isDir: true, mode: 0o755, modTime: time.Now()},
		},
	}
}

func (s *Sandbox) Backend() string { return "fake" }

// Capabilities reports a fully capable, isolated backend. The in-memory tree
// can never touch a host path, which is at least as strong as the namespaces
// the containerd backend reports, so the containment class matches it.
func (s *Sandbox) Capabilities() shepherd.SandboxCapabilities {
	return shepherd.SandboxCapabilities{
		Lifecycle:   true,
		Exec:        true,
		FileIO:      true,
		Diff:        true,
		Isolated:    true,
		Containment: shepherd.ContainContained,
	}
}

// Create provisions the sandbox. A second Create is an error: a caller that
// cannot tell whether it already created the sandbox is exactly the caller
// that leaks containers. When the spec names a Workdir it is created, the way
// a container's working directory exists once the container does — every
// in-band script the workspace runs assumes it.
func (s *Sandbox) Create(_ context.Context, spec shepherd.SandboxSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.destroyed:
		return ErrDestroyed
	case s.created:
		return ErrAlreadyCreated
	}
	if spec.Workdir != "" {
		clean, err := s.resolve(spec.Workdir, "/")
		if err != nil {
			return err
		}
		if err := s.mkdirParentsLocked(clean, 0o755); err != nil {
			return err
		}
	}
	s.created = true
	return nil
}

// Destroy releases the sandbox. It never touches anything it did not create —
// the tree lives in memory — but it refuses on an uncreated sandbox so a
// teardown without a start is as loud as a start without a teardown.
func (s *Sandbox) Destroy(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.destroyed:
		return ErrAlreadyDestroyed
	case !s.created:
		return ErrNotCreated
	}
	s.destroyed = true
	return nil
}

// Created and Destroyed report lifecycle state, for leak assertions: a
// session teardown test checks Destroyed and, more importantly, that it did
// not destroy twice or leak a still-created sandbox.
func (s *Sandbox) Created() bool   { s.mu.Lock(); defer s.mu.Unlock(); return s.created }
func (s *Sandbox) Destroyed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.destroyed }

// Execs returns every recorded ExecRequest, in order, including requests made
// while the sandbox was not usable (before Create or after Destroy) — an
// attempt against a dead sandbox is exactly the signal a lifecycle test wants.
func (s *Sandbox) Execs() []shepherd.ExecRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]shepherd.ExecRequest, len(s.execs))
	copy(out, s.execs)
	return out
}

// Exec interprets the request. A `sh -c script name args...` request runs
// through the interpreter in shell.go; any other command is an error, because
// the fake has no userland — tests that need a specific tool register ExecHook.
func (s *Sandbox) Exec(ctx context.Context, req shepherd.ExecRequest) (shepherd.ExecResult, error) {
	s.mu.Lock()
	s.execs = append(s.execs, req)
	err := s.usable()
	s.mu.Unlock()
	if err != nil {
		return shepherd.ExecResult{}, err
	}

	if s.ExecHook != nil {
		return s.ExecHook(req)
	}
	if req.Command != "sh" || len(req.Args) < 2 || req.Args[0] != "-c" {
		return shepherd.ExecResult{
			ExitCode: 127,
			Stderr:   fmt.Sprintf("sandboxfake: no such command: %s", req.Command),
		}, nil
	}
	return s.runSh(ctx, req), nil
}

// ReadFile returns a file's content, following symlinks like os.ReadFile.
func (s *Sandbox) ReadFile(ctx context.Context, p string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return nil, err
	}
	e, err := s.lookup(ctx, p, true)
	if err != nil {
		return nil, err
	}
	if e.isDir {
		return nil, fmt.Errorf("sandboxfake: read %s: is a directory", p)
	}
	out := make([]byte, len(e.data))
	copy(out, e.data)
	return out, nil
}

// WriteFile stores a file, creating parent directories like the containerd
// backend's WriteFile does. The perm argument may carry setuid/setgid/sticky
// bits (0o4000/0o2000/0o1000), which are preserved.
func (s *Sandbox) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return err
	}
	return s.writeFileLocked(ctx, p, data, perm)
}

func (s *Sandbox) writeFileLocked(_ context.Context, p string, data []byte, perm fs.FileMode) error {
	clean, err := s.resolve(p, "")
	if err != nil {
		return err
	}
	e, ok := s.files[clean]
	if ok && e.isDir {
		return fmt.Errorf("sandboxfake: write %s: is a directory", p)
	}
	if !ok {
		if err := s.mkdirParentsLocked(clean, 0o755); err != nil {
			return err
		}
	}
	e = &entry{mode: toMode(perm), data: append([]byte(nil), data...), modTime: time.Now()}
	s.files[clean] = e
	return nil
}

// MkdirAll creates a directory and its missing parents, for seeding. It is
// host-side test support, not part of shepherd.Sandbox.
func (s *Sandbox) MkdirAll(p string, perm fs.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean, err := s.resolve(p, "")
	if err != nil {
		return err
	}
	return s.mkdirParentsLocked(clean, perm)
}

// Symlink creates newname pointing at oldname, for seeding.
func (s *Sandbox) Symlink(oldname, newname string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean, err := s.resolve(newname, "")
	if err != nil {
		return err
	}
	if _, ok := s.files[clean]; ok {
		return fmt.Errorf("sandboxfake: symlink %s: already exists", newname)
	}
	if err := s.mkdirParentsLocked(clean, 0o755); err != nil {
		return err
	}
	s.files[clean] = &entry{isLink: true, target: oldname, mode: 0o777, modTime: time.Now()}
	return nil
}

// usable reports whether the sandbox can serve IO. Callers hold s.mu.
func (s *Sandbox) usable() error {
	switch {
	case s.destroyed:
		return ErrDestroyed
	case !s.created:
		return ErrNotCreated
	}
	return nil
}

// resolve cleans p and makes it absolute against cwd (POSIX, matching the
// substrate). Callers hold s.mu.
func (s *Sandbox) resolve(p, cwd string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("sandboxfake: empty path")
	}
	if !strings.HasPrefix(p, "/") {
		if cwd == "" {
			cwd = "/"
		}
		p = cwd + "/" + p
	}
	return path.Clean(p), nil
}

// mkdirParentsLocked creates dir and every missing ancestor as a directory
// entry. Existing non-directory ancestors are an error.
func (s *Sandbox) mkdirParentsLocked(dir string, perm fs.FileMode) error {
	if dir == "/" {
		return nil
	}
	var ancestors []string
	for d := dir; d != "/" && d != "."; d = path.Dir(d) {
		if _, ok := s.files[d]; ok {
			break
		}
		ancestors = append(ancestors, d)
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		d := ancestors[i]
		if e, ok := s.files[d]; ok {
			if !e.isDir {
				return fmt.Errorf("sandboxfake: %s: not a directory", d)
			}
			continue
		}
		parent := path.Dir(d)
		if e, ok := s.files[parent]; ok && !e.isDir {
			return fmt.Errorf("sandboxfake: %s: not a directory", parent)
		}
		s.files[d] = &entry{isDir: true, mode: perm.Perm(), modTime: time.Now()}
	}
	return nil
}

const maxSymlinkDepth = 40

// lookup finds the entry for p. With follow, a final symlink is resolved to
// its target (like os.Stat); without it, the link itself is returned (like
// os.Lstat). Intermediate path components are not resolved as symlinks —
// nothing on the workspace path walks through a symlinked directory, and a
// fake that grew a full path walker would be a second implementation of the
// thing under test. A missing or over-deep path is an error a caller can
// inspect with errors.Is against fs.ErrNotExist.
func (s *Sandbox) lookup(_ context.Context, p string, follow bool) (*entry, error) {
	clean, err := s.resolve(p, "")
	if err != nil {
		return nil, err
	}
	e, ok := s.files[clean]
	if !ok {
		return nil, newPathError("sandboxfake", p, fs.ErrNotExist)
	}
	if !follow || !e.isLink {
		return e, nil
	}
	for depth := 0; e.isLink; depth++ {
		if depth >= maxSymlinkDepth {
			return nil, fmt.Errorf("sandboxfake: %s: too many levels of symbolic links", p)
		}
		target := e.target
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(clean), target)
		}
		e, ok = s.files[target]
		if !ok {
			return nil, newPathError("sandboxfake", p, fs.ErrNotExist)
		}
	}
	return e, nil
}

// children returns the sorted immediate children of dir. Callers hold s.mu.
func (s *Sandbox) children(dir string) []string {
	prefix := dir
	if prefix != "/" {
		prefix += "/"
	}
	var names []string
	for p := range s.files {
		if p == dir || !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		names = append(names, rest)
	}
	sort.Strings(names)
	return names
}

// removeLocked deletes p (and everything under it, when it is a directory —
// the fake has no -r distinction at this layer).
func (s *Sandbox) removeLocked(p string) {
	clean, _ := s.resolve(p, "")
	delete(s.files, clean)
	if clean != "/" {
		prefix := clean + "/"
		for q := range s.files {
			if strings.HasPrefix(q, prefix) {
				delete(s.files, q)
			}
		}
	}
}

// toMode splits a possibly-setuid octal perm (0o4755) into fs.FileMode bits.
// fs.FileMode(0o4755) does not carry ModeSetuid: the high octal bits overlap
// fs.ModeDir and friends, so they must be mapped explicitly.
func toMode(perm fs.FileMode) fs.FileMode {
	m := perm.Perm()
	if perm&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if perm&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if perm&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

// modeOctal is toMode's inverse, matching GNU stat's %a: 0o4755 renders as
// "4755", plain 0o644 as "644".
func modeOctal(m fs.FileMode) string {
	u := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		u |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		u |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		u |= 0o1000
	}
	return strconv.FormatUint(uint64(u), 8)
}

// newPathError wraps err with an operation and path, so errors.Is works
// against the sentinel (fs.ErrNotExist) while the message still names the
// path.
func newPathError(op, p string, err error) error {
	return fmt.Errorf("%s: %s: %w", op, p, err)
}

// kindString returns GNU stat's %F spelling for the entry.
func (e *entry) kindString() string {
	switch {
	case e.isDir:
		return "directory"
	case e.isLink:
		return "symbolic link"
	default:
		return "regular file"
	}
}

// kindChar returns find's %y and stat's %y type character.
func (e *entry) kindChar() string {
	switch {
	case e.isDir:
		return "d"
	case e.isLink:
		return "l"
	default:
		return "f"
	}
}

// size mirrors stat's %s: directories report 4096 and symlinks the target
// length, the values GNU stat produces on common filesystems.
func (e *entry) size() int64 {
	switch {
	case e.isDir:
		return 4096
	case e.isLink:
		return int64(len(e.target))
	default:
		return int64(len(e.data))
	}
}
