package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	err := reg.SetWorkspace(newSandboxWS(t, nil))
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

	if err := reg.SetWorkspace(newSandboxWS(t, nil)); err != nil {
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

	for _, name := range []string{"read", "write", "edit", "delete", "patch", "bash", "ls", "grep", "glob", "replace", "git", "go_mod", "go_test", "staticcheck", "bisect"} {
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

func TestRegistry_SetPathValidatorBackfills(t *testing.T) {
	reg := NewRegistry()
	ws := t.TempDir()
	reg.SetPathValidator(NewPathValidator(ws, false, nil))

	rt, ok := reg.Get("read").(*ReadTool)
	if !ok {
		t.Fatal("read tool not registered as *ReadTool")
	}
	if rt.PV == nil || rt.PV.WorkspaceRoot == "" {
		t.Error("SetPathValidator did not backfill the read tool")
	}

	reg.Register(&WriteTool{})
	wt, ok := reg.Get("write").(*WriteTool)
	if !ok {
		t.Fatal("write tool not registered as *WriteTool")
	}
	if wt.PV == nil {
		t.Error("Register after SetPathValidator did not auto-inject")
	}
}

// unmigratedFSTool is a filesystem-touching tool that has not adopted
// WorkspaceSetter, used to check that the isolation gate classifies the tool
// inside a RecordingTool wrapper rather than the wrapper itself.
type unmigratedFSTool struct{}

func (unmigratedFSTool) Name() string                                    { return "legacy_fs" }
func (unmigratedFSTool) Description() string                             { return "" }
func (unmigratedFSTool) Schema() json.RawMessage                         { return nil }
func (unmigratedFSTool) Execute(context.Context, string) (string, error) { return "", nil }
func (unmigratedFSTool) filesystemTool()                                 {}

// TestRegistry_IsolationGateUnwrapsRecordingTools pins that tools registered
// through the conflict-tracking wrapper reach the gate: the wrapped tool's
// markers decide migration, and SetWorkspace is forwarded so the tool inside
// actually receives the workspace.
func TestRegistry_IsolationGateUnwrapsRecordingTools(t *testing.T) {
	legacy := NewEmptyRegistry()
	legacy.Register(NewRecordingTool(unmigratedFSTool{}, nil))
	if got := legacy.UnmigratedFilesystemTools(); len(got) != 1 || got[0] != "legacy_fs" {
		t.Errorf("unmigrated = %v, want [legacy_fs]", got)
	}

	migrated := NewEmptyRegistry()
	migrated.Register(NewRecordingTool(&WriteTool{}, nil))
	if got := migrated.UnmigratedFilesystemTools(); len(got) != 0 {
		t.Errorf("unmigrated = %v, want none for a migrated wrapped tool", got)
	}

	rec := NewRecordingTool(&WriteTool{}, nil)
	rec.SetWorkspace(newLocalWorkspace(nil))
	if wt, ok := rec.inner.(*WriteTool); !ok || wt.WS == nil {
		t.Error("RecordingTool.SetWorkspace must reach the wrapped tool")
	}
}

func TestReadTool_WorkspaceEnforced(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(ws, "ok.txt")
	if err := os.WriteFile(inside, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := &ReadTool{PV: NewPathValidator(ws, false, nil)}

	if _, err := rt.Execute(context.Background(), `{"filePath":"`+filepath.ToSlash(secret)+`"}`); err == nil {
		t.Error("read outside workspace must fail")
	}
	res, err := rt.Execute(context.Background(), `{"filePath":"`+filepath.ToSlash(inside)+`"}`)
	if err != nil {
		t.Fatalf("read inside workspace failed: %v", err)
	}
	if !strings.Contains(res, "hello") {
		t.Errorf("unexpected content: %q", res)
	}
}

func TestWriteTool_WorkspaceEnforced(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()

	wt := &WriteTool{PV: NewPathValidator(ws, false, nil)}

	target := filepath.Join(outside, "evil.txt")
	if _, err := wt.Execute(context.Background(), `{"filePath":"`+filepath.ToSlash(target)+`","content":"x"}`); err == nil {
		t.Fatal("write outside workspace must fail")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("file outside workspace was created despite rejection")
	}
}
