package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "yaah-test-amd64")
	content := []byte("fake binary content")
	if err := os.WriteFile(asset, content, 0o755); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	checksums := []byte(strings.Join([]string{
		"0000000000000000000000000000000000000000000000000000000000000000  other-asset",
		good + "  yaah-test-amd64",
		"",
	}, "\n"))

	if err := VerifyChecksum(asset, checksums); err != nil {
		t.Errorf("valid checksum rejected: %v", err)
	}

	t.Run("mismatch fails closed", func(t *testing.T) {
		bad := []byte("1111111111111111111111111111111111111111111111111111111111111111  yaah-test-amd64")
		err := VerifyChecksum(asset, bad)
		if err == nil {
			t.Fatal("mismatched checksum accepted")
		}
		if !strings.Contains(err.Error(), "mismatch") {
			t.Errorf("error = %q; want mismatch reason", err)
		}
	})

	t.Run("missing entry fails closed", func(t *testing.T) {
		err := VerifyChecksum(asset, []byte("abc  unrelated-file\n"))
		if err == nil {
			t.Fatal("missing checksum entry accepted")
		}
		if !strings.Contains(err.Error(), "no SHA-256 checksum entry") {
			t.Errorf("error = %q; want missing-entry reason", err)
		}
	})

	t.Run("uppercase digest accepted", func(t *testing.T) {
		upper := []byte(strings.ToUpper(good) + "  yaah-test-amd64")
		if err := VerifyChecksum(asset, upper); err != nil {
			t.Errorf("uppercase digest rejected: %v", err)
		}
	})

	t.Run("tampered asset fails", func(t *testing.T) {
		tampered := filepath.Join(dir, "tampered")
		if err := os.WriteFile(tampered, []byte("tampered content"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := VerifyChecksum(tampered, checksums); err == nil {
			t.Fatal("asset without checksum entry accepted")
		}
	})
}

func TestEscapePSPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `C:\bin\yaah.exe`, `C:\bin\yaah.exe`},
		{"double quote", `C:\we"ird\yaah.exe`, "C:\\we`\"ird\\yaah.exe"},
		{"subexpression", `C:\$(calc)\yaah.exe`, "C:\\`$(calc)\\yaah.exe"},
		{"backtick", "C:\\we`ird\\yaah.exe", "C:\\we``ird\\yaah.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapePSPath(tt.in); got != tt.want {
				t.Errorf("escapePSPath(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLookupChecksum(t *testing.T) {
	checksums := []byte("abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234  dir/yaah-linux-amd64\nnot a digest line\n")
	if got := lookupChecksum(checksums, "yaah-linux-amd64"); got != "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234" {
		t.Errorf("lookupChecksum = %q", got)
	}
	if got := lookupChecksum(checksums, "missing"); got != "" {
		t.Errorf("lookupChecksum(missing) = %q; want empty", got)
	}
	// A short non-64-char hex token must not be treated as a digest.
	if got := lookupChecksum([]byte("deadbeef  yaah-x\n"), "yaah-x"); got != "" {
		t.Errorf("short digest accepted: %q", got)
	}
}

// A staged binary keeps the name yaah.exe.next, so verifying it must use
// the release asset name rather than its on-disk name.
func TestVerifyChecksumAs(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, "yaah.exe.next")
	content := []byte("staged binary")
	if err := os.WriteFile(staged, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  yaah-windows-amd64.exe\n")

	if err := VerifyChecksumAs(staged, "yaah-windows-amd64.exe", checksums); err != nil {
		t.Fatalf("staged binary rejected: %v", err)
	}
	// Plain VerifyChecksum looks up the on-disk base name and must fail.
	if err := VerifyChecksum(staged, checksums); err == nil {
		t.Fatal("VerifyChecksum accepted the staged name; want missing-entry error")
	}
	if err := VerifyChecksumAs(staged, "yaah-windows-arm64.exe", checksums); err == nil {
		t.Fatal("wrong asset name accepted")
	}
}

func TestWindowsSwapScript(t *testing.T) {
	script := windowsSwapScript(
		`C:\bin\yaah.exe`,
		`C:\bin\yaah.old.exe`,
		`C:\bin\yaah.exe.next`,
		`C:\bin\yaah.exe.update.ps1`,
		`C:\bin\yaah.exe.update.log`,
		4242,
	)

	for _, want := range []string{
		`Get-Process -Id 4242`,
		`Move-Item -Force`,
		`catch { }`,
		`-LiteralPath $next`,
		`yaah.exe.update.log`,
		`processes holding the binary`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}

	// Hostile paths stay inside the double-quoted literals.
	hostile := windowsSwapScript(`C:\$(calc)\"x\yaah.exe`, "o", "n", "s", "l", 1)
	if strings.Contains(hostile, `"C:\$(calc)`) {
		t.Error("dollar subexpression not escaped")
	}
}

func TestPendingIn(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, BinaryName(runtime.GOOS)+".next")
	logPath := filepath.Join(dir, BinaryName(runtime.GOOS)+".update.log")

	if _, ok := pendingIn(dir); ok {
		t.Fatal("empty dir reported a pending update")
	}

	if err := os.WriteFile(staged, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	pending, ok := pendingIn(dir)
	if !ok {
		t.Fatal("staged binary not reported")
	}
	if pending.Path != staged {
		t.Errorf("Path = %q; want %q", pending.Path, staged)
	}
	if pending.Log != "" {
		t.Errorf("Log = %q; want empty", pending.Log)
	}

	if err := os.WriteFile(logPath, []byte("swap failed\nprocesses: 1, 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, ok = pendingIn(dir)
	if !ok || pending.LogPath != logPath {
		t.Fatalf("failure log not reported: %+v", pending)
	}
	if !strings.Contains(pending.Log, "swap failed") {
		t.Errorf("Log = %q; want failure text", pending.Log)
	}
}

// Cleanup must keep the staged binary and its log: they are the retry
// path after a blocked swap.
func TestCleanOldBinariesIn(t *testing.T) {
	dir := t.TempDir()
	keep := []string{
		filepath.Join(dir, BinaryName(runtime.GOOS)+".next"),
		filepath.Join(dir, BinaryName(runtime.GOOS)+".update.log"),
	}
	drop := []string{
		filepath.Join(dir, OldName(runtime.GOOS)),
		filepath.Join(dir, OldName(runtime.GOOS)+".tmp"),
		filepath.Join(dir, BinaryName(runtime.GOOS)+".update.ps1"),
	}
	for _, p := range append(append([]string{}, keep...), drop...) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cleanOldBinariesIn(dir)

	for _, p := range keep {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("cleanup removed %s; it is the retry path", filepath.Base(p))
		}
	}
	for _, p := range drop {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("cleanup kept %s", filepath.Base(p))
		}
	}
}
