package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGlobTool_findsMatchingFiles(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "b.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "c.txt"), []byte("x"), 0o644)

	gt := &GlobTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "*.go", "path": tmp})
	result, err := gt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !contains(result, "a.go") || !contains(result, "b.go") {
		t.Errorf("expected a.go and b.go, got %q", result)
	}
	if contains(result, "c.txt") {
		t.Errorf("should not contain c.txt, got %q", result)
	}
}

func TestGlobTool_noMatches(t *testing.T) {
	tmp := t.TempDir()
	gt := &GlobTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "*.nonexistent", "path": tmp})
	result, err := gt.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "No files found." {
		t.Errorf("expected 'No files found.', got %q", result)
	}
}

func TestGlobTool_returnsErrorForEmptyPattern(t *testing.T) {
	gt := &GlobTool{}
	_, err := gt.Execute(context.Background(), `{"path":"."}`)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestGlobNative_directoryPrefixedPatterns(t *testing.T) {
	tmp := t.TempDir()
	nested := filepath.Join(tmp, "shepherd-ref", "workspace-handles")
	os.MkdirAll(nested, 0o755)
	os.WriteFile(filepath.Join(nested, "a.py"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "shepherd-ref", "b.py"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "top.py"), []byte("x"), 0o644)

	tests := []struct {
		name    string
		pattern string
		want    []string
		notWant []string
	}{
		{
			name:    "recursive directory prefix",
			pattern: "shepherd-ref/**/*.py",
			want:    []string{"a.py", "b.py"},
			notWant: []string{"top.py"},
		},
		{
			name:    "single-level directory prefix",
			pattern: "shepherd-ref/workspace-handles/*.py",
			want:    []string{"a.py"},
			notWant: []string{"b.py", "top.py"},
		},
		{
			name:    "bare recursive pattern",
			pattern: "**/*.py",
			want:    []string{"a.py", "b.py", "top.py"},
		},
		{
			name:    "absolute pattern",
			pattern: filepath.ToSlash(filepath.Join(tmp, "shepherd-ref", "**", "*.py")),
			want:    []string{"a.py", "b.py"},
			notWant: []string{"top.py"},
		},
		{
			name:    "basename pattern",
			pattern: "*.py",
			want:    []string{"a.py", "b.py", "top.py"},
		},
	}

	gt := &GlobTool{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := gt.globNative(context.Background(), tc.pattern, tmp)
			if err != nil {
				t.Fatalf("globNative() error: %v", err)
			}
			for _, want := range tc.want {
				if !contains(result, want) {
					t.Errorf("pattern %q: expected %q in %q", tc.pattern, want, result)
				}
			}
			for _, notWant := range tc.notWant {
				if contains(result, notWant) {
					t.Errorf("pattern %q: did not expect %q in %q", tc.pattern, notWant, result)
				}
			}
		})
	}
}
