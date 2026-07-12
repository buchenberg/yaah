package yaah

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/spf13/cobra"
)

// doctorCmd runs diagnostic checks on the yaah installation.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose config, environment, and system health",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		checks := runChecks()
		for _, c := range checks {
			cmd.Printf("  [%s]  %s\n", statusLabel(c.Status), c.Label)
			if c.Detail != "" {
				cmd.Printf("         %s\n", dimText(c.Detail))
			}
		}

		cmd.Println()
		if allOK(checks) {
			cmd.Println(greenText("All checks passed. yaah is ready."))
		} else {
			cmd.Println(yellowText("Some checks need attention."))
		}

		return nil
	},
}

// check represents a single diagnostic result.
type check struct {
	Label  string
	Detail string
	Status string // "OK", "WARN", "FAIL"
}

// runChecks executes all diagnostic checks and returns the results.
func runChecks() []check {
	return []check{
		checkConfig(),
		checkHomeWritable(),
		checkPlatform(),
		checkEditor(),
	}
}

func allOK(checks []check) bool {
	for _, c := range checks {
		if c.Status != "OK" {
			return false
		}
	}
	return true
}

func checkConfig() check {
	path, err := config.ConfigPath()
	if err != nil {
		return check{Label: "Config path", Status: "FAIL", Detail: err.Error()}
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return check{
			Label:  "Config file",
			Detail: fmt.Sprintf("not found at %s — will use built-in defaults", path),
			Status: "WARN",
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return check{Label: "Config file", Status: "FAIL", Detail: err.Error()}
	}

	detail := fmt.Sprintf("%s (model: %s, %d provider(s))",
		path, cfg.Default.Model, len(cfg.Providers))
	return check{Label: "Config file", Status: "OK", Detail: detail}
}

func checkHomeWritable() check {
	home := os.Getenv("YAAH_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return check{Label: "Home directory", Status: "FAIL", Detail: err.Error()}
		}
		home = filepath.Join(userHome, ".yaah")
	}

	if err := os.MkdirAll(home, 0o755); err != nil {
		return check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}

	testFile := filepath.Join(home, ".doctor-write-test")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		return check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}
	os.Remove(testFile)

	return check{Label: "Home directory writable", Status: "OK", Detail: home}
}

func checkPlatform() check {
	return check{
		Label:  "Platform",
		Status: "OK",
		Detail: fmt.Sprintf("%s/%s %s", runtime.GOOS, runtime.GOARCH, runtime.Version()),
	}
}

func checkEditor() check {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		return check{
			Label:  "Editor ($EDITOR)",
			Detail: "not set — 'yaah config edit' will default to vi",
			Status: "WARN",
		}
	}
	return check{Label: "Editor ($EDITOR)", Status: "OK", Detail: editor}
}

// --- color helpers (respect NO_COLOR) ---

var doctorUseColor = os.Getenv("NO_COLOR") == ""

func statusLabel(status string) string {
	if !doctorUseColor {
		return status
	}
	switch status {
	case "OK":
		return "\x1b[32m" + status + "\x1b[0m"
	case "WARN":
		return "\x1b[33m" + status + "\x1b[0m"
	case "FAIL":
		return "\x1b[31m" + status + "\x1b[0m"
	}
	return status
}

func greenText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[32m" + s + "\x1b[0m"
}

func yellowText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[33m" + s + "\x1b[0m"
}

func dimText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
