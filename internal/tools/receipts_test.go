package tools

// receipts_test.go pins the write receipt that closes a successful mutating
// call (bug yaah-qlli): path, byte delta, and the digest of what is now on
// disk, as the final line of the result. The receipt is the layer that makes
// a lost update legible — "375 -> 383" immediately after "375 -> 396" is
// visibly impossible, where a bare "edited" is not. json_query, replace and
// append carry their own receipt tests beside them; edit, write and patch are
// covered here.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// receiptDigest renders the 12-hex digest prefix a receipt line carries.
func receiptDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// receiptLastLine returns the final newline-separated line of a tool result
// with any trailing newlines stripped, so a test asserts the receipt sits
// exactly where it should rather than merely somewhere in the text.
func receiptLastLine(result string) string {
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestWriteReceipt_LastLineNamesDeltaAndDigest pins write's receipt for both
// shapes: "wrote" for a file the call overwrote, "created" for one it brought
// into existence, with a byte delta and digest that match the file on disk.
func TestWriteReceipt_LastLineNamesDeltaAndDigest(t *testing.T) {
	ws, dir := realWorkspace(t)
	wt := &WriteTool{WS: ws}

	t.Run("overwrite", func(t *testing.T) {
		path := filepath.Join(dir, "over.txt")
		writeFileAt(t, path, "old content\n")
		out, err := wt.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"content":"new\n"}`, path))
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("wrote %s (12 -> %d bytes, sha256:%s)", filepath.Base(path), len(after), receiptDigest(after))
		if got := receiptLastLine(out); got != want {
			t.Errorf("receipt line = %q, want %q (full result %q)", got, want, out)
		}
	})

	t.Run("create", func(t *testing.T) {
		path := filepath.Join(dir, "fresh.txt")
		out, err := wt.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"content":"hello\n"}`, path))
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("created %s (0 -> %d bytes, sha256:%s)", filepath.Base(path), len(after), receiptDigest(after))
		if got := receiptLastLine(out); got != want {
			t.Errorf("receipt line = %q, want %q (full result %q)", got, want, out)
		}
	})
}

// TestPatchReceipt_AgreesWithLineCounts pins patch's receipt and the fact that
// it agrees with the line counts on the line above it: a patch that reports
// line counts and a byte delta derived from two different writes would be a
// contradiction the receipt exists to expose.
func TestPatchReceipt_AgreesWithLineCounts(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "p.txt")
	writeFileAt(t, path, "alpha\nbeta\n")

	pt := &PatchTool{WS: ws}
	args, err := json.Marshal(map[string]string{
		"filePath": path,
		"patch":    "@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA!\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := pt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "alpha\nBETA!\n" {
		t.Fatalf("patched content = %q", after)
	}
	// countLines counts the empty line a trailing newline implies, so a
	// two-line file reports 3 -> 3. Pinning it keeps the receipt honest
	// against the line counts printed directly above it.
	if !strings.Contains(out, fmt.Sprintf("Patch applied to %s (3 → 3 lines)", path)) {
		t.Errorf("patch must keep its line-count line, got %q", out)
	}
	want := fmt.Sprintf("wrote %s (11 -> %d bytes, sha256:%s)", filepath.Base(path), len(after), receiptDigest(after))
	if got := receiptLastLine(out); got != want {
		t.Errorf("receipt line = %q, want %q (full result %q)", got, want, out)
	}
}

// TestEditReceipt_LastLineNamesDeltaAndDigest pins edit's receipt for both the
// single-edit and the multi-edit path: exactly one trailing receipt line, with
// a delta and digest that match the file on disk.
func TestEditReceipt_LastLineNamesDeltaAndDigest(t *testing.T) {
	ws, dir := realWorkspace(t)
	et := &EditTool{WS: ws}

	t.Run("single", func(t *testing.T) {
		path := filepath.Join(dir, "s.txt")
		writeFileAt(t, path, "alpha\n")
		out, err := et.Execute(context.Background(), fmt.Sprintf(`{"filePath":%q,"oldString":"alpha","newString":"alphabet"}`, path))
		if err != nil {
			t.Fatalf("edit failed: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("wrote %s (6 -> %d bytes, sha256:%s)", filepath.Base(path), len(after), receiptDigest(after))
		if got := receiptLastLine(out); got != want {
			t.Errorf("receipt line = %q, want %q (full result %q)", got, want, out)
		}
		if strings.Count(out, "sha256:") != 1 {
			t.Errorf("exactly one receipt line expected, got %q", out)
		}
	})

	t.Run("multi", func(t *testing.T) {
		path := filepath.Join(dir, "m.txt")
		writeFileAt(t, path, "one\ntwo\n")
		args, err := json.Marshal(map[string]any{
			"filePath": path,
			"edits": []map[string]string{
				{"oldString": "one", "newString": "ONE"},
				{"oldString": "two", "newString": "TWO"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		out, err := et.Execute(context.Background(), string(args))
		if err != nil {
			t.Fatalf("multi-edit failed: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != "ONE\nTWO\n" {
			t.Fatalf("multi-edit content = %q", after)
		}
		want := fmt.Sprintf("wrote %s (8 -> %d bytes, sha256:%s)", filepath.Base(path), len(after), receiptDigest(after))
		if got := receiptLastLine(out); got != want {
			t.Errorf("receipt line = %q, want %q (full result %q)", got, want, out)
		}
		if strings.Count(out, "sha256:") != 1 {
			t.Errorf("exactly one receipt line expected, got %q", out)
		}
	})
}
