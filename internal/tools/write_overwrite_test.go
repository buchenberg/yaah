package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeArgs builds the tool arguments for a write call, adding flags only when
// they are set so an omitted flag really is omitted.
func writeArgs(path, content string, extra map[string]any) string {
	fields := map[string]any{"filePath": path, "content": content}
	for k, v := range extra {
		fields[k] = v
	}
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// TestWrite_RefusesBlindOverwrite pins the core of yaah-5hkb: write means
// "replace this file wholesale", and an existing path without overwrite is
// refused with nothing changed. The refusal has to be useful, so it names what
// would be lost and the three ways out.
func TestWrite_RefusesBlindOverwrite(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "existing.txt")
	original := "line one\nline two\nline three\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := &WriteTool{WS: ws}
	_, err := wt.Execute(context.Background(), writeArgs(path, "clobbered", nil))
	if err == nil {
		t.Fatal("blind overwrite succeeded; want a refusal")
	}
	var refused *OverwriteRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v (%T), want *OverwriteRefusedError", err, err)
	}
	if refused.Bytes != len(original) || refused.Lines != 3 {
		t.Errorf("refusal reports %d bytes / %d lines; want %d / 3", refused.Bytes, refused.Lines, len(original))
	}
	for _, want := range []string{"already exists", "overwrite: true", "edit", "patch", "different path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Errorf("content = %q; a refusal must leave the file byte-identical", data)
	}
}

