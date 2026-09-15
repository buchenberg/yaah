package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

// --- local workspace (host filesystem) ---

func TestLocalWorkspace_ResolvePathUsesValidator(t *testing.T) {
	ws := t.TempDir()
	pv := NewPathValidator(ws, false, nil)
	// Relative paths resolve against WorkDir; without it they resolve against
	// the process cwd and land outside the workspace.
	pv.WorkDir = pv.WorkspaceRoot
	lw := newLocalWorkspace(pv)

	got, err := lw.ResolvePath("sub/file.txt")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want, err := resolvePathWithPV(pv, "sub/file.txt")
	if err != nil {
		t.Fatalf("resolvePathWithPV: %v", err)
	}
	if got != want {
		t.Errorf("local workspace resolution = %q, want %q (must match the validator)", got, want)
	}

	// Containment is the validator's, not a reimplementation.
	if _, err := lw.ResolvePath(filepath.Join(ws, "..", "escape.txt")); err == nil {
		t.Error("parent escape must be rejected")
	}
}

func TestLocalWorkspace_WorkDirAndLocal(t *testing.T) {
	dir := t.TempDir()
	lw := newLocalWorkspace(&PathValidator{WorkDir: dir})

	if !lw.Local() {
		t.Error("the local workspace is the host filesystem")
	}
	if lw.WorkDir() != dir {
		t.Errorf("WorkDir = %q, want %q", lw.WorkDir(), dir)
	}
	// A nil validator must not panic.
	if got := newLocalWorkspace(nil).WorkDir(); got != "" {
		t.Errorf("nil-validator WorkDir = %q, want empty", got)
	}
}

func TestLocalWorkspace_ShellMatchesPlatform(t *testing.T) {
	shell, flag := newLocalWorkspace(nil).Shell()
	if runtime.GOOS == "windows" {
		if shell != "pwsh" && shell != "powershell" {
			t.Errorf("windows shell = %q, want pwsh or powershell", shell)
		}
		if flag != "-Command" {
			t.Errorf("windows flag = %q, want -Command", flag)
		}
		return
	}
	if shell != "sh" || flag != "-c" {
		t.Errorf("posix shell = (%q, %q), want (sh, -c)", shell, flag)
	}
}

// TestLocalWorkspace_WriteFileIsAtomic pins the crash-safety guarantee: the write
// goes through a temp file and rename, and leaves no temp file behind.
func TestLocalWorkspace_WriteFileIsAtomic(t *testing.T) {
	dir := t.TempDir()
	lw := newLocalWorkspace(nil)
	ctx := context.Background()
	target := filepath.Join(dir, "f.txt")

	if err := lw.WriteFile(ctx, target, []byte("first"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "first" {
		t.Errorf("content = %q, want first", got)
	}
	// Mode is only meaningful on POSIX: Windows tracks just the read-only bit, so
	// asserting 0600 there would test the OS, not the writer.
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(target); err != nil {
			t.Fatalf("stat: %v", err)
		} else if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", st.Mode().Perm())
		}

		// An overwrite preserves the existing mode rather than reapplying perm.
		if err := lw.WriteFile(ctx, target, []byte("second"), 0o777); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		if st, _ := os.Stat(target); st.Mode().Perm() != 0o600 {
			t.Errorf("mode after overwrite = %v, want 0600 preserved", st.Mode().Perm())
		}
	} else if err := lw.WriteFile(ctx, target, []byte("second"), 0o777); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "second" {
		t.Errorf("content after overwrite = %q, want second", got)
	}

	// No temp files may survive the rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory has %v, want only f.txt (temp file leaked?)", names)
	}
}

