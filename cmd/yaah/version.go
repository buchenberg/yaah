package yaah

import (
	"github.com/spf13/cobra"
)

// versionCmd prints the yaah version, commit, and build date. Triggered
// by `yaah version` or `yaah --version` (the latter is wired by cobra
// automatically via rootCmd.Version).
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print yaah version, commit, and build date",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Println(rootCmd.Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
