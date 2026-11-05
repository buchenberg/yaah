package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

func TestLocalWorkspaceAppendAndReadRange(t *testing.T) {
	dir := t.TempDir()
	ws := newLocalWorkspace(NewPathValidator(dir, false, nil))
	path := filepath.Join(dir, "ledger.jsonl")

	if err := ws.Append(context.Background(), path, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if err := ws.Append(context.Background(), path, []byte("two\n"), 0o600); err != nil {
		t.Fatalf("second Append: %v", err)
	}
	data, err := ws.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "one\ntwo\n" {
		t.Fatalf("appended content = %q", data)
	}
	if runtime.GOOS != "windows" {
		// Windows does not model Unix permission bits; the host open path is
		// still O_APPEND, so there is nothing portable to assert there.
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("append changed the existing mode: %v %v", info, err)
		}
	}

	got, err := ws.ReadRange(context.Background(), path, 2, 4)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if string(got) != "e\ntw" {
		t.Fatalf("ReadRange = %q", got)
	}
	got, err = ws.ReadRange(context.Background(), path, 100, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("ReadRange past EOF = %q, %v", got, err)
	}
	if _, err := ws.ReadRange(context.Background(), path, -1, 4); err == nil {
		t.Fatal("ReadRange accepted a negative offset")
	}
}

func TestSandboxWorkspaceAppendAndReadRange(t *testing.T) {
	ctx := context.Background()
	sb := sandboxfake.New()
	if err := sb.Create(ctx, goatherdsandbox.SandboxSpec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ws := newSandboxWorkspace(sb, "/")
	if err := ws.Append(ctx, "/ledger.jsonl", []byte("one\n"), 0o644); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if err := ws.Append(ctx, "/ledger.jsonl", []byte("two\n"), 0o600); err != nil {
		t.Fatalf("second Append: %v", err)
	}
	data, err := ws.ReadFile(ctx, "/ledger.jsonl")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "one\ntwo\n" {
		t.Fatalf("appended content = %q", data)
	}
	got, err := ws.ReadRange(ctx, "/ledger.jsonl", 2, 4)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if string(got) != "e\ntw" {
		t.Fatalf("ReadRange = %q", got)
	}
	got, err = ws.ReadRange(ctx, "/ledger.jsonl", 100, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("ReadRange past EOF = %q, %v", got, err)
	}
	if _, err := ws.ReadRange(ctx, "/ledger.jsonl", -1, 4); err == nil {
		t.Fatal("sandbox ReadRange accepted a negative offset")
	}
}

func TestIsCrossDeviceMoveError(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		want   bool
	}{
		{"mv: cannot move 'a' to 'b': Invalid cross-device link", true},
		{"mv: cannot move 'a' to 'b': cross-device link", true},
		{"mv: cannot move 'cross-device' to 'b': Permission denied", false},
		{"mv: cannot move 'a' to 'b': No such file or directory", false},
	} {
		if got := isCrossDeviceMoveError(tc.stderr); got != tc.want {
			t.Errorf("isCrossDeviceMoveError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

func TestLocalWorkspaceConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	ws := newLocalWorkspace(NewPathValidator(dir, false, nil))
	path := filepath.Join(dir, "concurrent.log")

	var wg sync.WaitGroup
	for writer := 0; writer < 2; writer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := ws.Append(context.Background(), path, []byte("line\n"), 0o644); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	data, err := ws.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := strings.Count(string(data), "line\n"); got != 200 {
		t.Fatalf("concurrent appends kept %d lines, want 200", got)
	}
}
