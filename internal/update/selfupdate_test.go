package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
		filepath.Join(dir, BinaryName(runtime.GOOS)+".update-1234.ps1"),
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

// Discarding is for a staged update the running binary has already
// overtaken: every artifact goes.
func TestDiscardStagedIn(t *testing.T) {
	dir := t.TempDir()
	artifacts := []string{
		filepath.Join(dir, BinaryName(runtime.GOOS)+".next"),
		filepath.Join(dir, BinaryName(runtime.GOOS)+".update.log"),
		filepath.Join(dir, OldName(runtime.GOOS)),
		filepath.Join(dir, OldName(runtime.GOOS)+".tmp"),
	}
	for _, p := range artifacts {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	discardStagedIn(dir)

	if _, ok := pendingIn(dir); ok {
		t.Fatal("staged binary survived the discard")
	}
	for _, p := range artifacts {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("discard kept %s", filepath.Base(p))
		}
	}
}

// The swap script's branches are exercised for real on Windows: a happy
// path and the retry path where another process holds the target. The
// permanent-lock path takes the full two-minute retry budget and is not
// run here.
func TestWindowsSwapScriptExecution(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only swap script")
	}
	if testing.Short() {
		t.Skip("runs powershell.exe")
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe not available")
	}

	target := func(dir string) string { return filepath.Join(dir, "yaah.exe") }
	staged := func(dir string) string { return filepath.Join(dir, "yaah.exe.next") }

	// run executes the generated script and waits for it to finish.
	run := func(t *testing.T, dir string) {
		t.Helper()
		scriptPath := filepath.Join(dir, "swap.ps1")
		script := windowsSwapScript(
			target(dir),
			filepath.Join(dir, OldName(runtime.GOOS)),
			staged(dir),
			scriptPath,
			filepath.Join(dir, "yaah.exe.update.log"),
			999999, // no such process: the wait loop exits at once
		)
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("swap script failed: %v\n%s", err, out)
		}
	}

	assertSwapped := func(t *testing.T, dir string) {
		t.Helper()
		got, err := os.ReadFile(target(dir))
		if err != nil {
			t.Fatalf("read target: %v", err)
		}
		if string(got) != "new binary" {
			t.Errorf("target = %q; want the staged binary", got)
		}
		for _, residual := range []string{
			staged(dir),
			filepath.Join(dir, OldName(runtime.GOOS)),
			filepath.Join(dir, OldName(runtime.GOOS)+".tmp"),
			filepath.Join(dir, "yaah.exe.update.log"),
		} {
			if _, err := os.Stat(residual); !os.IsNotExist(err) {
				t.Errorf("swap left %s behind", filepath.Base(residual))
			}
		}
	}

	seed := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(target(dir), []byte("old binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(staged(dir), []byte("new binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("applies", func(t *testing.T) {
		dir := t.TempDir()
		seed(t, dir)
		run(t, dir)
		assertSwapped(t, dir)
	})

	t.Run("retries until the target is released", func(t *testing.T) {
		dir := t.TempDir()
		seed(t, dir)

		// Hold the target with no sharing for three seconds. The marker
		// file proves the handle was acquired before the swap starts.
		marker := filepath.Join(dir, "locked")
		hold := strings.ReplaceAll(target(dir), "'", "''")
		locker := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command",
			fmt.Sprintf("$fs=[IO.File]::Open('%s','Open','ReadWrite','None'); Set-Content -Path '%s' -Value locked; Start-Sleep -Seconds 3; $fs.Close()",
				hold, strings.ReplaceAll(marker, "'", "''")))
		if err := locker.Start(); err != nil {
			t.Fatalf("start locker: %v", err)
		}
		defer func() { _ = locker.Process.Kill() }()

		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("locker never acquired the target")
			}
			time.Sleep(100 * time.Millisecond)
		}

		run(t, dir) // blocks until the lock is released and the retry lands
		assertSwapped(t, dir)
	})
}
