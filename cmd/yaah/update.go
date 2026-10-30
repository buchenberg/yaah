package yaah

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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

	// A previous attempt may have staged a binary that never landed —
	// typically a Windows swap blocked by another running yaah process.
	pending, hasPending := update.Pending()
	if hasPending {
		reportPending(cmd, pending)
	}

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
		if hasPending {
			// The running binary is at least as new as the staged
			// release, so the staged file can never be applied — clear
			// it instead of warning forever.
			update.DiscardStaged()
			cmd.Println()
			cmd.Printf("  %s  Cleared a stale staged update — this binary is already newer.\n", doctor.StatusLabel("OK"))
		}
		return nil
	}

	assetName := update.AssetName(runtime.GOOS, runtime.GOARCH)
	downloadURL := latest.AssetURL(assetName)
	if downloadURL == "" {
		cmd.Printf("  %s  Release %s publishes no %s asset\n", doctor.StatusLabel("FAIL"), latest.TagName, assetName)
		cmd.Printf("         The build may still be running; check the release page:\n")
		cmd.Printf("         https://github.com/%s/releases/tag/%s\n", gitHubRepo, latest.TagName)
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "yaah-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Integrity gate (review finding S5): apply only when the release
	// publishes checksums.txt and the digest matches. Releases without
	// checksums are refused unless the operator explicitly opts out —
	// self-updates must fail closed. The checksums are fetched first
	// because they also validate a previously staged binary.
	var checksums []byte
	if checksumsURL := latest.AssetURL("checksums.txt"); checksumsURL != "" {
		checksumsPath := filepath.Join(tmpDir, "checksums.txt")
		if err := update.Download(cmd.Context(), checksumsURL, checksumsPath); err != nil {
			return fmt.Errorf("download checksums: %w", err)
		}
		checksums, err = os.ReadFile(checksumsPath)
		if err != nil {
			return fmt.Errorf("read checksums: %w", err)
		}
	} else if os.Getenv("YAAH_UPDATE_SKIP_VERIFY") != "1" {
		return fmt.Errorf("release %s publishes no checksums.txt; refusing to self-update without integrity verification (set YAAH_UPDATE_SKIP_VERIFY=1 to override)", latest.TagName)
	}

	tmpPath := filepath.Join(tmpDir, assetName)
	reused := false
	if hasPending && checksums != nil {
		if err := update.VerifyChecksumAs(pending.Path, assetName, checksums); err == nil {
			tmpPath = pending.Path
			reused = true
			cmd.Println()
			cmd.Printf("  %s  Reusing staged %s (checksum verified)\n", Dim("==>"), assetName)
		}
	}

	if !reused {
		cmd.Println()
		cmd.Printf("  %s  Downloading %s...\n", Dim("==>"), assetName)
		if err := update.Download(cmd.Context(), downloadURL, tmpPath); err != nil {
			return fmt.Errorf("download: %w", err)
		}
		if checksums != nil {
			if err := update.VerifyChecksum(tmpPath, checksums); err != nil {
				return fmt.Errorf("integrity check failed: %w", err)
			}
			cmd.Printf("  %s  SHA-256 verified\n", doctor.StatusLabel("OK"))
		} else {
			cmd.Printf("  %s  Release has no checksums.txt — applying UNVERIFIED (YAAH_UPDATE_SKIP_VERIFY=1)\n", doctor.StatusLabel("WARN"))
		}
	}

	deferred, err := update.Apply(tmpPath)
	if err != nil {
		return fmt.Errorf("apply update: %w", err)
	}

	cmd.Println()
	if deferred {
		// On Windows the swap runs after this process exits; say so
		// instead of claiming the update already landed.
		cmd.Printf("  %s  Staged %s; it will be swapped in when this process exits.\n", doctor.StatusLabel("OK"), latestVersion)
		cmd.Printf("         If 'yaah --version' still reports %s, another yaah process is\n", currentVersion)
		cmd.Printf("         holding the binary: close them all and run 'yaah update' again.\n")
		cmd.Printf("         The staged download is reused when the release publishes\n")
		cmd.Printf("         checksums, so that retry needs no download.\n")
		return nil
	}

	cmd.Printf("  %s  Updated to %s\n", doctor.StatusLabel("OK"), latestVersion)
	cmd.Println()
	cmd.Printf("  Restart yaah to use the new version.\n")

	return nil
}

// reportPending explains a staged update that never reached the
// installed binary.
func reportPending(cmd *cobra.Command, p update.PendingState) {
	cmd.Printf("  %s  A previous update is staged but not applied\n", doctor.StatusLabel("WARN"))
	cmd.Printf("         %s (staged %s)\n", p.Path, p.StagedAt.Format("2006-01-02 15:04:05"))
	if p.Log != "" {
		for _, line := range strings.Split(p.Log, "\n") {
			cmd.Printf("         %s\n", doctor.DimText(line))
		}
	} else {
		cmd.Printf("         Close every running yaah process, then re-run 'yaah update'.\n")
	}
	cmd.Println()
}

func runUpdateCheck(cmd *cobra.Command, args []string) error {
	if pending, ok := update.Pending(); ok {
		reportPending(cmd, pending)
	}

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
