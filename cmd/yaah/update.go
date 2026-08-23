package yaah

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/buchenberg/yaah/internal/doctor"
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
	update.CleanOldBinaries()

	cmd.Println("Checking for updates...")

	latest, err := update.FetchLatestRelease(gitHubRepo)
	if err != nil {
		cmd.Printf("  %s  Could not check for updates: %v\n", doctor.StatusLabel("WARN"), err)
		cmd.Printf("         You can check manually at https://github.com/%s/releases\n", gitHubRepo)
		return nil
	}

	latestVersion := update.ParseVersionFromTag(latest.TagName)
	currentVersion := parseCurrentVersion()

	cmd.Printf("  Current: %s\n", currentVersion)
	cmd.Printf("  Latest:  %s\n", latestVersion)

	if !update.IsNewer(currentVersion, latestVersion) {
		cmd.Println()
		cmd.Printf("  %s  You're on the latest version.\n", doctor.StatusLabel("OK"))
		return nil
	}

	assetName := update.AssetName(runtime.GOOS, runtime.GOARCH)
	downloadURL := latest.AssetURL(assetName)
	if downloadURL == "" {
		cmd.Printf("  %s  No asset found for %s/%s\n", doctor.StatusLabel("FAIL"), runtime.GOOS, runtime.GOARCH)
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
	if err := update.Download(downloadURL, tmpPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	if err := update.Apply(tmpPath); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}

	cmd.Println()
	cmd.Printf("  %s  Updated to %s\n", doctor.StatusLabel("OK"), latestVersion)
	cmd.Println()
	cmd.Printf("  Restart yaah to use the new version.\n")

	return nil
}

func runUpdateCheck(cmd *cobra.Command, args []string) error {
	cmd.Println("Checking for updates...")

	latest, err := update.FetchLatestRelease(gitHubRepo)
	if err != nil {
		cmd.Printf("  %s  Could not check for updates: %v\n", doctor.StatusLabel("WARN"), err)
		cmd.Printf("         You can check manually at https://github.com/%s/releases\n", gitHubRepo)
		return nil
	}

	latestVersion := update.ParseVersionFromTag(latest.TagName)
	currentVersion := parseCurrentVersion()

	cmd.Printf("  Current: %s\n", currentVersion)
	cmd.Printf("  Latest:  %s\n", latestVersion)

	if update.IsNewer(currentVersion, latestVersion) {
		cmd.Println()
		cmd.Printf("  %s  A newer version is available!\n", doctor.StatusLabel("WARN"))
		cmd.Printf("         Asset for this machine: %s\n",
			update.AssetName(runtime.GOOS, runtime.GOARCH))
		cmd.Printf("         Download: https://github.com/%s/releases/tag/%s\n",
			gitHubRepo, latest.TagName)
		cmd.Printf("         Run 'yaah update' to apply it.\n")
	} else {
		cmd.Println()
		cmd.Printf("  %s  You're on the latest version.\n", doctor.StatusLabel("OK"))
	}

	return nil
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
