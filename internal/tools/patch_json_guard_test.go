package tools

// patch_json_guard_test.go pins the guard conversion of patch and json_query
// (bug yaah-qlli): two mutating file tools dispatched in the same batch
// against the same file must not silently discard the loser's write. The
// per-path lock serializes the read→mutate→write sequence inside one
// process, and the compare-and-swap refuses a write whose base went stale
// under it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// racingReadWorkspace wraps the real local workspace: every ReadFile returns
// the true bytes and then an out-of-band writer (another process, an editor)
// immediately replaces the file, so the tool is racing a world that changes
// between its read and its write.
type racingReadWorkspace struct {
	Workspace
	path string
	onto []byte
}

func (w *racingReadWorkspace) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, err := w.Workspace.ReadFile(ctx, path)
	if err == nil && resolvedPath(path) == resolvedPath(w.path) {
		if werr := os.WriteFile(path, w.onto, 0o644); werr != nil {
			return nil, werr
		}
	}
	return data, err
}

// TestJSONQuery_GuardedWritesDoNotLoseUpdates is the lost-update regression:
// eight writers behind one start barrier each set an independent key of the
// same JSON document. Before the guard the read→set→write races were
// last-writer-wins; after it every one of the eight writes must land.
func TestJSONQuery_GuardedWritesDoNotLoseUpdates(t *testing.T) {
	const n = 8
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "store.json")
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	seed, err := json.Marshal(map[string]any{"store": map[string]any{"keys": keys}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			jq := &JSONQueryTool{WS: ws}
			args, _ := json.Marshal(map[string]any{
				"file": path,
				"path": fmt.Sprintf("store.keys[%d]", i),
				"set":  fmt.Sprintf(`"written-%d"`, i),
			})
			_, errs[i] = jq.Execute(context.Background(), string(args))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Store struct {
			Keys []string `json:"keys"`
		} `json:"store"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("re-read store.json (%s): %v", data, err)
	}
	if len(doc.Store.Keys) != n {
		t.Fatalf("keys = %v, want %d entries", doc.Store.Keys, n)
	}
	for i, got := range doc.Store.Keys {
		if want := fmt.Sprintf("written-%d", i); got != want {
			t.Errorf("store.keys[%d] = %q, want %q (a concurrent write was lost)", i, got, want)
		}
	}
}

// TestPatch_GuardedHunksDoNotLoseUpdates is the same regression through patch:
// eight writers each insert their own marker line after the shared anchor
// line. The hunks are independent — each matches the anchor and inserts one
// marker — so all eight must be present when the dust settles.
func TestPatch_GuardedHunksDoNotLoseUpdates(t *testing.T) {
	const n = 8
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(path, []byte("anchor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			pt := &PatchTool{WS: ws}
			args, _ := json.Marshal(map[string]string{
				"filePath": path,
				"patch":    fmt.Sprintf("@@ -1 +1,2 @@\n anchor\n+G%d\n", i),
			})
			_, errs[i] = pt.Execute(context.Background(), string(args))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "anchor\n") {
		t.Errorf("anchor line lost; file = %q", data)
	}
	for i := 0; i < n; i++ {
		if marker := fmt.Sprintf("G%d\n", i); !strings.Contains(string(data), marker) {
			t.Errorf("marker %q missing; a concurrent hunk was lost. file = %q", marker, data)
		}
	}
	if got := strings.Count(string(data), "\n"); got != n+1 {
		t.Errorf("line count = %d, want %d (anchor plus one per writer); file = %q", got, n+1, data)
	}
}

// TestJSONQuery_StaleWriteRefused pins the compare-and-swap at the tool
// boundary: when the file changes between the tool's read and its write, the
// tool reports *StaleWriteError and the out-of-band bytes survive untouched.
func TestJSONQuery_StaleWriteRefused(t *testing.T) {
	real, dir := realWorkspace(t)
	path := filepath.Join(dir, "stale.json")
	if err := os.WriteFile(path, []byte("{\n  \"name\": \"original\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outOfBand := []byte("{\n  \"name\": \"out-of-band\"\n}\n")
	ws := &racingReadWorkspace{Workspace: real, path: path, onto: outOfBand}

	jq := &JSONQueryTool{WS: ws}
	args, _ := json.Marshal(map[string]any{"file": path, "path": "name", "set": `"tool"`})
	_, err := jq.Execute(context.Background(), string(args))

	var stale *StaleWriteError
	if !errors.As(err, &stale) {
		t.Fatalf("json_query error = %v, want *StaleWriteError", err)
	}
	assertFileContent(t, path, string(outOfBand))
}

// TestPatch_StaleWriteRefused pins the same refusal through patch: the hunks
// verify against bytes that are already gone, so nothing is written.
func TestPatch_StaleWriteRefused(t *testing.T) {
	real, dir := realWorkspace(t)
	path := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &racingReadWorkspace{Workspace: real, path: path, onto: []byte("out-of-band\n")}

	pt := &PatchTool{WS: ws}
	args, _ := json.Marshal(map[string]string{
		"filePath": path,
		"patch":    "@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n",
	})
	_, err := pt.Execute(context.Background(), string(args))

	var stale *StaleWriteError
	if !errors.As(err, &stale) {
		t.Fatalf("patch error = %v, want *StaleWriteError", err)
	}
	assertFileContent(t, path, "out-of-band\n")
}

// TestPatch_FailingHunkThroughGuard keeps the tool's own refusal: a hunk
// whose preimage does not match errors with the usual report and leaves the
// target byte-identical, with the guard in the loop.
func TestPatch_FailingHunkThroughGuard(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "f.txt")
	original := "alpha\nbeta\ngamma\n"
	writeFileAt(t, path, original)

	pt := &PatchTool{WS: ws}
	args, _ := json.Marshal(map[string]string{
		"filePath": path,
		"patch":    "@@ -1,3 +1,3 @@\n alpha\n-notbeta\n+NOTBETA\n gamma\n",
	})
	_, err := pt.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("a failing hunk must be an error")
	}
	if !strings.Contains(err.Error(), "left unchanged") {
		t.Errorf("error lost its hunk-failure wording: %v", err)
	}
	assertFileContent(t, path, original)
}

// TestJSONQuery_InvalidJSONThroughGuard keeps the decode refusal on both
// mutating paths: an undecodable document errors with the usual message and
// the file is byte-identical, with the guard in the loop.
func TestJSONQuery_InvalidJSONThroughGuard(t *testing.T) {
	for _, action := range []string{"write", "delete"} {
		t.Run(action, func(t *testing.T) {
			ws, dir := realWorkspace(t)
			path := filepath.Join(dir, "bad.json")
			original := "not json"
			writeFileAt(t, path, original)

			jq := &JSONQueryTool{WS: ws}
			args, _ := json.Marshal(map[string]any{
				"file": path, "path": "name", "action": action, "set": `"v"`,
			})
			_, err := jq.Execute(context.Background(), string(args))
			if err == nil {
				t.Fatal("invalid JSON must be an error")
			}
			if !strings.Contains(err.Error(), "invalid JSON") {
				t.Errorf("error lost its decode wording: %v", err)
			}
			assertFileContent(t, path, original)
		})
	}
}

// TestJSONQuery_SuccessEndsWithReceipt pins the receipt the guard returns at
// the json_query boundary: a successful write and a successful delete each
// end with one final line — wrote <file> (before -> after bytes,
// sha256:<12 hex>) — whose numbers and digest match the file on disk, after
// the tool's existing report lines.
func TestJSONQuery_SuccessEndsWithReceipt(t *testing.T) {
	sha12 := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])[:12]
	}

	t.Run("write", func(t *testing.T) {
		ws, dir := realWorkspace(t)
		path := filepath.Join(dir, "cfg.json")
		seed := []byte(`{"react":"17.0.0"}`)
		writeFileAt(t, path, string(seed))

		jq := &JSONQueryTool{WS: ws}
		args, _ := json.Marshal(map[string]any{"file": path, "path": "react", "set": `"19.0.0"`})
		result, err := jq.Execute(context.Background(), string(args))
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// The receipt names the file workspace-relative, as the stale refusal
		// does, and its numbers and digest must match the disk.
		want := fmt.Sprintf("wrote %s (%d -> %d bytes, sha256:%s)", filepath.Base(path), len(seed), len(after), sha12(after))
		if !strings.HasSuffix(result, want) {
			t.Errorf("result must end with the receipt %q, got %q", want, result)
		}
		if !strings.Contains(result, "Set react") {
			t.Errorf("the Set line must keep its place before the receipt: %q", result)
		}
	})

	t.Run("delete", func(t *testing.T) {
		ws, dir := realWorkspace(t)
		path := filepath.Join(dir, "cfg.json")
		seed := []byte(`{"keep":"yes","remove":"no"}`)
		writeFileAt(t, path, string(seed))

		jq := &JSONQueryTool{WS: ws}
		args, _ := json.Marshal(map[string]any{"file": path, "path": "remove", "action": "delete"})
		result, err := jq.Execute(context.Background(), string(args))
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("wrote %s (%d -> %d bytes, sha256:%s)", filepath.Base(path), len(seed), len(after), sha12(after))
		if !strings.HasSuffix(result, want) {
			t.Errorf("result must end with the receipt %q, got %q", want, result)
		}
		if !strings.Contains(result, "Deleted remove") {
			t.Errorf("the Deleted line must keep its place before the receipt: %q", result)
		}
	})
}
