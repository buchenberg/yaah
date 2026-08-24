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
	name := filepath.Base(assetPath)
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
// On Unix, the running binary is renamed to .old and the new binary is
// written in its place. The OS preserves the running process's inode
// reference after the rename, so the replace is safe.
//
// On Windows, the running binary cannot be reliably replaced in-process.
// Instead, the new binary is staged alongside as yaah.exe.next and a
// detached PowerShell script is launched to swap it after this process
// exits. The stale .next is cleaned up on the next startup.
func Apply(newPath string) error {
	currentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate current binary: %w", err)
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
		return err
	}

	if runtime.GOOS == "windows" {
		return applyWindows(newPath, targetPath)
	}
	return applyUnix(newPath, targetPath, binDir)
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

func applyWindows(newPath, targetPath string) error {
	nextPath := targetPath + ".next"
	oldPath := filepath.Join(filepath.Dir(targetPath), OldName(runtime.GOOS))

	if err := copyFileEx(newPath, nextPath); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	scriptPath := targetPath + ".update.ps1"
	pid := os.Getpid()

	// The swap runs detached after this process exits. It backs the
	// current binary up to .old first and restores it if the move
	// fails — mirroring the Unix rollback (review finding S5).
	script := fmt.Sprintf(
		`$id = %[5]d
$timeout = 60
while ($timeout -gt 0 -and (Get-Process -Id $id -ErrorAction SilentlyContinue)) {
	Start-Sleep -Milliseconds 250
	$timeout--
}
Start-Sleep -Milliseconds 500
Copy-Item -Force -LiteralPath "%[1]s" -Destination "%[2]s"
Move-Item -Force -LiteralPath "%[3]s" -Destination "%[1]s"
Start-Sleep -Milliseconds 500
if (Test-Path -LiteralPath "%[1]s") {
	Remove-Item -LiteralPath "%[2]s" -Force -ErrorAction SilentlyContinue
} else {
	Move-Item -Force -LiteralPath "%[2]s" -Destination "%[1]s" -ErrorAction SilentlyContinue
}
Remove-Item -LiteralPath "%[4]s" -Force -ErrorAction SilentlyContinue
`,
		escapePSPath(targetPath),
		escapePSPath(oldPath),
		escapePSPath(nextPath),
		escapePSPath(scriptPath),
		pid,
	)

	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return fmt.Errorf("write update script: %w", err)
	}

	cmd := exec.Command("powershell.exe",
		"-WindowStyle", "Hidden",
		"-ExecutionPolicy", "Bypass",
		"-File", scriptPath,
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch update script: %w", err)
	}

	return nil
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

// CleanOldBinaries removes leftover update artifacts (.old, .next,
// .update.ps1) next to the current executable. Called lazily by the
// update command — not on every yaah invocation.
func CleanOldBinaries() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	os.Remove(filepath.Join(dir, OldName(runtime.GOOS)))
	os.Remove(filepath.Join(dir, BinaryName(runtime.GOOS)+".next"))
	os.Remove(filepath.Join(dir, BinaryName(runtime.GOOS)+".update.ps1"))
}
