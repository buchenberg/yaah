// Package yaah implements the cobra root command and subcommand tree.
package yaah

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/buchenberg/yaah/internal/mcp"
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
// command and returns the first error encountered, if any. The argv is
// normalized in init (see normalizeResumeArgs), before any Execute
// caller parses it, so the legacy `--resume <id>` form survives pflag's
// value-optional flag on every subcommand.
func Execute() error {
	// pflag keeps each flag's Changed bit on the shared *Flag, so a second
	// Execute() in the same process (tests, embedding) would see flags from
	// the previous run still marked as set. The agent-flag guard reads
	// Changed, so clear it up front and keep the guard's verdict per-run.
	resetAgentFlagChanged(rootCmd)
	return rootCmd.Execute()
}

// init normalizes the argv before anything parses it, so every
// rootCmd.Execute() caller — not just Execute() — sees the legacy
// `--resume <id>` form spliced into `--resume=<id>`. Callers that set
// their own args explicitly (tests) still override this.
func init() {
	rootCmd.SetArgs(normalizeResumeArgs(os.Args[1:]))
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
	mcp.SetClientVersion(version)
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			slog.Debug("otel export error", "err", err)
		}))
		return rejectAgentFlagsOnNonAgentCommands(cmd)
	}
	rootCmd.PersistentFlags().StringVarP(&approvalOverride,
		"approval", "a", "",
		"override approval mode: allow, ask, or deny")
	rootCmd.PersistentFlags().StringVar(&resumeSessionID,
		"resume", "",
		"resume a previous session by ID (bare --resume lists recent sessions to pick from; prefer --session)")
	// pflag NoOptDefVal: a bare --resume becomes the picker sentinel
	// while --resume=<id> still names a concrete session.
	rootCmd.PersistentFlags().Lookup("resume").NoOptDefVal = resumePickerSentinel
	rootCmd.PersistentFlags().StringVar(&resumeSessionIDExplicit,
		"session", "",
		"resume a previous session by ID (value-carrying form: --session sess-123)")
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

// resumeSessionIDExplicit backs --session, the value-carrying spelling of
// "resume this specific session". --resume keeps its value-optional shape
// (bare = interactive picker) plus the legacy --resume=<id> form, so
// existing scripts keep working.
var resumeSessionIDExplicit string
var directiveOverrides []string
var workspaceRoot string
var allowHomeAccess bool
var workspaceAsk bool
