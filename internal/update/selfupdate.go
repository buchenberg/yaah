// selfupdate.go downloads a release asset and replaces the running
// binary. The cmd layer owns presentation (progress output, version
// comparison messaging); everything with filesystem or network side
// effects lives here.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Release represents the relevant fields from the GitHub API response.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a downloadable release artifact.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// AssetURL returns the download URL for the named asset, or empty string.
func (r *Release) AssetURL(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// FetchLatestRelease queries the GitHub API for the latest release.
func FetchLatestRelease(repo string) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("cannot parse response: %w", err)
	}

	return &release, nil
}

// Download fetches url to path. The context bounds the whole download
// (dial + transfer); the client timeout backstops a stalled server.
func Download(ctx context.Context, url, path string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// VerifyChecksum verifies that the file at assetPath matches the
// SHA-256 digest recorded for its name in checksums (sha256sum format:
// one "<hex>  <filename>" per line). It fails closed: a missing entry
// or a digest mismatch aborts the update (review finding S5).
func VerifyChecksum(assetPath string, checksums []byte) error {
	return VerifyChecksumAs(assetPath, filepath.Base(assetPath), checksums)
}

// VerifyChecksumAs is VerifyChecksum for a file whose on-disk name
// differs from its release asset name — a staged yaah.exe.next reusing
// the yaah-windows-amd64.exe entry, for example.
func VerifyChecksumAs(assetPath, assetName string, checksums []byte) error {
	name := filepath.Base(assetName)
	want := lookupChecksum(checksums, name)
	if want == "" {
		return fmt.Errorf("no SHA-256 checksum entry for %q — refusing to apply", name)
	}

	f, err := os.Open(assetPath)
	if err != nil {
		return fmt.Errorf("open asset for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash asset: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(want))) != 1 {
		return fmt.Errorf("SHA-256 mismatch for %s: got %s, want %s — refusing to apply", name, got, want)
	}
	return nil
}

// lookupChecksum extracts the hex digest for filename from sha256sum
// output, or "" when absent.
func lookupChecksum(checksums []byte, filename string) string {
	scanner := bufio.NewScanner(bytes.NewReader(checksums))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		digest, name := fields[0], fields[len(fields)-1]
		if filepath.Base(name) == filename && isHexDigest(digest) {
			return strings.ToLower(digest)
		}
	}
	return ""
}

func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Apply replaces the current running binary with the downloaded one at
// newPath.
//
// On Unix the replacement happens in-process: the running binary is
// renamed to .old and the new binary is written in its place, so Apply
// reports false (the swap is already done). The OS preserves the running
// process's inode reference after the rename.
//
// On Windows the running binary cannot be replaced in-process. The new
// binary is staged alongside as yaah.exe.next and a detached PowerShell
// script swaps it after this process exits, so Apply reports true (the
// swap is deferred). When newPath is already the staged .next, staging
// is skipped and only the swap is (re)launched — that is the retry path
// after a swap blocked by another running yaah process.
func Apply(newPath string) (deferred bool, err error) {
	currentPath, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("cannot locate current binary: %w", err)
	}

	binDir := filepath.Dir(currentPath)
	binName := BinaryName(runtime.GOOS)
	targetPath := filepath.Join(binDir, binName)

	if targetPath != currentPath {
		resolved, err := filepath.EvalSymlinks(currentPath)
		if err == nil && resolved != currentPath {
			binDir = filepath.Dir(resolved)
			targetPath = filepath.Join(binDir, binName)
		}
	}

	if err := checkWriteAccess(binDir); err != nil {
		return false, err
	}

	if runtime.GOOS == "windows" {
		return applyWindows(newPath, targetPath)
	}
	return false, applyUnix(newPath, targetPath, binDir)
}

func checkWriteAccess(dir string) error {
	testFile := filepath.Join(dir, ".yaah-write-test")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		os.Remove(testFile)
		return fmt.Errorf("cannot write to %s — you may need to re-run the install script or use sudo", dir)
	}
	os.Remove(testFile)
	return nil
}

