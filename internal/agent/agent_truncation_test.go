package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/config"
)

func testLoop() *Loop { return &Loop{} }

func TestTruncateToolResult_shortResult(t *testing.T) {
	result := "short output"
	got := testLoop().truncateToolResult(result)
	if got != result {
		t.Errorf("short result should be unchanged, got %q", got)
	}
}

func TestTruncateToolResult_lineCapped(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < defaultTruncateMaxLines+100; i++ {
		sb.WriteString("line\n")
	}
	result := sb.String()
	got := testLoop().truncateToolResult(result)

	if !strings.Contains(got, "[output truncated at") {
		t.Errorf("line-capped result missing truncation marker: %s", got[len(got)-200:])
	}
	if !strings.Contains(got, "read tool with offset/limit") {
		t.Errorf("line-capped result missing recovery hint")
	}

	truncatedDir := filepath.Join(config.HomeDir(), "truncated")
	entries, _ := os.ReadDir(truncatedDir)
	if len(entries) == 0 {
		t.Error("expected spillover file in truncated dir")
	}
	for _, e := range entries {
		p := filepath.Join(truncatedDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if string(data) == result {
			return
		}
	}
	t.Error("did not find spillover file with full content")
}

func TestTruncateToolResult_byteCapped(t *testing.T) {
	var sb strings.Builder
	line := strings.Repeat("x", 80) + "\n"
	for i := 0; i < 1000; i++ {
		sb.WriteString(line)
	}
	result := sb.String()

	got := testLoop().truncateToolResult(result)

	if len(got) > len(result) {
		t.Errorf("byte-capped result should be shorter than input")
	}
	if !strings.Contains(got, "[output truncated at") {
		t.Errorf("byte-capped result missing truncation marker")
	}
}

func TestTruncateToolResult_emptyResult(t *testing.T) {
	got := testLoop().truncateToolResult("")
	if got != "" {
		t.Errorf("empty result should remain empty, got %q", got)
	}

	beforeEntries := countTruncatedFiles()
	_ = testLoop().truncateToolResult("")
	afterEntries := countTruncatedFiles()
	if afterEntries > beforeEntries {
		t.Error("empty result should not create spillover file")
	}
}

func TestTruncateToolResult_bothLimitsExceeded(t *testing.T) {
	line := strings.Repeat("x", 80) + "\n"
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		sb.WriteString(line)
	}
	result := sb.String()
	got := testLoop().truncateToolResult(result)

	if !strings.Contains(got, "[output truncated at") {
		t.Errorf("result missing truncation marker when both limits exceeded")
	}
}

func TestTruncateToolResult_binaryContent(t *testing.T) {
	result := strings.Repeat("\x00\x01\x02\x03", 20000)
	got := testLoop().truncateToolResult(result)
	if len(got) > len(result) {
		t.Errorf("binary content should be truncated")
	}
	if !strings.Contains(got, "[output truncated at") && !strings.Contains(got, "[truncated]") {
		t.Errorf("binary content result missing truncation marker")
	}
}

func TestTruncateToolResult_lazyDirCreation(t *testing.T) {
	spillDir := filepath.Join(config.HomeDir(), "truncated")
	os.RemoveAll(spillDir)

	if _, err := os.Stat(spillDir); err == nil {
		t.Fatal("spill dir should not exist before test")
	}

	var sb strings.Builder
	for i := 0; i < 2500; i++ {
		sb.WriteString("line\n")
	}
	_ = testLoop().truncateToolResult(sb.String())

	if _, err := os.Stat(spillDir); err != nil {
		t.Errorf("spill dir was not created lazily: %v", err)
	}
}

func TestCleanTruncatedDir(t *testing.T) {
	spillDir := filepath.Join(config.HomeDir(), "truncated")
	os.MkdirAll(spillDir, 0o755)

	oldFile := filepath.Join(spillDir, "old.txt")
	newFile := filepath.Join(spillDir, "new.txt")

	os.WriteFile(oldFile, []byte("old"), 0o644)
	os.WriteFile(newFile, []byte("new"), 0o644)

	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(oldFile, oldTime, oldTime)

	cleanTruncatedDir(spillDir)

	if _, err := os.Stat(oldFile); err == nil {
		t.Error("old file should have been cleaned up")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Error("new file should still exist")
	}
}

func countTruncatedFiles() int {
	dir := filepath.Join(config.HomeDir(), "truncated")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}