// TestWrite_ApprovedOverwriteStashesPrevious pins the recovery half: the bytes
// the overwrite destroys are kept, and the result says where.
func TestWrite_ApprovedOverwriteStashesPrevious(t *testing.T) {
	ws, dir := realWorkspace(t)
	stash := filepath.Join(dir, ".scratch", "overwritten")
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("previous content"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := &WriteTool{WS: ws}
	result, err := wt.Execute(context.Background(), writeArgs(path, "new content", map[string]any{"overwrite": true}))
	if err != nil {
		t.Fatalf("approved overwrite failed: %v", err)
	}
	if !strings.Contains(result, "stashed at") {
		t.Errorf("result = %q, want the stash path reported", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Errorf("content = %q, want the new content", data)
	}

	entries, err := os.ReadDir(stash)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("stash holds %d entries, want 1", len(entries))
	}
	stashed, err := os.ReadFile(filepath.Join(stash, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(stashed) != "previous content" {
		t.Errorf("stash holds %q, want the previous content", stashed)
	}
	if !strings.Contains(entries[0].Name(), "existing.txt") {
		t.Errorf("stash entry %q does not name the source file", entries[0].Name())
	}
}

// TestWrite_StashFailureRefusesTheOverwrite pins fail-closed: a replace that
// cannot be recovered is not performed.
func TestWrite_StashFailureRefusesTheOverwrite(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file where the stash directory should be: MkdirAll cannot succeed.
	blocked := filepath.Join(dir, ".scratch")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := &WriteTool{WS: ws}
	if _, err := wt.Execute(context.Background(), writeArgs(path, "new", map[string]any{"overwrite": true})); err == nil {
		t.Fatal("overwrite succeeded with an unusable stash; want a refusal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep me" {
		t.Errorf("content = %q; a stash failure must leave the file untouched", data)
	}
}

// TestWrite_IfDigest pins the precondition: it verifies the replace instead of
// trusting it, and refuses with both digests when the file is not what the
// caller read.
func TestWrite_IfDigest(t *testing.T) {
	ws, dir := realWorkspace(t)
	path := filepath.Join(dir, "guarded.txt")
	if err := os.WriteFile(path, []byte("as read"), 0o644); err != nil {
		t.Fatal(err)
	}
	full := fileContentDigest([]byte("as read"))

	t.Run("a receipt digest prefix matches", func(t *testing.T) {
		wt := &WriteTool{WS: ws}
		args := writeArgs(path, "replaced", map[string]any{"overwrite": true, "if_digest": "sha256:" + full[:12]})
		if _, err := wt.Execute(context.Background(), args); err != nil {
			t.Fatalf("write with a matching if_digest failed: %v", err)
		}
	})

	t.Run("a bare full digest matches", func(t *testing.T) {
		// Restore the file the previous subtest replaced.
		if err := os.WriteFile(path, []byte("as read"), 0o644); err != nil {
			t.Fatal(err)
		}
		wt := &WriteTool{WS: ws}
		args := writeArgs(path, "replaced again", map[string]any{"overwrite": true, "if_digest": full})
		if _, err := wt.Execute(context.Background(), args); err != nil {
			t.Fatalf("write with a bare full if_digest failed: %v", err)
		}
	})

	t.Run("a stale digest is refused", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("as read"), 0o644); err != nil {
			t.Fatal(err)
		}
		wt := &WriteTool{WS: ws}
		stale := fileContentDigest([]byte("something else"))
		args := writeArgs(path, "should not land", map[string]any{"overwrite": true, "if_digest": "sha256:" + stale[:12]})
		_, err := wt.Execute(context.Background(), args)
		if err == nil {
			t.Fatal("mismatched if_digest accepted; want a refusal")
		}
		var mismatch *DigestMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("error = %v (%T), want *DigestMismatchError", err, err)
		}
		for _, want := range []string{"if_digest does not match", "expected", "found", "re-read"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("mismatch %q does not mention %q", err, want)
			}
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != "as read" {
			t.Errorf("content = %q; a digest mismatch must leave the file untouched", data)
		}
	})

	t.Run("if_digest on a missing path is refused", func(t *testing.T) {
		wt := &WriteTool{WS: ws}
		missing := filepath.Join(dir, "does-not-exist.txt")
		args := writeArgs(missing, "x", map[string]any{"if_digest": full})
		_, err := wt.Execute(context.Background(), args)
		if err == nil {
			t.Fatal("if_digest on a missing path was accepted; create and replace must not be confused")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("error = %q, want it to say the path does not exist", err)
		}
	})
}

// TestDigestMatches pins the accepted forms. Receipts print a 12-hex prefix, so
// that is the shortest thing a caller can copy; shorter prefixes collide too
// often to be a precondition.
func TestDigestMatches(t *testing.T) {
	full := fileContentDigest([]byte("payload"))
	tests := []struct {
		name  string
		given string
		want  bool
	}{
		{"receipt prefix", "sha256:" + full[:12], true},
		{"bare prefix", full[:12], true},
		{"full hex", full, true},
		{"upper case", strings.ToUpper("sha256:" + full[:12]), true},
		{"too short", full[:8], false},
		{"wrong content", strings.Repeat("0", 12), false},
		{"not hex", "sha256:zzzzzzzzzzzz", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := digestMatches(tc.given, full); got != tc.want {
				t.Errorf("digestMatches(%q) = %v, want %v", tc.given, got, tc.want)
			}
		})
	}
}

// TestPruneOverwriteStash pins the bound: the stash keeps the newest entries so
// a loop that overwrites the same file every turn cannot fill the disk.
func TestPruneOverwriteStash(t *testing.T) {
	ws, dir := realWorkspace(t)
	stash := filepath.Join(dir, ".scratch", "overwritten")
	if err := os.MkdirAll(stash, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		name := filepath.Join(stash, fmt.Sprintf("entry-%d", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Distinct mtimes so "newest" is well defined.
		mod := time.Now().Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(name, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	pruneOverwriteStash(context.Background(), ws, stash, 2)
	entries, err := os.ReadDir(stash)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("stash holds %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Name() != "entry-3" && e.Name() != "entry-4" {
			t.Errorf("kept %q; want the two newest entries", e.Name())
		}
	}
}

// TestLineCount pins the count the refusal reports, which is the number a human
// would read off an editor.
func TestLineCount(t *testing.T) {
	tests := []struct {
		content string
		want    int
	}{
		{"", 0},
		{"one line", 1},
		{"one\n", 1},
		{"one\ntwo", 2},
		{"one\ntwo\n", 2},
		{"\n", 1},
	}
	for _, tc := range tests {
		if got := lineCount([]byte(tc.content)); got != tc.want {
			t.Errorf("lineCount(%q) = %d, want %d", tc.content, got, tc.want)
		}
	}
}
