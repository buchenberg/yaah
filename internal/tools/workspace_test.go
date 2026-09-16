package tools

import (
	"context"
	"encoding/json"
	"errors"
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

// TestRegistry_SetWorkspace_RefusesIsolated pins the isolation gate. Selecting a
// non-local workspace while any built-in tool still touches the host would give
// the caller a containment guarantee that is not real, so it is refused.
func TestRegistry_SetWorkspace_RefusesIsolated(t *testing.T) {
	reg := NewEmptyRegistry()
	rec := &wsRecorder{}
	reg.Register(rec)
	// A host-only tool that can never be isolated, so the gate has something to
	// find. (Every migratable tool has been migrated, so an unmigrated filesystem
	// tool is no longer available to use here.)
	reg.Register(NewLeafTool("go_refactor"))
	reg.SetPathValidator(&PathValidator{WorkDir: t.TempDir()})

	err := reg.SetWorkspace(newSandboxWS(&fakeSandbox{}))
	if err == nil {
		t.Fatal("an isolated workspace must be refused")
	}
	if !strings.Contains(err.Error(), "go_refactor") {
		t.Errorf("error should name the disqualifying tool, got %v", err)
	}
	// The refusal must not half-apply: the derived local workspace stays.
	if rec.got == nil || !rec.got.Local() {
		t.Error("a refused SetWorkspace must leave the previous workspace in place")
	}
}

// TestRegistry_SetWorkspace_AllowsIsolatedWhenClean verifies the gate is precise
// rather than blanket: a registry holding only migrated tools can be isolated.
// Without this, isolation would be unreachable even once migration finishes.
func TestRegistry_SetWorkspace_AllowsIsolatedWhenClean(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.Register(NewLeafTool("read"))  // migrated
	reg.Register(NewLeafTool("write")) // migrated

	if err := reg.SetWorkspace(newSandboxWS(&fakeSandbox{})); err != nil {
		t.Fatalf("a registry of migrated tools must accept an isolated workspace: %v", err)
	}
}

// TestRegistry_EveryToolIsClassified is what makes the precise gate trustworthy:
// every registered tool must be a FilesystemTool, a HostOnlyTool, or named in the
// non-filesystem allowlist. Adding a tool without classifying it fails here,
// rather than silently becoming an isolation hole.
func TestRegistry_EveryToolIsClassified(t *testing.T) {
	// A claim that the tool cannot touch the host. Adding a name here is
	// deliberate; that is the point.
	nonFilesystem := map[string]bool{
		"question":  true,
		"webfetch":  true,
		"http":      true,
		"calculate": true,
	}

	reg := NewRegistry()
	if len(reg.tools) == 0 {
		t.Fatal("no tools registered: the classification check would be vacuous")
	}
	for name, tool := range reg.tools {
		if _, ok := tool.(FilesystemTool); ok {
			continue
		}
		if _, ok := tool.(HostOnlyTool); ok {
			continue
		}
		if !nonFilesystem[name] {
			t.Errorf("tool %q is neither a FilesystemTool nor a known non-filesystem tool: classify it as one or the other", name)
		}
	}
}

// TestRegistry_SetWorkspace_AcceptsLocal verifies a local workspace is applied
// to every migrated tool.
func TestRegistry_SetWorkspace_AcceptsLocal(t *testing.T) {
	reg := NewEmptyRegistry()
	rec := &wsRecorder{}
	reg.Register(rec)
	reg.SetPathValidator(&PathValidator{WorkDir: t.TempDir()})

	local := newLocalWorkspace(&PathValidator{WorkDir: "/tmp/other"})
	if err := reg.SetWorkspace(local); err != nil {
		t.Fatalf("a local workspace must be accepted: %v", err)
	}
	if rec.got != local {
		t.Errorf("workspace = %T, want the supplied local workspace", rec.got)
	}
	// The validator stays, because unmigrated tools still use it.
	if rec.pv == nil {
		t.Error("the validator must not be cleared by SetWorkspace")
	}
}

// TestRegistry_UnmigratedFilesystemTools tracks the migration backlog: migrated
// tools must not appear, host-only tools must.
func TestRegistry_UnmigratedFilesystemTools(t *testing.T) {
	reg := NewRegistry()
	// Host-only tools are runtime-wired in production, so add one to exercise the
	// separate reporting path.
	reg.Register(&RoleTool{})
	unmigrated := reg.UnmigratedFilesystemTools()

	isUnmigrated := func(name string) bool {
		for _, n := range unmigrated {
			if n == name {
				return true
			}
		}
		return false
	}

	for _, name := range []string{"read", "write", "edit", "delete", "patch", "bash", "ls", "grep", "glob", "sed", "replace", "git", "go_mod", "go_test", "staticcheck", "bisect"} {
		if isUnmigrated(name) {
			t.Errorf("%s is migrated and must not be listed as unmigrated", name)
		}
	}
	// Every migratable tool has been migrated. If this ever fails, a new tool was
	// added without being migrated.
	if len(unmigrated) != 0 {
		t.Errorf("unmigrated = %v, want none: all migratable filesystem tools are converted", unmigrated)
	}
	// Host-only tools are not migration debt and must not be reported as such.
	for _, name := range reg.HostOnlyFilesystemTools() {
		if isUnmigrated(name) {
			t.Errorf("%s is host-only by design and must not be listed as needing migration", name)
		}
	}
	if len(reg.HostOnlyFilesystemTools()) == 0 {
		t.Error("expected host-only tools to be reported separately")
	}
}

// --- sandbox workspace ---

// fakeSandbox is a minimal shepherd.Sandbox that records calls.
type fakeSandbox struct {
	execs  []shepherd.ExecRequest
	stdout string
	stderr string
	exit   int
	// execErr, when non-nil, simulates a transport failure: the command never
	// reached (or never ran in) the sandbox.
	execErr error
	files   map[string][]byte
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
	if f.execErr != nil {
		return shepherd.ExecResult{}, f.execErr
	}
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

// newSandboxWS returns a real sandboxWorkspace over the given sandbox, so the
// tests exercise the production implementation rather than a stand-in.
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

// TestSandboxWorkspace_ReadDirIncludesDotfiles pins that the in-band listing
// surfaces hidden entries: os.ReadDir returns them on the host, and a sandbox
// that silently drops ".env" or ".github" would make glob and grep miss files
// the caller asked about.
func TestSandboxWorkspace_ReadDirIncludesDotfiles(t *testing.T) {
	sb := &fakeSandbox{stdout: ".env\n.git/\nREADME.md\nsrc/\n"}
	ws := newSandboxWS(sb)

	entries, err := ws.ReadDir(context.Background(), "/workspace")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(sb.execs) != 1 {
		t.Fatalf("execs = %d, want 1", len(sb.execs))
	}
	// ReadDir shells out via `sh -c`, so the listing flags live in the script.
	if script := sb.execs[0].Args[1]; !strings.Contains(script, "ls -1Ap") {
		t.Errorf("listing script %q must use ls -1Ap (dotfiles included, . and .. excluded)", script)
	}

	byName := map[string]bool{}
	for _, e := range entries {
		byName[e.Name()] = e.IsDir()
	}
	for name, wantDir := range map[string]bool{
		".env": false, ".git": true, "README.md": false, "src": true,
	} {
		got, ok := byName[name]
		if !ok {
			t.Errorf("entry %q missing from listing", name)
			continue
		}
		if got != wantDir {
			t.Errorf("entry %q IsDir = %v, want %v", name, got, wantDir)
		}
	}
}

// TestSandboxWorkspace_ExecTransportErrorReportsExitMinusOne pins the
// Workspace interface contract: ExitCode is -1 when the command could not be
// started at all, so callers (staticcheck, diff) can distinguish "never ran"
// from "ran and failed".
func TestSandboxWorkspace_ExecTransportErrorReportsExitMinusOne(t *testing.T) {
	sb := &fakeSandbox{execErr: errors.New("container gone")}
	ws := newSandboxWS(sb)

	res, err := ws.Exec(context.Background(), ExecRequest{Command: "go", Args: []string{"version"}})
	if err == nil {
		t.Fatal("a transport failure must surface as an error")
	}
	if res.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a command that never ran", res.ExitCode)
	}
}

// TestSandboxWorkspace_RootSlashContainsWholeContainer pins the "/" edge case:
// trimming must not collapse the root to an empty prefix, which would disable
// containment entirely.
func TestSandboxWorkspace_RootSlashContainsWholeContainer(t *testing.T) {
	ws := newSandboxWorkspace(&fakeSandbox{}, "/")

	if ws.root != "/" {
		t.Fatalf("root = %q, want %q", ws.root, "/")
	}
	got, err := ws.ResolvePath("etc/passwd")
	if err != nil || got != "/etc/passwd" {
		t.Errorf("ResolvePath(etc/passwd) = %q, %v; want /etc/passwd, nil", got, err)
	}
	if got, err := ws.ResolvePath("/etc/passwd"); err != nil || got != "/etc/passwd" {
		t.Errorf("ResolvePath(/etc/passwd) = %q, %v; want /etc/passwd, nil", got, err)
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
