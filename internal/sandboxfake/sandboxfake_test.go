package sandboxfake

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"path"
	"strings"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/goatherd-runtime/effect"
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
)

func newCreated(t *testing.T) *Sandbox {
	t.Helper()
	s := New()
	if err := s.Create(context.Background(), goatherdsandbox.SandboxSpec{Workdir: "/workspace"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s
}

// runScript executes `sh -c script name args...`, the exact wire shape
// sandboxWorkspace uses for every in-band operation.
func runScript(t *testing.T, s *Sandbox, script string, stdin []byte, args ...string) goatherdsandbox.ExecResult {
	t.Helper()
	res, err := s.Exec(context.Background(), goatherdsandbox.ExecRequest{
		Command: "sh",
		Args:    append([]string{"-c", script, "shepherd"}, args...),
		Cwd:     "/workspace",
		Stdin:   stdin,
	})
	if err != nil {
		t.Fatalf("Exec(%q): %v", script, err)
	}
	return res
}

func TestCapabilitiesReportFullyIsolatedBackend(t *testing.T) {
	c := New().Capabilities()
	if !c.Lifecycle || !c.Exec || !c.FileIO || !c.Diff || !c.Isolated {
		t.Errorf("capabilities = %+v, want every capability true", c)
	}
	if c.Containment != shepherdtrace.ContainContained {
		t.Errorf("containment = %q, want %q (matching the containerd backend)", c.Containment, shepherdtrace.ContainContained)
	}
}

func TestLifecycleGates(t *testing.T) {
	ctx := context.Background()
	s := New()

	// Before Create: every operation is refused, but Exec is still recorded.
	if _, err := s.Exec(ctx, goatherdsandbox.ExecRequest{Command: "sh", Args: []string{"-c", "true"}}); !errors.Is(err, ErrNotCreated) {
		t.Errorf("Exec before Create = %v, want ErrNotCreated", err)
	}
	if len(s.Execs()) != 1 {
		t.Errorf("a refused Exec must still be recorded, got %d", len(s.Execs()))
	}
	if err := s.Destroy(ctx); !errors.Is(err, ErrNotCreated) {
		t.Errorf("Destroy before Create = %v, want ErrNotCreated", err)
	}
	if _, err := s.Capture(ctx); !errors.Is(err, ErrNotCreated) {
		t.Errorf("Capture before Create = %v, want ErrNotCreated", err)
	}

	if err := s.Create(ctx, goatherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Create(ctx, goatherdsandbox.SandboxSpec{}); !errors.Is(err, ErrAlreadyCreated) {
		t.Errorf("second Create = %v, want ErrAlreadyCreated", err)
	}
	if !s.Created() || s.Destroyed() {
		t.Errorf("created=%v destroyed=%v, want true false", s.Created(), s.Destroyed())
	}

	if err := s.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := s.ReadFile(ctx, "/x"); !errors.Is(err, ErrDestroyed) {
		t.Errorf("ReadFile after Destroy = %v, want ErrDestroyed", err)
	}
	if _, err := s.Exec(ctx, goatherdsandbox.ExecRequest{Command: "sh", Args: []string{"-c", "true"}}); !errors.Is(err, ErrDestroyed) {
		t.Errorf("Exec after Destroy = %v, want ErrDestroyed", err)
	}
	if err := s.Destroy(ctx); !errors.Is(err, ErrAlreadyDestroyed) {
		t.Errorf("second Destroy = %v, want ErrAlreadyDestroyed", err)
	}
}

func TestFileIORoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newCreated(t)

	// WriteFile creates parent directories, like the containerd backend.
	if err := s.WriteFile(ctx, "/workspace/sub/f.txt", []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := s.ReadFile(ctx, "/workspace/sub/f.txt")
	if err != nil || string(got) != "hello" {
		t.Fatalf("ReadFile = %q, %v; want hello", got, err)
	}
	if _, err := s.ReadFile(ctx, "/workspace/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile missing = %v, want fs.ErrNotExist", err)
	}

	// Setuid bits survive the write and surface in stat's %a.
	if err := s.WriteFile(ctx, "/workspace/suid", []byte("x"), 0o4755); err != nil {
		t.Fatalf("WriteFile suid: %v", err)
	}
	res := runScript(t, s, `stat -c %a -- "$1"`, nil, "/workspace/suid")
	if strings.TrimSpace(res.Stdout) != "4755" {
		t.Errorf("%%a = %q, want 4755", res.Stdout)
	}

	// Seeding helpers. A symlink's target resolves relative to the link's
	// directory when the final path component is a link.
	if err := s.MkdirAll("/workspace/d/nested", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := s.Symlink("sub/f.txt", "/workspace/link"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if data, err := s.ReadFile(ctx, "/workspace/link"); err != nil || string(data) != "hello" {
		t.Errorf("ReadFile through symlink = %q, %v; want hello (follow, like os.ReadFile)", data, err)
	}
}

// TestExecRunsTheAtomicWriteScript interprets the exact first script
// sandboxWorkspace.WriteFile emits and pins the resulting tree: the
// parent directory is mkdir -p'd from its positional (the host
// atomicWriteFile's MkdirAll parity), the base64 chunk lands in the
// mktemp file decoded, the scratch .b64 is removed, the EXIT trap is
// stood down so the temp survives (a mishandled `trap -` would surface
// as a stderr diagnostic at teardown), and the temp's name returns on
// stdout.
func TestExecRunsTheAtomicWriteScript(t *testing.T) {
	s := newCreated(t)
	script := `mkdir -p -- "$3" || exit 1
t=$(mktemp -- "$1.XXXXXX") || exit 1
trap 'rm -f -- "$t" "$t.b64"' EXIT
printf '%s' "$2" > "$t.b64" || exit 1
base64 -d "$t.b64" >> "$t" || exit 1
rm -f -- "$t.b64"
trap - EXIT
printf '%s' "$t"`

	res := runScript(t, s, script, nil, "/workspace/f.txt", base64.StdEncoding.EncodeToString([]byte("payload\n")), "/workspace")
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	// The trap must have been cleared, not merely made inert: a fake
	// that mishandles `trap -` runs the leftover trap body ("-") as a
	// command at teardown and leaves a stderr diagnostic behind.
	if res.Stderr != "" {
		t.Errorf("a stood-down trap must leave no diagnostic: stderr = %q", res.Stderr)
	}
	temp := strings.TrimSpace(res.Stdout)
	if !strings.HasPrefix(temp, "/workspace/f.txt.") {
		t.Fatalf("temp name = %q, want it beside the target", temp)
	}
	data, err := s.ReadFile(context.Background(), temp)
	if err != nil || string(data) != "payload\n" {
		t.Fatalf("content = %q, %v; want payload", data, err)
	}
	if _, err := s.ReadFile(context.Background(), temp+".b64"); err == nil {
		t.Error("the base64 scratch file must be removed")
	}

	// A mid-script failure must fire the still-armed EXIT trap: an
	// invalid base64 chunk fails the decode, the failed run's temp and
	// scratch must be gone, and the successful run's temp must be the
	// only survivor — counting entries cannot tell kept-from-removed,
	// so the survivors are named.
	res = runScript(t, s, script, nil, "/workspace/f.txt", "!!!not base64!!!", "/workspace")
	if res.ExitCode == 0 {
		t.Error("an invalid chunk must fail the script")
	}
	if _, err := s.ReadFile(context.Background(), temp); err != nil || string(data) != "payload\n" {
		t.Fatalf("the successful run's temp must survive the failed run: %v", err)
	}
	list := runScript(t, s, `find "$1" -mindepth 1 -maxdepth 1 -printf '%f\n'`, nil, "/workspace")
	if got := strings.Fields(list.Stdout); len(got) != 1 || got[0] != path.Base(temp) || strings.HasSuffix(got[0], ".b64") {
		t.Errorf("survivors after failure = %v, want exactly [%s] and no scratch", got, path.Base(temp))
	}

	// The mkdir -p line lets the same script serve a target whose parent
	// does not exist yet: the parent is created from its positional and
	// the temp lands beside the nested target.
	res = runScript(t, s, script, nil, "/workspace/fresh/sub/g.txt", base64.StdEncoding.EncodeToString([]byte("deep\n")), "/workspace/fresh/sub")
	if res.ExitCode != 0 {
		t.Fatalf("nested exit = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	nestedTemp := strings.TrimSpace(res.Stdout)
	if !strings.HasPrefix(nestedTemp, "/workspace/fresh/sub/g.txt.") {
		t.Errorf("nested temp name = %q, want it beside the target", nestedTemp)
	}
	if nested, err := s.ReadFile(context.Background(), nestedTemp); err != nil || string(nested) != "deep\n" {
		t.Errorf("nested content = %q, %v; want deep", nested, err)
	}
}

func TestExecRunsTheStatScripts(t *testing.T) {
	s := newCreated(t)
	ctx := context.Background()
	if err := s.WriteFile(ctx, "/workspace/f.txt", []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.MkdirAll("/workspace/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Symlink("d", "/workspace/link"); err != nil {
		t.Fatal(err)
	}

	lstatScript := `[ -e "$1" ] || [ -L "$1" ] || exit 42
stat -c "$2" -- "$1"`
	statScript := `[ -e "$1" ] || exit 42
stat -L -c "$2" -- "$1"`

	// Lstat reports the link itself.
	res := runScript(t, s, lstatScript, nil, "/workspace/link", "%F|%s|%a")
	if got := strings.TrimSpace(res.Stdout); got != "symbolic link|1|777" {
		t.Errorf("lstat output = %q, want symbolic link|1|777", got)
	}
	// Stat follows it to the directory.
	res = runScript(t, s, statScript, nil, "/workspace/link", "%F|%s|%a")
	if got := strings.TrimSpace(res.Stdout); got != "directory|4096|755" {
		t.Errorf("stat output = %q, want directory|4096|755", got)
	}
	// A regular file parses with size and mode.
	res = runScript(t, s, statScript, nil, "/workspace/f.txt", "%F|%s|%a|%Y")
	if got := strings.TrimSpace(res.Stdout); !strings.HasPrefix(got, "regular file|5|644|") {
		t.Errorf("stat output = %q, want regular file|5|644|<epoch>", got)
	}
	// A missing path reaches the exit 42 branch, which is what maps to
	// fs.ErrNotExist in sandboxWorkspace.Stat.
	res = runScript(t, s, lstatScript, nil, "/workspace/nope", "%F")
	if res.ExitCode != 42 {
		t.Errorf("missing path exit = %d, want 42", res.ExitCode)
	}
	// A dangling link: -e fails, -L succeeds, so Lstat still reports it.
	if err := s.Symlink("/workspace/gone", "/workspace/dangling"); err != nil {
		t.Fatal(err)
	}
	res = runScript(t, s, lstatScript, nil, "/workspace/dangling", "%F|%s")
	if got := strings.TrimSpace(res.Stdout); got != "symbolic link|15" {
		t.Errorf("dangling lstat = %q, want symbolic link|15", got)
	}
	// But Stat (follow) cannot: -e fails and exits 42.
	res = runScript(t, s, statScript, nil, "/workspace/dangling", "%F")
	if res.ExitCode != 42 {
		t.Errorf("dangling stat exit = %d, want 42", res.ExitCode)
	}
}

func TestExecRunsTheReadDirScript(t *testing.T) {
	s := newCreated(t)
	ctx := context.Background()
	for _, p := range []string{"/workspace/.env", "/workspace/README.md", "/workspace/weird\nname.txt"} {
		if err := s.WriteFile(ctx, p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MkdirAll("/workspace/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.MkdirAll("/workspace/src", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Symlink("README.md", "/workspace/link"); err != nil {
		t.Fatal(err)
	}

	script := `find "$1" -mindepth 1 -maxdepth 1 -printf '%y\0%f\0'`
	res := runScript(t, s, script, nil, "/workspace")
	want := "f\x00.env\x00d\x00.git\x00f\x00README.md\x00l\x00link\x00d\x00src\x00f\x00weird\nname.txt\x00"
	if res.Stdout != want {
		t.Errorf("find output = %q\nwant              %q", res.Stdout, want)
	}
}

func TestExecRunsMutationsAndTests(t *testing.T) {
	s := newCreated(t)

	// mkdir -p && chmod, the MkdirAll shape.
	res := runScript(t, s, `mkdir -p -- "$1" && chmod "$2" "$1"`, nil, "/workspace/a/b", "700")
	if res.ExitCode != 0 {
		t.Fatalf("mkdir script exit = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	mode := runScript(t, s, `stat -c %a -- "$1"`, nil, "/workspace/a/b")
	if strings.TrimSpace(mode.Stdout) != "700" {
		t.Errorf("dir mode = %q, want 700", mode.Stdout)
	}

	// rm -f on a file.
	if err := s.WriteFile(context.Background(), "/workspace/trash", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = runScript(t, s, `rm -f -- "$1"`, nil, "/workspace/trash")
	if res.ExitCode != 0 {
		t.Fatalf("rm exit = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	if _, err := s.ReadFile(context.Background(), "/workspace/trash"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("rm left the file behind: %v", err)
	}

	// rm -f on a directory refuses without -r, the error path
	// sandboxWorkspace.Remove surfaces.
	res = runScript(t, s, `rm -f -- "$1"`, nil, "/workspace/a")
	if res.ExitCode == 0 || !strings.Contains(res.Stderr, "Is a directory") {
		t.Errorf("rm on a dir = exit %d, stderr %q; want non-zero with Is a directory", res.ExitCode, res.Stderr)
	}

	// rm -f on a missing path succeeds (that is what -f means).
	res = runScript(t, s, `rm -f -- "$1"`, nil, "/workspace/gone")
	if res.ExitCode != 0 {
		t.Errorf("rm -f on missing = exit %d, want 0", res.ExitCode)
	}
}

func TestExecHookOverridesAndRecords(t *testing.T) {
	s := newCreated(t)
	s.ExecHook = func(req goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
		return goatherdsandbox.ExecResult{ExitCode: 3, Stdout: "boom"}, nil
	}
	res := runScript(t, s, `stat -c %a -- "$1"`, nil, "/x")
	if res.ExitCode != 3 || res.Stdout != "boom" {
		t.Errorf("hook result = %+v, want exit 3 stdout boom", res)
	}
	if len(s.Execs()) != 1 || s.Execs()[0].Command != "sh" {
		t.Errorf("Execs = %+v, want the recorded sh request", s.Execs())
	}
}

func TestExecDirectCommandHasNoUserland(t *testing.T) {
	s := newCreated(t)
	res, err := s.Exec(context.Background(), goatherdsandbox.ExecRequest{Command: "go", Args: []string{"version"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 127 {
		t.Errorf("unknown command exit = %d, want 127", res.ExitCode)
	}
}

func TestCaptureApplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newCreated(t)
	if err := s.WriteFile(ctx, "/workspace/f.txt", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.MkdirAll("/workspace/d", 0o755); err != nil {
		t.Fatal(err)
	}

	state, err := s.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	// Mutate past the captured state.
	if err := s.WriteFile(ctx, "/workspace/f.txt", []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile(ctx, "/workspace/new.txt", []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Apply(ctx, state); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := s.ReadFile(ctx, "/workspace/f.txt")
	if err != nil || string(data) != "one" {
		t.Fatalf("after Apply content = %q, %v; want one", data, err)
	}
	if _, err := s.ReadFile(ctx, "/workspace/new.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Apply must reset, but new.txt survived: %v", err)
	}

	// The restored tree captures to an identical file digest: Apply is a
	// reset, not a merge, and modTime is not part of the state. (The states'
	// Revision fields differ by design, so compare the files maps.)
	after, err := s.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture after Apply: %v", err)
	}
	d1, _ := shepherdtrace.CanonicalDigest(state.Data["files"])
	d2, _ := shepherdtrace.CanonicalDigest(after.Data["files"])
	if d1 != d2 {
		t.Errorf("file digest drifted after apply: %q vs %q", d1, d2)
	}

	// Cross-backend state is refused.
	if err := s.Apply(ctx, shepherdtrace.WorkspaceState{Backend: "containerd"}); err == nil {
		t.Error("a state from another backend must be refused")
	}
}

func TestDiffReportsChanges(t *testing.T) {
	ctx := context.Background()
	s := newCreated(t)
	if err := s.WriteFile(ctx, "/workspace/f.txt", []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := s.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	if err := s.WriteFile(ctx, "/workspace/f.txt", []byte("alpha\ngamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile(ctx, "/workspace/added.txt", []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}

	diff, files, err := s.Diff(ctx, state, 0)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	want := []string{"/workspace/added.txt", "/workspace/f.txt"}
	if len(files) != len(want) || files[0] != want[0] || files[1] != want[1] {
		t.Errorf("changed files = %v, want %v", files, want)
	}
	if !strings.Contains(diff, "-beta") || !strings.Contains(diff, "+gamma") {
		t.Errorf("diff missing the line change:\n%s", diff)
	}
	if !strings.Contains(diff, "+++ added /workspace/added.txt") {
		t.Errorf("diff missing the added file:\n%s", diff)
	}

	// No change, no diff.
	after, _ := s.Capture(ctx)
	diff, files, err = s.Diff(ctx, after, 0)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff != "" || len(files) != 0 {
		t.Errorf("identical state diff = %q, %v; want empty", diff, files)
	}
}

// TestSymlinkChainResolvesFromEachLinkDirectory pins the chain rule: a
// relative target is anchored at the link's own directory at every hop, not
// at the path the lookup started from. link2 → other/link1 → ../sub/f.txt
// only resolves if hop two anchors at /workspace/other.
func TestSymlinkChainResolvesFromEachLinkDirectory(t *testing.T) {
	s := newCreated(t)
	ctx := context.Background()
	if err := s.WriteFile(ctx, "/workspace/sub/f.txt", []byte("chain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.MkdirAll("/workspace/other", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Symlink("../sub/f.txt", "/workspace/other/link1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Symlink("other/link1", "/workspace/link2"); err != nil {
		t.Fatal(err)
	}

	data, err := s.ReadFile(ctx, "/workspace/link2")
	if err != nil || string(data) != "chain" {
		t.Errorf("read through link chain = %q, %v; want chain", data, err)
	}
}

// TestEffectChangesMatchesTheTypedContract pins the fake's change set to the
// shape the container proposal seal consumes: workspace-relative paths, sorted,
// with created/patched/deleted kinds, post-image size and mode, and no
// permission bits on a delete.
func TestEffectChangesMatchesTheTypedContract(t *testing.T) {
	ctx := context.Background()
	s := newCreated(t)
	for p, body := range map[string]string{
		"/workspace/keep.txt": "before\n",
		"/workspace/gone.txt": "bye\n",
	} {
		if err := s.WriteFile(ctx, p, []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	base, err := s.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	if err := s.WriteFile(ctx, "/workspace/keep.txt", []byte("after!\n"), 0o600); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if err := s.WriteFile(ctx, "/workspace/new.txt", []byte("new\n"), 0o755); err != nil {
		t.Fatalf("add: %v", err)
	}
	if res := runScript(t, s, `rm -f -- "$1"`, nil, "/workspace/gone.txt"); res.ExitCode != 0 {
		t.Fatalf("remove: exit %d: %s", res.ExitCode, res.Stderr)
	}
	if err := s.MkdirAll("/workspace/sub", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	changes, err := s.EffectChanges(ctx, base)
	if err != nil {
		t.Fatalf("EffectChanges: %v", err)
	}
	got := make(map[string]effect.Change, len(changes))
	for _, c := range changes {
		got[c.Path] = c
	}
	if len(got) != 4 {
		t.Fatalf("changes = %+v, want keep.txt, new.txt, gone.txt, sub", changes)
	}
	if c := got["keep.txt"]; c.Kind != effect.KindPatched || c.Size != int64(len("after!\n")) || c.Mode.Perm() != 0o600 {
		t.Errorf("keep.txt = %+v", c)
	}
	if c := got["new.txt"]; c.Kind != effect.KindCreated || c.Size != int64(len("new\n")) || c.Mode.Perm() != 0o755 {
		t.Errorf("new.txt = %+v", c)
	}
	if c := got["gone.txt"]; c.Kind != effect.KindDeleted || c.Size != 0 || c.Mode.Perm() != 0 {
		t.Errorf("gone.txt = %+v, want a delete with no size or permission bits", c)
	}
	if c := got["sub"]; c.Kind != effect.KindCreated || !c.IsDir {
		t.Errorf("sub = %+v, want a created directory", c)
	}
	for i := 1; i < len(changes); i++ {
		if changes[i-1].Path > changes[i].Path {
			t.Fatalf("changes are not sorted: %+v", changes)
		}
	}
}