func TestLocalWorkspace_ExecCombinesOutputAndReportsExit(t *testing.T) {
	lw := newLocalWorkspace(nil)
	ctx := context.Background()

	if runtime.GOOS == "windows" {
		res, err := lw.Exec(ctx, ExecRequest{Command: "cmd", Args: []string{"/c", "echo hi & exit /b 3"}})
		if err != nil && res.ExitCode == 0 {
			t.Fatalf("Exec: %v", err)
		}
		if !strings.Contains(res.Stdout, "hi") {
			t.Errorf("stdout = %q, want it to contain hi", res.Stdout)
		}
		return
	}

	res, err := lw.Exec(ctx, ExecRequest{Command: "sh", Args: []string{"-c", "echo out; echo err 1>&2; exit 3"}})
	if err == nil {
		t.Error("a non-zero exit must surface as an error")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	// Combined output preserves interleaving, which is what shell tools relied on.
	if !strings.Contains(res.Stdout, "out") || !strings.Contains(res.Stdout, "err") {
		t.Errorf("stdout = %q, want combined out+err", res.Stdout)
	}
}

func TestLocalWorkspace_ExecHonoursCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	dir := t.TempDir()
	lw := newLocalWorkspace(&PathValidator{WorkDir: dir})

	res, err := lw.Exec(context.Background(), ExecRequest{Command: "sh", Args: []string{"-c", "pwd"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := strings.TrimSpace(res.Stdout)
	wantReal, errA := filepath.EvalSymlinks(dir)
	gotReal, errB := filepath.EvalSymlinks(got)
	if errA == nil && errB == nil {
		if gotReal != wantReal {
			t.Errorf("cwd = %q, want %q", gotReal, wantReal)
		}
	}
}

func TestWorkspaceOf_FallsBackToLocalValidator(t *testing.T) {
	dir := t.TempDir()
	ws := workspaceOf(nil, &PathValidator{WorkDir: dir})

	if !ws.Local() {
		t.Error("a nil workspace must fall back to the local filesystem")
	}
	if ws.WorkDir() != dir {
		t.Errorf("WorkDir = %q, want the validator's %q", ws.WorkDir(), dir)
	}
}

// --- registry injection ---

// wsRecorder records what the registry injected.
type wsRecorder struct {
	Tool
	got Workspace
	pv  *PathValidator
}

func (r *wsRecorder) Name() string                                    { return "ws-recorder" }
func (r *wsRecorder) SetWorkspace(ws Workspace)                       { r.got = ws }
func (r *wsRecorder) SetPathValidator(pv *PathValidator)              { r.pv = pv }
func (r *wsRecorder) Execute(context.Context, string) (string, error) { return "", nil }
func (r *wsRecorder) Description() string                             { return "" }
func (r *wsRecorder) Schema() json.RawMessage                         { return nil }

// TestRegistry_DerivesLocalWorkspaceFromValidator pins the migration behaviour:
// every existing caller sets only a PathValidator, and migrated tools must still
// get a working workspace.
func TestRegistry_DerivesLocalWorkspaceFromValidator(t *testing.T) {
	reg := NewEmptyRegistry()
	rec := &wsRecorder{}
	reg.Register(rec)

	pv := &PathValidator{WorkDir: t.TempDir()}
	reg.SetPathValidator(pv)

	if rec.pv != pv {
		t.Error("the validator must still be injected for unmigrated tools")
	}
	if rec.got == nil {
		t.Fatal("a workspace must be derived from the validator")
	}
	if !rec.got.Local() {
		t.Error("the derived workspace must be the local filesystem")
	}
	if rec.got.WorkDir() != pv.WorkDir {
		t.Errorf("derived WorkDir = %q, want %q", rec.got.WorkDir(), pv.WorkDir)
	}

	// Registering after SetPathValidator also injects.
	late := &wsRecorder{}
	reg.Register(late)
	if late.got == nil {
		t.Error("a tool registered later must be injected too")
	}
}

func TestRegistry_SetWorkspaceOverrides(t *testing.T) {
	reg := NewEmptyRegistry()
	rec := &wsRecorder{}
	reg.Register(rec)
	reg.SetPathValidator(&PathValidator{WorkDir: t.TempDir()})

	custom := &fakeSandboxWorkspace{root: "/workspace"}
	reg.SetWorkspace(custom)

	if rec.got != custom {
		t.Errorf("SetWorkspace must override the derived workspace, got %T", rec.got)
	}
	// The validator stays, because unmigrated tools still use it.
	if rec.pv == nil {
		t.Error("the validator must not be cleared by SetWorkspace")
	}
}

// --- sandbox workspace ---

// fakeSandbox is a minimal shepherd.Sandbox that records calls.
type fakeSandbox struct {
	execs  []shepherd.ExecRequest
	stdout string
	stderr string
	exit   int
	files  map[string][]byte
}

func (f *fakeSandbox) Backend() string { return "fake" }
func (f *fakeSandbox) Capabilities() shepherd.SandboxCapabilities {
	return shepherd.SandboxCapabilities{}
}
func (f *fakeSandbox) Create(context.Context, shepherd.SandboxSpec) error { return nil }
func (f *fakeSandbox) Destroy(context.Context) error                      { return nil }
func (f *fakeSandbox) Capture(context.Context) (shepherd.WorkspaceState, error) {
	return shepherd.WorkspaceState{}, nil
}
func (f *fakeSandbox) Apply(context.Context, shepherd.WorkspaceState) error { return nil }
func (f *fakeSandbox) Diff(context.Context, shepherd.WorkspaceState, int) (string, []string, error) {
	return "", nil, nil
}

// Exec records the request and returns canned output, so tests can assert what
// the workspace asked the sandbox to do.
func (f *fakeSandbox) Exec(_ context.Context, req shepherd.ExecRequest) (shepherd.ExecResult, error) {
	f.execs = append(f.execs, req)
	return shepherd.ExecResult{ExitCode: f.exit, Stdout: f.stdout, Stderr: f.stderr}, nil
}

func (f *fakeSandbox) ReadFile(_ context.Context, path string) ([]byte, error) {
	if data, ok := f.files[path]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeSandbox) WriteFile(_ context.Context, path string, data []byte, _ fs.FileMode) error {
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[path] = data
	return nil
}

// fakeSandboxWorkspace is a Workspace used only to prove override plumbing.
type fakeSandboxWorkspace struct{ root string }

func (f *fakeSandboxWorkspace) ResolvePath(p string) (string, error) { return p, nil }
func (f *fakeSandboxWorkspace) WorkDir() string                      { return f.root }
func (f *fakeSandboxWorkspace) Local() bool                          { return false }
func (f *fakeSandboxWorkspace) Shell() (string, string)              { return "sh", "-c" }
func (f *fakeSandboxWorkspace) ReadFile(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (f *fakeSandboxWorkspace) WriteFile(context.Context, string, []byte, fs.FileMode) error {
	return nil
}
func (f *fakeSandboxWorkspace) Stat(context.Context, string) (fs.FileInfo, error) {
	return nil, nil
}
func (f *fakeSandboxWorkspace) Remove(context.Context, string) error                { return nil }
func (f *fakeSandboxWorkspace) MkdirAll(context.Context, string, fs.FileMode) error { return nil }
func (f *fakeSandboxWorkspace) Exec(context.Context, ExecRequest) (ExecResult, error) {
	return ExecResult{}, nil
}

func newSandboxWS(sb shepherd.Sandbox) *sandboxWorkspace {
	return newSandboxWorkspace(sb, "/workspace")
}

func TestSandboxWorkspace_ResolvePathContainment(t *testing.T) {
	ws := newSandboxWS(&fakeSandbox{})

	// Relative paths resolve against the container root.
	got, err := ws.ResolvePath("sub/f.txt")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if got != "/workspace/sub/f.txt" {
		t.Errorf("resolved = %q, want /workspace/sub/f.txt", got)
	}

	// Absolute paths inside the root are kept.
	if got, err := ws.ResolvePath("/workspace/other.txt"); err != nil || got != "/workspace/other.txt" {
		t.Errorf("absolute inside root = %q, %v", got, err)
	}

	// Escapes are rejected, including through .. traversal.
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "../../root"} {
		if _, err := ws.ResolvePath(p); err == nil {
			t.Errorf("ResolvePath(%q) must be rejected", p)
		}
	}
	if _, err := ws.ResolvePath("  "); err == nil {
		t.Error("a blank path must be rejected")
	}

	// The root itself is allowed.
	if _, err := ws.ResolvePath("/workspace"); err != nil {
		t.Errorf("the root itself must resolve: %v", err)
	}
}

func TestSandboxWorkspace_LocalAndShell(t *testing.T) {
	ws := newSandboxWS(&fakeSandbox{})
	if ws.Local() {
		t.Error("a sandbox workspace is not the host filesystem")
	}
	shell, flag := ws.Shell()
	if shell != "sh" || flag != "-c" {
		t.Errorf("shell = (%q, %q), want (sh, -c)", shell, flag)
	}
	// An empty root must default rather than produce a relative path.
	if got := newSandboxWorkspace(&fakeSandbox{}, "").WorkDir(); got != "/workspace" {
		t.Errorf("default root = %q, want /workspace", got)
	}
}

// TestSandboxWorkspace_WriteFileIsAtomicInBand pins that the atomic write happens
// inside the sandbox (temp file plus rename) and that content travels on stdin
// rather than the command line.
func TestSandboxWorkspace_WriteFileIsAtomicInBand(t *testing.T) {
	sb := &fakeSandbox{}
	ws := newSandboxWS(sb)

	content := []byte("payload\nrm -rf /\n")
	if err := ws.WriteFile(context.Background(), "/workspace/f.txt", content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if len(sb.execs) != 1 {
		t.Fatalf("execs = %d, want 1", len(sb.execs))
	}
	req := sb.execs[0]
	if req.Command != "sh" {
		t.Errorf("command = %q, want sh", req.Command)
	}
	script := req.Args[1]
	if !strings.Contains(script, "mv -f") || !strings.Contains(script, ".tmp.") {
		t.Errorf("script must write a temp file and rename it: %q", script)
	}
	if !strings.Contains(script, `"$1"`) {
		t.Errorf("script must take the path from $1, got %q", script)
	}
	if strings.Contains(script, "payload") {
		t.Error("content must not be interpolated into the script")
	}
	if string(req.Stdin) != string(content) {
		t.Errorf("stdin = %q, want the content", req.Stdin)
	}
	if req.Cwd != "/workspace" {
		t.Errorf("cwd = %q, want the workspace root", req.Cwd)
	}
}

func TestSandboxWorkspace_ReadFileDelegates(t *testing.T) {
	sb := &fakeSandbox{files: map[string][]byte{"/workspace/a.txt": []byte("hello")}}
	ws := newSandboxWS(sb)

	got, err := ws.ReadFile(context.Background(), "/workspace/a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
}

// TestSandboxWorkspace_StatParsesInBandOutput verifies the POSIX stat output is
// mapped onto fs.FileInfo without a host stat call.
func TestSandboxWorkspace_StatParsesInBandOutput(t *testing.T) {
	sb := &fakeSandbox{stdout: "regular file|42|644|1700000000"}
	ws := newSandboxWS(sb)

	info, err := ws.Stat(context.Background(), "/workspace/f.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != 42 {
		t.Errorf("Size = %d, want 42", info.Size())
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("Mode = %v, want 0644", info.Mode().Perm())
	}
	if info.IsDir() {
		t.Error("a regular file must not report IsDir")
	}
	if info.Name() != "f.txt" {
		t.Errorf("Name = %q, want f.txt", info.Name())
	}

	// A directory is distinguished by the %F field.
	dirSB := &fakeSandbox{stdout: "directory|4096|755|1700000000"}
	dirInfo, err := newSandboxWS(dirSB).Stat(context.Background(), "/workspace/d")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !dirInfo.IsDir() {
		t.Error("a directory must report IsDir")
	}

	// Unparseable output is an error, not a zero-valued FileInfo.
	badSB := &fakeSandbox{stdout: "garbage"}
	if _, err := newSandboxWS(badSB).Stat(context.Background(), "/workspace/x"); err == nil {
		t.Error("unexpected stat output must fail")
	}
}

func TestSandboxWorkspace_NonZeroExitIsAnError(t *testing.T) {
	sb := &fakeSandbox{exit: 2, stderr: "boom"}
	ws := newSandboxWS(sb)

	if err := ws.Remove(context.Background(), "/workspace/f.txt"); err == nil {
		t.Fatal("a non-zero exit must surface as an error")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry stderr, got %v", err)
	}
}

func TestSandboxWorkspace_ExecDefaultsCwdToRoot(t *testing.T) {
	sb := &fakeSandbox{}
	ws := newSandboxWS(sb)

	if _, err := ws.Exec(context.Background(), ExecRequest{Command: "go", Args: []string{"test", "./..."}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	req := sb.execs[0]
	if req.Cwd != "/workspace" {
		t.Errorf("cwd = %q, want the workspace root", req.Cwd)
	}
	if req.Command != "go" {
		t.Errorf("command = %q, want go", req.Command)
	}
}

func TestRequireLocal(t *testing.T) {
	if err := requireLocal(newLocalWorkspace(nil), "powershell"); err != nil {
		t.Errorf("a local workspace must be accepted: %v", err)
	}
	err := requireLocal(newSandboxWS(&fakeSandbox{}), "powershell")
	if err == nil {
		t.Fatal("a sandbox workspace must be rejected for host-only tools")
	}
	if !strings.Contains(err.Error(), "powershell") {
		t.Errorf("error should name the tool: %v", err)
	}
}