func applyUnix(newPath, targetPath, binDir string) error {
	oldPath := filepath.Join(binDir, OldName(runtime.GOOS))

	if err := os.Rename(targetPath, oldPath); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}

	src, err := os.Open(newPath)
	if err != nil {
		return fmt.Errorf("open new binary: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		os.Rename(oldPath, targetPath)
		return fmt.Errorf("create new binary: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(targetPath)
		os.Rename(oldPath, targetPath)
		return fmt.Errorf("write new binary: %w", err)
	}

	return nil
}

func applyWindows(newPath, targetPath string) (bool, error) {
	nextPath := targetPath + ".next"
	oldPath := filepath.Join(filepath.Dir(targetPath), OldName(runtime.GOOS))
	logPath := targetPath + ".update.log"

	// newPath == nextPath happens when a previously staged binary is
	// being retried; copying a file onto itself would truncate it.
	if filepath.Clean(newPath) != filepath.Clean(nextPath) {
		if err := copyFileEx(newPath, nextPath); err != nil {
			return false, fmt.Errorf("stage new binary: %w", err)
		}
	}

	scriptPath := targetPath + ".update.ps1"
	script := windowsSwapScript(targetPath, oldPath, nextPath, scriptPath, logPath, os.Getpid())
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return false, fmt.Errorf("write update script: %w", err)
	}

	cmd := exec.Command("powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-WindowStyle", "Hidden",
		"-ExecutionPolicy", "Bypass",
		"-File", scriptPath,
	)
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("launch update script: %w", err)
	}

	return true, nil
}

// windowsSwapScript builds the detached swap script. It waits for the
// updating process to exit, then retries the move for two minutes:
// antivirus scanners and other running yaah processes keep the image
// mapped, and a single attempt loses that race silently.
//
// Every outcome is durable. On success the backup and any stale failure
// log are removed; on failure the staged .next stays put and a log names
// the error and the processes still holding the binary, so the next
// `yaah update` can report it and retry without re-downloading.
func windowsSwapScript(targetPath, oldPath, nextPath, scriptPath, logPath string, pid int) string {
	return fmt.Sprintf(`$target = "%[1]s"
$old    = "%[2]s"
$next   = "%[3]s"
$log    = "%[5]s"

# Wait for the updater to exit, then let Windows release the image mapping.
$deadline = (Get-Date).AddSeconds(120)
while ((Get-Process -Id %[6]d -ErrorAction SilentlyContinue) -and ((Get-Date) -lt $deadline)) {
	Start-Sleep -Milliseconds 250
}
Start-Sleep -Milliseconds 500

# One backup copy of the binary being replaced; the move below either
# succeeds or leaves the target untouched. A locked target is expected
# and must not abort the script.
$applied = $false
$lastError = ''
for ($i = 0; $i -lt 240 -and -not $applied; $i++) {
	try {
		if (-not (Test-Path -LiteralPath $next)) { throw 'staged binary is missing' }
		if (-not (Test-Path -LiteralPath $old)) {
			Copy-Item -Force -LiteralPath $target -Destination ($old + '.tmp') -ErrorAction Stop
			Move-Item -Force -LiteralPath ($old + '.tmp') -Destination $old -ErrorAction Stop
		}
		Move-Item -Force -LiteralPath $next -Destination $target -ErrorAction Stop
		$applied = $true
	} catch {
		$lastError = $_.Exception.Message.Trim()
		# A partial replace can leave the target missing; restore it
		# before retrying so a working binary always exists.
		if ((-not (Test-Path -LiteralPath $target)) -and (Test-Path -LiteralPath $old)) {
			try { Move-Item -Force -LiteralPath $old -Destination $target -ErrorAction Stop } catch { }
		}
		Start-Sleep -Milliseconds 500
	}
}

if ($applied -and (Test-Path -LiteralPath $target)) {
	try { Remove-Item -LiteralPath $old -Force -ErrorAction Stop } catch { }
	try { Remove-Item -LiteralPath ($old + '.tmp') -Force -ErrorAction Stop } catch { }
	try { Remove-Item -LiteralPath $log -Force -ErrorAction Stop } catch { }
} else {
	$holders = @(Get-Process -Name yaah -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Id)" }) -join ', '
	if ($holders -eq '') { $holders = 'none detected (a scanner or another user may hold the file)' }
	if ((-not (Test-Path -LiteralPath $target)) -and (Test-Path -LiteralPath $old)) {
		try { Move-Item -Force -LiteralPath $old -Destination $target -ErrorAction Stop } catch { }
	}
	try {
		@(
			"yaah update could not replace $target",
			"error: $lastError",
			"processes holding the binary: $holders",
			"Close every running yaah process, then run 'yaah update' again."
		) | Set-Content -LiteralPath $log -Encoding ASCII -ErrorAction Stop
	} catch { }
}
try { Remove-Item -LiteralPath "%[4]s" -Force -ErrorAction Stop } catch { }
`,
		escapePSPath(targetPath),
		escapePSPath(oldPath),
		escapePSPath(nextPath),
		escapePSPath(scriptPath),
		escapePSPath(logPath),
		pid,
	)
}

