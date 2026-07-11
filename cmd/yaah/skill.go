package yaah

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/skills"
	"github.com/spf13/cobra"
)

// skillCmd is the `yaah skill` subcommand tree.
var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Discover and inspect skills",
}

// skillListCmd lists all discovered skills.
var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all discovered skills",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dirs := skillSearchPaths()
		found := skills.Discover(dirs)

		if len(found) == 0 {
			cmd.Println("No skills found.")
			cmd.Println()
			cmd.Println("Skill search paths:")
			for _, d := range dirs {
				cmd.Printf("  %s\n", d)
			}
			return nil
		}

		cmd.Printf("Found %d skill(s):\n\n", len(found))
		for _, s := range found {
			cmd.Printf("  %s  %s\n", Bold(s.Name), Dim(s.Description))
			cmd.Printf("        %s\n", s.Path)
		}
		return nil
	},
}

// skillShowCmd shows a skill's content.
var skillShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a skill's content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dirs := skillSearchPaths()
		s := skills.FindSkill(dirs, args[0])
		if s == nil {
			return fmt.Errorf("skill %q not found", args[0])
		}

		cmd.Println(skills.FormatSkillForAgent(s))
		return nil
	},
}

// skillSearchPaths returns the directories to scan for skills, in order.
func skillSearchPaths() []string {
	home := config.HomeDir()

	// 1. Project-level (walk up from cwd)
	cwd, _ := os.Getwd()
	var projectDirs []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		projectDirs = append(projectDirs, filepath.Join(dir, ".agents", "skills"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	// 2. Yaah-specific
	yaahDir := filepath.Join(home, ".yaah", "skills")

	// 3. User-level cross-tool
	userDir := filepath.Join(home, ".agents", "skills")

	// Order: project → yaah → user (first wins)
	var dirs []string
	dirs = append(dirs, projectDirs...)
	dirs = append(dirs, yaahDir)
	dirs = append(dirs, userDir)
	return dirs
}

func init() {
	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillShowCmd)
	rootCmd.AddCommand(skillCmd)
}
