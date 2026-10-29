package tools

// enforcement_test.go is a source-scan gate: it enforces "enforcement by
// construction" (bead yaah-cede) by parsing every non-test file in this package
// and failing when one of them touches the filesystem through os.* instead of
// the injected Workspace.
//
// Why a source scan: a rule that lives only in review ("tools must use the
// Workspace") decays; the next tool added reaches for os.ReadFile because that
// is what the tutorial said. The compile-time alternative — an interface that
// makes os.* unreachable — is not available in Go for a package's own imports.
// Scanning the AST is therefore the cheapest honest enforcement: it cannot be
// satisfied by a comment, and a violation fails CI on the commit that adds it.
//
// Scope: only os.* *filesystem* identifiers. os.Getenv, os.Exit, os.Args,
// os.Environ, os.LookupEnv and friends are not filesystem operations and are
// used freely. Test files are exempt — a test must be able to set up and verify
// real files, and a fake workspace is proven by exercising the real one.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// filesystemOps are the os package selectors that reach the filesystem and must
// therefore go through the Workspace interface. Every entry here has a
// Workspace counterpart (ReadFile, WriteFile, Stat, Lstat, ReadDir, Remove,
// MkdirAll, ResolvePath) or is deliberately absent from it (Chdir, Getwd,
// Rename, Create, CopyFS, Mkdir — a tool has no business changing the process
// working directory or creating files non-atomically).
var filesystemOps = map[string]bool{
	"Open":      true,
	"OpenFile":  true,
	"ReadFile":  true,
	"WriteFile": true,
	"ReadDir":   true,
	"Remove":    true,
	"RemoveAll": true,
	"Mkdir":     true,
	"MkdirAll":  true,
	"Stat":      true,
	"Lstat":     true,
	"Rename":    true,
	"Create":    true,
	"CopyFS":    true,
	"Getwd":     true,
	"Chdir":     true,
}

// fsExempt lists the files allowed to call os.* filesystem functions in
// addition to workspace.go, with the reason each call is harness code rather
// than tool file access. Keys are "file.go" or "file.go:Ident" for a
// single-call exemption; an exemption that names a call only covers that call,
// so a new os.ReadFile in the same file still fails.
//
// The bar for adding an entry: the call must operate on something that is NOT a
// project path a tool was asked to touch — the implementation of the Workspace
// itself, an executable's own location, or a scratch directory the harness owns
// before any workspace exists. "It was easier than injecting the workspace" is
// not a reason; migrate the call instead.
var fsExempt = map[string]string{
	"workspace.go": "the local Workspace implementation — the single place in this package that is allowed to turn a resolved path into a host syscall",

	"atomic_write.go": "the temp-file+rename primitive that localWorkspace.WriteFile is built on: it is the host syscall layer one level *below* the Workspace interface, so it cannot route through it",

	"workspace_bootstrap.go": "bootstrapping a workspace: it copies the host tree into a substrate and rolls that back before any workspace exists to route through",

	"confinement.go": "the confinement harness writes its shim binary and redirects HOME/TMPDIR into a harness-owned scratch directory — none of it is a project path",

	"path_validator.go": "the containment validator itself: os.Lstat is how it detects a symlink escape *before* a path is handed to any tool",

	"retained.go": "the retained lane's harness paths, not project paths: worktree teardown probe (Stat), the release export written beside the worktree (MkdirAll/WriteFile), the repo's .git/info/exclude (ReadFile/OpenFile), and the default repo path (Getwd)",

	"git_delta.go:Chmod":     "container settlement applies the manifest's post-image mode to the host tree — the manifest is the authority, not the workspace",
	"git_delta.go:Lstat":     "settlement checks a destination's ancestors for a symlink escape before writing; this is the host-writer half of settlement, not tool file access",
	"git_delta.go:MkdirAll":  "settlement creates parents for whole-path post-images inside the validated repository root",
	"git_delta.go:Open":      "the durable artifact fsync opens the manifest file it just wrote under .yaah/ bookkeeping",
	"git_delta.go:OpenFile":  "the durable artifact fsync opens the manifest read-write for FlushFileBuffers on Windows",
	"git_delta.go:ReadFile":  "release copies the manifest it owns out of .yaah/proposals — not a project path",
	"git_delta.go:Remove":    "settlement applies a manifest delete to the host tree",
	"git_delta.go:RemoveAll": "consume-once removes the proposal's own .yaah/proposals directory",
	"git_delta.go:Stat":      "fsync skips artifacts that were not written and checks a path before opening",

	"proposal_rehydrate.go:ReadDir":   "rehydration scans yaah's own .yaah/proposals bookkeeping before any dispatch workspace exists; it reads no project path",
	"proposal_rehydrate.go:Stat":      "rehydration checks its own settlement markers under .yaah/proposals",
	"proposal_rehydrate.go:RemoveAll": "rehydration consumes a settled artifact directory under .yaah/proposals after a crash between the marker and removal",
	"proposal_rehydrate.go:ReadFile":  "rehydration reads the manifest, basis, and metadata it wrote under .yaah/proposals",

	"role.go": "the role tool edits yaah's own role definitions and then reloads the process-wide role registry; role files are harness metadata, and the reload has no workspace view",

	"availability.go:Stat": "GoToolAvailable walks up from a registry-curation root looking for go.mod to decide which Go tools to ship; it runs before any dispatch, when no workspace is injected yet",
}