// escapePSPath escapes p for embedding inside a DOUBLE-quoted
// PowerShell string — the script's actual quoting context. Backtick,
// double quote, and dollar are the metacharacters there ($ can open a
// $(...) subexpression). The previous single-quote escaping did not
// match the double-quoted embedding and let hostile paths inject
// script (review finding S5).
func escapePSPath(p string) string {
	const (
		psBacktick = "`"
		psQuote    = `"`
		psDollar   = "$"
	)
	p = strings.ReplaceAll(p, psBacktick, psBacktick+psBacktick)
	p = strings.ReplaceAll(p, psQuote, psBacktick+psQuote)
	p = strings.ReplaceAll(p, psDollar, psBacktick+psDollar)
	return p
}

func copyFileEx(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

// PendingState describes a downloaded update that has not been swapped
// into place yet.
type PendingState struct {
	Path     string    // staged binary (yaah.exe.next)
	StagedAt time.Time // when the staged binary was written
	LogPath  string    // failure log written by the swap script, if any
	Log      string    // contents of that log
}

// Pending reports a staged-but-unapplied update next to the running
// binary. On Windows this is the normal state between `yaah update` and
// the exit of the updating process; a state that survives the exit means
// the swap was blocked (another yaah process held the binary) or the
// machine shut down first.
func Pending() (PendingState, bool) {
	exe, err := os.Executable()
	if err != nil {
		return PendingState{}, false
	}
	// Mirror Apply: artifacts live next to the real binary, not next to
	// a symlink pointing at it.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return pendingIn(filepath.Dir(exe))
}

func pendingIn(dir string) (PendingState, bool) {
	staged := filepath.Join(dir, BinaryName(runtime.GOOS)+".next")
	fi, err := os.Stat(staged)
	if err != nil || fi.IsDir() {
		return PendingState{}, false
	}
	st := PendingState{Path: staged, StagedAt: fi.ModTime()}
	logPath := filepath.Join(dir, BinaryName(runtime.GOOS)+".update.log")
	if b, err := os.ReadFile(logPath); err == nil {
		st.LogPath = logPath
		st.Log = strings.TrimSpace(string(b))
	}
	return st, true
}

// CleanOldBinaries removes leftover update artifacts next to the
// current executable. Called lazily by the update command — not on every
// yaah invocation.
//
// The staged .next and its failure log are deliberately kept: they are
// the retry path when a swap could not be applied, and `yaah update`
// reuses a checksum-verified staged binary instead of downloading it
// again.
func CleanOldBinaries() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cleanOldBinariesIn(filepath.Dir(exe))
}

func cleanOldBinariesIn(dir string) {
	os.Remove(filepath.Join(dir, OldName(runtime.GOOS)))
	os.Remove(filepath.Join(dir, OldName(runtime.GOOS)+".tmp"))
	os.Remove(filepath.Join(dir, BinaryName(runtime.GOOS)+".update.ps1"))
}
