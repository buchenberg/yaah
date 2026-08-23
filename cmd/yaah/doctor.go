package yaah

import (
	"github.com/buchenberg/yaah/internal/doctor"
	"github.com/spf13/cobra"
)

// doctorCmd runs diagnostic checks on the yaah installation.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose config, environment, and system health",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		checks := doctor.RunChecks(doctor.Options{DirectiveOverrides: sessionOptionsFromFlags().DirectiveOverrides})
		for _, c := range checks {
			cmd.Printf("  [%s]  %s\n", doctor.StatusLabel(c.Status), c.Label)
			if c.Detail != "" {
				cmd.Printf("         %s\n", doctor.DimText(c.Detail))
			}
		}

		cmd.Println()
		if doctor.AllOK(checks) {
			cmd.Println(doctor.GreenText("All checks passed. yaah is ready."))
		} else {
			cmd.Println(doctor.YellowText("Some checks need attention."))
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
