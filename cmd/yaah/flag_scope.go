package yaah

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// agentFlagNames are the persistent flags that only mean something to a
// command that spawns an agent. They are registered on the root command
// because five commands need them, so pflag accepts them everywhere —
// including on commands that never read them. Accepting a flag and doing
// nothing with it is a lie about the interface: `yaah version --workspace
// /tmp` looks like it restricts something and does not.
//
// The guard below turns that silent acceptance into an error. Commands that
// do spawn an agent are listed by full path in agentCommandPaths.
var agentFlagNames = []string{
	"approval",
	"directive",
	"resume",
	"session",
	"workspace",
	"allow-home",
	"workspace-ask",
}

// agentCommandPaths lists, by full command path, every command that reads
// the persistent agent flags through sessionOptionsFromFlags. Anything not
// listed here rejects them.
var agentCommandPaths = map[string]bool{
	"yaah":           true, // bare REPL and one-shot prompts
	"yaah tui":       true,
	"yaah web":       true,
	"yaah serve":     true, // deprecated alias
	"yaah mcp serve": true,
	"yaah acp-serve": true, // deprecated alias
	"yaah acp serve": true,
}

// resolveListenAddr merges the --listen flag with its deprecated alias
// (--http on serve, --addr on web). Before this, the same concept had a
// different name on each server, so there was no shared vocabulary to
// document once.
//
// Supplying both is an error rather than a silent precedence rule: guessing
// which address the user meant is exactly the kind of silence this file's
// other guard exists to remove.
func resolveListenAddr(cmd *cobra.Command, primaryName, legacyName, primaryVal, legacyVal string) (string, error) {
	primarySet := cmd.Flags().Changed(primaryName)
	legacySet := cmd.Flags().Changed(legacyName)
	switch {
	case primarySet && legacySet:
		return "", fmt.Errorf("--%s and --%s are mutually exclusive: use --%s", primaryName, legacyName, primaryName)
	case legacySet:
		fmt.Fprintf(os.Stderr, "warning: --%s is deprecated, use --%s\n", legacyName, primaryName)
		return legacyVal, nil
	default:
		return primaryVal, nil
	}
}

// commandPath walks up from cmd to the root and returns the space-joined
// command names, e.g. "yaah mcp serve".
func commandPath(cmd *cobra.Command) string {
	var names []string
	for c := cmd; c != nil; c = c.Parent() {
		names = append([]string{c.Name()}, names...)
	}
	return strings.Join(names, " ")
}

// resetAgentFlagChanged clears the pflag Changed bit for every agent-only
// flag. It exists because Changed lives on the shared *Flag object: without
// this, a second Execute() in the same process inherits the first run's
// "set" markers and the guard rejects flags nobody passed.
func resetAgentFlagChanged(cmd *cobra.Command) {
	for _, name := range agentFlagNames {
		if f := cmd.PersistentFlags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

// rejectAgentFlagsOnNonAgentCommands returns an error when an agent-only
// flag was explicitly set on a command that does not spawn an agent.
func rejectAgentFlagsOnNonAgentCommands(cmd *cobra.Command) error {
	if agentCommandPaths[commandPath(cmd)] {
		return nil
	}
	var rejected []string
	for _, name := range agentFlagNames {
		// cmd.Flags() includes persistent flags inherited from parents
		// once cobra has merged them during parsing.
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			rejected = append(rejected, "--"+name)
		}
	}
	if len(rejected) == 0 {
		return nil
	}
	return fmt.Errorf("%s cannot use %s: agent-only flag (run 'yaah %s --help' for what it accepts)",
		commandPath(cmd), strings.Join(rejected, ", "), strings.TrimPrefix(commandPath(cmd), "yaah "))
}
