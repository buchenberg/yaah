package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"
	"testing"
	"time"
)

// memFS is an in-memory Workspace used to prove go_refactor touches only the
// injected workspace. Its root does not exist on the host, so an os.* fallback
// would fail the test instead of passing silently — which is the whole point of
// the migration (bead yaah-0fy).
type memFS struct {
	root     string
	files    map[string]string
	dirs     map[string]bool
	local    bool
	writes   int
	writeErr error
}

func newMemFS(root string) *memFS {
	return &memFS{root: root, files: map[string]string{}, dirs: map[string]bool{root: true}}
}

// put stores a file at a workspace-relative path and records its directories.
func (m *memFS) put(rel, content string) {
	p := path.Join(m.root, rel)
	m.files[p] = content
	for d := path.Dir(p); ; d = path.Dir(d) {
		m.dirs[d] = true
		if d == m.root || len(d) < len(m.root) {
			break
		}
	}
}

func (m *memFS) content(rel string) string { return m.files[path.Join(m.root, rel)] }

func (m *memFS) ResolvePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is required")
	}
	if path.IsAbs(p) {
		return path.Clean(p), nil
	}
	return path.Clean(m.root + "/" + p), nil
}

func (m *memFS) WorkDir() string            { return m.root }
func (m *memFS) Join(elem ...string) string { return path.Join(elem...) }
func (m *memFS) Local() bool                { return m.local }
func (m *memFS) Shell() (string, string)    { return "sh", "-c" }

func (m *memFS) ReadFile(_ context.Context, p string) ([]byte, error) {
	c, ok := m.files[p]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return []byte(c), nil
}

func (m *memFS) WriteFile(_ context.Context, p string, data []byte, _ fs.FileMode) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.files[p] = string(data)
	m.dirs[path.Dir(p)] = true
	m.writes++
	return nil
}

func (m *memFS) Stat(_ context.Context, p string) (fs.FileInfo, error) { return m.stat(p) }

func (m *memFS) Lstat(_ context.Context, p string) (fs.FileInfo, error) { return m.stat(p) }

func (m *memFS) stat(p string) (fs.FileInfo, error) {
	if c, ok := m.files[p]; ok {
		return memInfo{name: path.Base(p), size: int64(len(c))}, nil
	}
	if m.dirs[p] {
		return memInfo{name: path.Base(p), dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
}

// ReadDir deliberately returns entries in map order: the tool must sort them
// itself, because the Workspace interface promises no ordering.
func (m *memFS) ReadDir(_ context.Context, p string) ([]fs.DirEntry, error) {
	if !m.dirs[p] {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
	}
	seen := make(map[string]bool)
	var out []fs.DirEntry
	for f, c := range m.files {
		if path.Dir(f) != p {
			continue
		}
		name := path.Base(f)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, fs.FileInfoToDirEntry(memInfo{name: name, size: int64(len(c))}))
	}
	for d := range m.dirs {
		if d == p || path.Dir(d) != p {
			continue
		}
		name := path.Base(d)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, fs.FileInfoToDirEntry(memInfo{name: name, dir: true}))
	}
	return out, nil
}

func (m *memFS) Remove(_ context.Context, p string) error {
	delete(m.files, p)
	delete(m.dirs, p)
	return nil
}

func (m *memFS) MkdirAll(_ context.Context, p string, _ fs.FileMode) error {
	m.dirs[p] = true
	return nil
}

func (m *memFS) Exec(context.Context, ExecRequest) (ExecResult, error) {
	return ExecResult{ExitCode: -1}, errors.New("memFS: commands are not available")
}