type fsViolation struct {
	file string
	op   string
	line int
}

func (v fsViolation) String() string {
	return v.file + ":" + v.op
}

// TestNoHostFilesystemAccessOutsideWorkspace scans this package's non-test
// sources and fails when os.* filesystem access escapes the Workspace.
func TestNoHostFilesystemAccessOutsideWorkspace(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	var violations []fsViolation
	scanned := 0
	for _, entry := range files {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, ok := fsExempt[name]; ok {
			continue
		}
		scanned++
		violations = append(violations, scanFileForFilesystemOps(t, name)...)
	}

	if scanned == 0 {
		t.Fatal("scanned no files — the gate is not looking at the package sources")
	}
	if len(violations) == 0 {
		return
	}

	// Deduplicate (the same call can be hit twice by the walk) and sort so the
	// failure text is stable across runs.
	seen := make(map[string]bool, len(violations))
	unique := make([]fsViolation, 0, len(violations))
	for _, v := range violations {
		if seen[v.String()] {
			continue
		}
		seen[v.String()] = true
		unique = append(unique, v)
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].file != unique[j].file {
			return unique[i].file < unique[j].file
		}
		return unique[i].line < unique[j].line
	})

	var b strings.Builder
	b.WriteString("filesystem access outside the Workspace (every tool file operation must go through tools.Workspace):\n")
	for _, v := range unique {
		b.WriteString("  " + v.String() + "\n")
	}
	b.WriteString("\nFix: take the path from ws.ResolvePath and use ws.ReadFile / ws.WriteFile / ws.Stat / ws.ReadDir / ws.Remove / ws.MkdirAll / ws.Exec.\n")
	b.WriteString("If the call is harness code that operates on no project path (an executable's own location, a scratch dir owned by the harness), add a narrow entry to fsExempt in enforcement_test.go with a comment saying why.\n")
	t.Fatal(b.String())
}

// scanFileForFilesystemOps returns the os.* filesystem calls in one source file.
func scanFileForFilesystemOps(t *testing.T, path string) []fsViolation {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// An exemption that names a call ("file.go:Ident") covers only that call:
	// a file-level entry never reaches this function (the caller skips it), so
	// everything found here is a per-call exemption being matched.
	permitted := map[string]bool{}
	for key := range fsExempt {
		if ident, ok := strings.CutPrefix(key, path+":"); ok {
			permitted[ident] = true
		}
	}

	var out []fsViolation
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "os" || !filesystemOps[sel.Sel.Name] {
			return true
		}
		if permitted[sel.Sel.Name] {
			return true
		}
		out = append(out, fsViolation{file: path, op: sel.Sel.Name, line: fset.Position(sel.Pos()).Line})
		return true
	})
	return out
}
