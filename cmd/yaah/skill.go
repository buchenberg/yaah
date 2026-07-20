package yaah

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// skillCreateCmd creates a new skill.
var skillCreateCmd = &cobra.Command{
	Use:   "create <name> <description>",
	Short: "Create a new skill",
	Long: `Create a new SKILL.md file with YAML frontmatter. The skill body is read
from stdin. The skill is created in the first available search path
(typically <project>/.agents/skills/).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, desc := args[0], args[1]
		dirs := skillSearchPaths()
		if len(dirs) == 0 {
			return fmt.Errorf("no skill search paths configured")
		}
		// Read body from stdin
		body := readStdin(cmd)
		if body == "" {
			return fmt.Errorf("skill body is required — pipe or redirect markdown content to stdin")
		}
		path, err := skills.Create(dirs[0], name, desc, body)
		if err != nil {
			return err
		}
		cmd.Printf("Created %s at %s\n", Bold(name), Dim(path))
		return nil
	},
}

// skillEditCmd updates an existing skill.
var skillEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit an existing skill",
	Long: `Update a skill's description and/or body. The new body is read from stdin
(if not provided, the existing body is preserved). Use --description to
update the description.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		dirs := skillSearchPaths()
		s := skills.FindSkill(dirs, name)
		if s == nil {
			return fmt.Errorf("skill %q not found", name)
		}
		desc, _ := cmd.Flags().GetString("description")
		body := readStdin(cmd)
		path, err := skills.Edit(s, desc, body)
		if err != nil {
			return err
		}
		cmd.Printf("Updated %s at %s\n", Bold(name), Dim(path))
		return nil
	},
}

// readStdin reads all available input from stdin. Returns an empty string
// if stdin is not a pipe/redirect or is empty.
func readStdin(cmd *cobra.Command) string {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return ""
	}
	// Only read if stdin is a pipe or redirect (not a terminal)
	if (stat.Mode()&os.ModeCharDevice) != 0 {
		return ""
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(cmd.InOrStdin()); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
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
	skillEditCmd.Flags().String("description", "", "new description (optional)")
	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillShowCmd)
	skillCmd.AddCommand(skillCreateCmd)
	skillCmd.AddCommand(skillEditCmd)
	rootCmd.AddCommand(skillCmd)
}