type memInfo struct {
	name string
	size int64
	dir  bool
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() any           { return nil }

// unformattedGoSource is valid Go with mis-indented code and unsorted imports.
const unformattedGoSource = `package shapes

import (
"math"
"fmt"
)

type Circle struct{ Radius float64 }

func NewCircle(r float64) *Circle { return &Circle{Radius:r} }

func (c *Circle) Area() float64 { return math.Pi * c.Radius * c.Radius }

var _ = fmt.Sprint
`

func TestGoRefactorTool_Format_WritesThroughWorkspace(t *testing.T) {
	ws := newMemFS("/memws")
	ws.put("shapes.go", unformattedGoSource)

	tool := &GoRefactorTool{WS: ws}
	out, err := tool.Execute(context.Background(), `{"action":"format","file":"shapes.go"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if ws.writes != 1 {
		t.Fatalf("workspace writes = %d, want exactly 1 (the formatted file)", ws.writes)
	}
	if !strings.Contains(out, "shapes.go: formatted") {
		t.Errorf("result does not report the format: %q", out)
	}

	got := ws.content("shapes.go")
	if !strings.Contains(got, "\t\"fmt\"\n\t\"math\"") {
		t.Errorf("imports were not sorted/formatted:\n%s", got)
	}
	if !strings.Contains(got, "return &Circle{Radius: r}") {
		t.Errorf("body was not gofmt'd:\n%s", got)
	}
	if strings.Contains(got, `"math"
"fmt"`) {
		t.Errorf("import order was left unsorted:\n%s", got)
	}
}

func TestGoRefactorTool_Format_AlreadyFormatted(t *testing.T) {
	ws := newMemFS("/memws")
	ws.put("shapes.go", unformattedGoSource)

	tool := &GoRefactorTool{WS: ws}
	if _, err := tool.Execute(context.Background(), `{"action":"format","file":"shapes.go"}`); err != nil {
		t.Fatalf("first format: %v", err)
	}
	writes := ws.writes

	out, err := tool.Execute(context.Background(), `{"action":"format","file":"shapes.go"}`)
	if err != nil {
		t.Fatalf("second format: %v", err)
	}
	if !strings.Contains(out, "already formatted") {
		t.Errorf("second format = %q, want an 'already formatted' result", out)
	}
	if ws.writes != writes {
		t.Errorf("a no-op format wrote the file again (writes %d → %d)", writes, ws.writes)
	}
}

func TestGoRefactorTool_Format_MissingFileIsAnError(t *testing.T) {
	ws := newMemFS("/memws")
	tool := &GoRefactorTool{WS: ws}

	_, err := tool.Execute(context.Background(), `{"action":"format","file":"nope.go"}`)
	if err == nil {
		t.Fatal("expected an error for a file the workspace does not have")
	}
	if !strings.Contains(err.Error(), "go_refactor format") {
		t.Errorf("error %q does not name the operation", err)
	}
	if ws.writes != 0 {
		t.Errorf("writes = %d, want 0", ws.writes)
	}
}

func TestGoRefactorTool_Format_WriteFailureIsAnError(t *testing.T) {
	ws := newMemFS("/memws")
	ws.put("shapes.go", unformattedGoSource)
	ws.writeErr = errors.New("injected write failure")

	tool := &GoRefactorTool{WS: ws}
	_, err := tool.Execute(context.Background(), `{"action":"format","file":"shapes.go"}`)
	if err == nil {
		t.Fatal("a refused write must be an error, not a success line")
	}
	if !strings.Contains(err.Error(), "injected write failure") {
		t.Errorf("error %q does not carry the cause", err)
	}
}

func TestGoRefactorTool_Format_BadSyntaxIsAnError(t *testing.T) {
	ws := newMemFS("/memws")
	ws.put("bad.go", "package x\nfunc ()\n")

	tool := &GoRefactorTool{WS: ws}
	if _, err := tool.Execute(context.Background(), `{"action":"format","file":"bad.go"}`); err == nil {
		t.Fatal("expected a parse error")
	}
	if ws.writes != 0 {
		t.Errorf("writes = %d, want 0 — an unparseable file must not be replaced", ws.writes)
	}
}

func TestGoRefactorTool_Info_ListsSymbolsThroughWorkspace(t *testing.T) {
	ws := newMemFS("/memws")
	ws.put("pkg/a.go", `package shapes

type Circle struct{ Radius float64 }

func NewCircle() *Circle { return &Circle{} }

func (c *Circle) Area() float64 { return c.Radius }
`)
	ws.put("pkg/b.go", "package shapes\n\nfunc Perimeter() int { return 0 }\n")
	ws.put("pkg/a_test.go", "package shapes\n\nfunc TestArea() {}\n")

	tool := &GoRefactorTool{WS: ws}
	out, err := tool.Execute(context.Background(), `{"action":"info","dir":"pkg"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	for _, want := range []string{
		"Package: /memws/pkg (1 package loaded)",
		"Name: shapes | Go files: 2 | Imports: 0",
		"## Functions",
		"NewCircle (line 5)",
		"Area (method) (line 7)",
		"Perimeter (line 3)",
		// The pre-migration tool printed obj.Type().String() — the *named*
		// type (pkg.Circle), not its underlying struct — so the symbol table
		// renders Circle as "<pkg path>.Circle".
		"Circle: /memws/pkg.Circle",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TestArea") {
		t.Errorf("test files must not be analyzed:\n%s", out)
	}
	t.Logf("info:\n%s", out)
}

func TestGoRefactorTool_Info_DefaultsToWorkDir(t *testing.T) {
	ws := newMemFS("/memws")
	ws.put("root.go", "package shapes\n\nfunc Only() {}\n")

	tool := &GoRefactorTool{WS: ws}
	out, err := tool.Execute(context.Background(), `{"action":"info"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(out, "Only (line 3)") {
		t.Errorf("info did not analyze the workspace root:\n%s", out)
	}
}

func TestGoRefactorTool_Info_MissingDirIsAnError(t *testing.T) {
	ws := newMemFS("/memws")
	tool := &GoRefactorTool{WS: ws}

	_, err := tool.Execute(context.Background(), `{"action":"info","dir":"absent"}`)
	if err == nil {
		t.Fatal("expected an error for a directory the workspace does not have")
	}
	if !strings.Contains(err.Error(), "go_refactor info") {
		t.Errorf("error %q does not name the operation", err)
	}
}

// TestGoRefactorTool_Info_NoGoFilesIsAnError pins that a directory with nothing
// to analyze reports a failure rather than an empty success. No file is parsed
// and no package is loaded, so "no symbols found" would describe work that
// never happened — the model-facing signal has to say the input had no Go files
// in it.
func TestGoRefactorTool_Info_NoGoFilesIsAnError(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"only non-Go files", map[string]string{"notes.txt": "hello\n", "README.md": "# hi\n"}},
		{"only test files", map[string]string{"a_test.go": "package shapes\n\nfunc TestA() {}\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := newMemFS("/memws")
			for name, content := range tc.files {
				ws.put(name, content)
			}

			tool := &GoRefactorTool{WS: ws}
			out, err := tool.Execute(context.Background(), `{"action":"info"}`)
			if err == nil {
				t.Fatalf("info over %s = %q, want an error naming the empty directory", tc.name, out)
			}
			if !strings.Contains(err.Error(), "no Go files found in /memws") {
				t.Errorf("error = %q, want it to name the directory that had no Go files", err)
			}
			if out != "" {
				t.Errorf("output = %q, want no analysis text beside the error", out)
			}
		})
	}
}

// TestGoRefactorTool_Info_SkipsForeignPackageFiles pins the other side of that
// boundary: a directory holding one real file plus a stray file with another
// package clause still gets a real analysis, with the skipped file named rather
// than dropped. It is what keeps the type-check reachable with a non-empty file
// list, which is why the loading path needs no "package is nil" fallback.
func TestGoRefactorTool_Info_SkipsForeignPackageFiles(t *testing.T) {
	ws := newMemFS("/memws")
	ws.put("pkg/a.go", "package shapes\n\nfunc Area() int { return 0 }\n")
	ws.put("pkg/scratch.go", "package scratch\n\nvar X = 1\n")

	tool := &GoRefactorTool{WS: ws}
	out, err := tool.Execute(context.Background(), `{"action":"info","dir":"pkg"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	for _, want := range []string{
		"Name: shapes | Go files: 1",
		"Area (line 3)",
		"skipped 1 file(s) with another package clause: scratch.go (package scratch)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
}

func TestGoRefactorTool_BadArguments(t *testing.T) {
	tool := &GoRefactorTool{WS: newMemFS("/memws")}

	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Error("expected an error for invalid JSON")
	}
	if _, err := tool.Execute(context.Background(), `{"action":"compile"}`); err == nil {
		t.Error("expected an error for an unknown action")
	}
	if _, err := tool.Execute(context.Background(), `{"action":"format"}`); err == nil {
		t.Error("expected an error for format without a file")
	}
}

// TestGoRefactorTool_Contract pins the parts of the tool contract consumers
// depend on: the name, a model-facing description, and a schema whose action
// enum is unchanged by the workspace migration.
func TestGoRefactorTool_Contract(t *testing.T) {
	tool := &GoRefactorTool{}
	if tool.Name() != "go_refactor" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "go_refactor")
	}
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
	if !tool.IsDangerous("{}") {
		t.Error("go_refactor must stay dangerous: format rewrites files")
	}

	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("Schema() is not valid JSON: %v", err)
	}
	got := strings.Join(schema.Properties["action"].Enum, ",")
	if got != "format,info" {
		t.Errorf("action enum = %q, want %q", got, "format,info")
	}
	for _, key := range []string{"action", "file", "dir"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("schema lost property %q", key)
		}
	}
}
