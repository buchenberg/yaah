package yaah

import (
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

	"github.com/buchenberg/yaah/internal/update"
	"github.com/spf13/cobra"
)

// GitHub repo for release checks. Can be overridden via -ldflags for forks.
var gitHubRepo = "buchenberg/yaah"

// updateCmd is the `yaah update` subcommand. Without flags, it checks
// for a newer release and applies it automatically.
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for and apply yaah updates",
	Long: `Check for a newer yaah release and apply it.

Without flags, this command checks for updates and, if a newer version
is available, downloads it and replaces the current binary atomically.

Subcommands:
  yaah update check     Check for updates without downloading (read-only).`,
	Args: cobra.NoArgs,
	RunE: runUpdate,
}

// updateCheckCmd checks the latest GitHub release without downloading.
var updateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check for a newer release without downloading",
	Args:  cobra.NoArgs,
	RunE:  runUpdateCheck,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	cmd.Println("Checking for updates...")

	latest, err := fetchLatestRelease(gitHubRepo)
	if err != nil {
		cmd.Printf("  %s  Could not check for updates: %v\n", statusLabel("WARN"), err)
		cmd.Printf("         You can check manually at https://github.com/%s/releases\n", gitHubRepo)
		return nil
	}

	latestVersion := update.ParseVersionFromTag(latest.TagName)
	currentVersion := parseCurrentVersion()

	cmd.Printf("  Current: %s\n", currentVersion)
	cmd.Printf("  Latest:  %s\n", latestVersion)

	if !update.IsNewer(currentVersion, latestVersion) {
		cmd.Println()
		cmd.Printf("  %s  You're on the latest version.\n", statusLabel("OK"))
		return nil
	}

	assetName := update.AssetName(runtime.GOOS, runtime.GOARCH)
	downloadURL := latest.assetURL(assetName)
	if downloadURL == "" {
		cmd.Printf("  %s  No asset found for %s/%s\n", statusLabel("FAIL"), runtime.GOOS, runtime.GOARCH)
		cmd.Printf("         Available assets on the release page:\n")
		cmd.Printf("         https://github.com/%s/releases/tag/%s\n", gitHubRepo, latest.TagName)
		return nil
	}

	cmd.Println()
	cmd.Printf("  %s  Downloading %s...\n", Dim("==>"), assetName)

	tmpDir, err := os.MkdirTemp("", "yaah-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpPath := filepath.Join(tmpDir, assetName)
	if err := downloadFile(downloadURL, tmpPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	if err := applyUpdate(tmpPath); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}

	cmd.Println()
	cmd.Printf("  %s  Updated to %s\n", statusLabel("OK"), latestVersion)
	cmd.Println()
	cmd.Printf("  Restart yaah to use the new version.\n")

	return nil
}

func runUpdateCheck(cmd *cobra.Command, args []string) error {
	cmd.Println("Checking for updates...")

	latest, err := fetchLatestRelease(gitHubRepo)
	if err != nil {
		cmd.Printf("  %s  Could not check for updates: %v\n", statusLabel("WARN"), err)
		cmd.Printf("         You can check manually at https://github.com/%s/releases\n", gitHubRepo)
		return nil
	}

	latestVersion := update.ParseVersionFromTag(latest.TagName)
	currentVersion := parseCurrentVersion()

	cmd.Printf("  Current: %s\n", currentVersion)
	cmd.Printf("  Latest:  %s\n", latestVersion)

	if update.IsNewer(currentVersion, latestVersion) {
		cmd.Println()
		cmd.Printf("  %s  A newer version is available!\n", statusLabel("WARN"))
		cmd.Printf("         Asset for this machine: %s\n",
			update.AssetName(runtime.GOOS, runtime.GOARCH))
		cmd.Printf("         Download: https://github.com/%s/releases/tag/%s\n",
			gitHubRepo, latest.TagName)
		cmd.Printf("         Run 'yaah update' to apply it.\n")
	} else {
		cmd.Println()
		cmd.Printf("  %s  You're on the latest version.\n", statusLabel("OK"))
	}

	return nil
}

// githubRelease represents the relevant fields from the GitHub API response.
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	PublishedAt time.Time     `json:"published_at"`
	HTMLURL     string        `json:"html_url"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// assetURL returns the download URL for the named asset, or empty string.
func (r *githubRelease) assetURL(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// fetchLatestRelease queries the GitHub API for the latest release.
func fetchLatestRelease(repo string) (*githubRelease, error) {
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

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("cannot parse response: %w", err)
	}

	return &release, nil
}

// downloadFile downloads a URL to a local file path.
func downloadFile(url, path string) error {
	resp, err := http.Get(url)
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

// applyUpdate replaces the current running binary with the new one.
//
// On Unix, the running binary is renamed to .old and the new binary is
// written in its place. The OS preserves the running process's inode
// reference after the rename, so the replace is safe.
//
// On Windows, the running binary cannot be reliably replaced in-process.
// Instead, the new binary is staged alongside as yaah.exe.next and a
// detached PowerShell script is launched to swap it after this process
// exits. The stale .next is cleaned up on the next startup.
func applyUpdate(newPath string) error {
	currentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate current binary: %w", err)
	}

	binDir := filepath.Dir(currentPath)
	binName := update.BinaryName(runtime.GOOS)
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
		return applyUpdateWindows(newPath, targetPath)
	}
	return applyUpdateUnix(newPath, targetPath, binDir)
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

func applyUpdateUnix(newPath, targetPath, binDir string) error {
	oldPath := filepath.Join(binDir, update.OldName(runtime.GOOS))

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

func applyUpdateWindows(newPath, targetPath string) error {
	nextPath := targetPath + ".next"

	if err := copyFileEx(newPath, nextPath); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	scriptPath := targetPath + ".update.ps1"
	pid := os.Getpid()

	script := fmt.Sprintf(
		`$id = %d
$timeout = 60
while ($timeout -gt 0 -and (Get-Process -Id $id -ErrorAction SilentlyContinue)) {
	Start-Sleep -Milliseconds 250
	$timeout--
}
Start-Sleep -Milliseconds 500
Move-Item -Force -LiteralPath "%s" -Destination "%s"
Start-Sleep -Milliseconds 500
Remove-Item -LiteralPath "%s" -Force -ErrorAction SilentlyContinue
`,
		pid,
		escapePSPath(nextPath),
		escapePSPath(targetPath),
		escapePSPath(scriptPath),
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

func escapePSPath(p string) string {
	return strings.ReplaceAll(p, "'", "''")
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

// CleanOldBinary removes leftover update artifacts (.old, .next, .update.ps1)
// if they exist. Called at startup.
func CleanOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	os.Remove(filepath.Join(dir, update.OldName(runtime.GOOS)))
	os.Remove(filepath.Join(dir, update.BinaryName(runtime.GOOS)+".next"))
	os.Remove(filepath.Join(dir, update.BinaryName(runtime.GOOS)+".update.ps1"))
}

// parseCurrentVersion extracts the clean semver from the build-time version string.
func parseCurrentVersion() string {
	v := version
	// Strip "v" prefix if present (from git tag in debug.ReadBuildInfo).
	v = update.ParseVersionFromTag(v)
	// Strip everything after a "+" or "-" suffix.
	for i, c := range v {
		if c == '+' || c == '-' {
			v = v[:i]
			break
		}
	}
	return v
}

func init() {
	updateCmd.AddCommand(updateCheckCmd)
	rootCmd.AddCommand(updateCmd)
}
