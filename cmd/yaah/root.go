// Package yaah implements the cobra root command and subcommand tree.
package yaah

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
)

// Build-time variables. Override via -ldflags "-X github.com/buchenberg/yaah/cmd/yaah.version=..."
// See scripts/release.sh for the full release pipeline.
var (
	version = "0.0.0"
	commit  = "none"
	date    = "unknown"
)

// Execute is the entry point called by main.go. It runs the cobra root
// command and returns the first error encountered, if any.
func Execute() error {
	return rootCmd.Execute()
}

// init populates the build-time variables. When built from a tagged
// release, the values come from -ldflags. When built from `go install` or
// `go run`, they fall back to the module version reported by debug.ReadBuildInfo.
func init() {
	if version == "0.0.0" || version == "" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	if commit == "none" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" {
					commit = s.Value
					if len(commit) > 7 {
						commit = commit[:7]
					}
				}
				if s.Key == "vcs.time" {
					date = s.Value
				}
			}
		}
	}
	rootCmd.Version = fmt.Sprintf("%s (commit %s, built %s)", version, commit, date)
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {}))
	}
	rootCmd.PersistentFlags().StringVarP(&approvalOverride,
		"approval", "a", "",
		"override approval mode: allow, ask, or deny")
	rootCmd.PersistentFlags().StringVar(&resumeSessionID,
		"resume", "",
		"resume a previous session by ID")
	rootCmd.PersistentFlags().StringArrayVarP(&directiveOverrides,
		"directive", "d", nil,
		"session directive injected into all agent prompts (repeatable)")
	rootCmd.PersistentFlags().StringVar(&workspaceRoot,
		"workspace", "",
		"restrict file-accessing tools to this directory (default: unrestricted)")
	rootCmd.PersistentFlags().BoolVar(&allowHomeAccess,
		"allow-home", false,
		"allow file-accessing tools to expand ~ paths (only meaningful with --workspace)")
	rootCmd.PersistentFlags().BoolVar(&workspaceAsk,
		"workspace-ask", false,
		"prompt before denying out-of-workspace file access instead of hard-rejecting (only meaningful with --workspace)")
}

var approvalOverride string
var resumeSessionID string
var directiveOverrides []string
var workspaceRoot string
var allowHomeAccess bool
var workspaceAsk bool
