package yaah

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/buchenberg/yaah/internal/update"
	"github.com/spf13/cobra"
)

// GitHub repo for release checks. Can be overridden via -ldflags for forks.
var gitHubRepo = "buchenberg/yaah"

// updateCmd is the `yaah update` subcommand.
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for and apply yaah updates",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Default behavior: same as `yaah update check`
		return updateCheckCmd.RunE(cmd, args)
	},
}

// updateCheckCmd checks the latest GitHub release without downloading.
var updateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check for a newer release without downloading",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.Println("Checking for updates...")

		latest, err := fetchLatestRelease(gitHubRepo)
		if err != nil {
			cmd.Printf("  %s  Could not check for updates: %v\n", statusLabel("WARN"), err)
			cmd.Printf("         You can check manually at https://github.com/%s/releases\n", gitHubRepo)
			return nil // non-fatal
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
		} else {
			cmd.Println()
			cmd.Printf("  %s  You're on the latest version.\n", statusLabel("OK"))
		}

		return nil
	},
}

// githubRelease represents the relevant fields from the GitHub API response.
type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
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

// parseCurrentVersion extracts the clean semver from the build-time version string.
// The version var may be "0.0.0" or "0.0.1-0.20260711..." (from debug.ReadBuildInfo).
func parseCurrentVersion() string {
	v := version
	// Strip everything after a "+" or "-" suffix
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

// Suppress unused import warning when os is only used in conditional builds
var _ = os.Stdout
