package yaah

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/repl"
	"github.com/buchenberg/yaah/internal/worldinfo"
)

// printWorldBanner writes the session's workspace/dispatch-world summary
// under the provider line: the REPL's only surface for it. A zero status
// (never derived) prints nothing, so older flows are unaffected.
func printWorldBanner(s worldinfo.Status) {
	for _, line := range worldBannerLines(s) {
		fmt.Fprintf(os.Stderr, "  %s\n", Dim(line))
	}
	fmt.Fprintln(os.Stderr)
}

// worldBannerLines renders at most two dim lines. The dispatch ladder is the
// session-level truth from worldinfo: it mirrors what a dispatch gets, not
// what was configured.
func worldBannerLines(s worldinfo.Status) []string {
	if s.IsZero() {
		return nil
	}
	workspace := s.Backend
	if s.Isolated {
		workspace += " · isolated"
	}
	if s.Root == "" {
		workspace += " · unrestricted"
	} else {
		workspace += " · root " + s.Root
	}
	if s.Image != "" {
		workspace += " · image " + s.Image
	}
	line1 := "workspace: " + workspace + " · mode: " + s.Mode

	jail := "unavailable"
	if s.JailOK {
		jail = "available"
	}
	world := s.WorldReq + " → " + s.ProposalWorld
	if s.Mode == "shared" {
		world = "shared · no dispatch world"
	} else if s.ReportWorld != s.ProposalWorld {
		world = s.WorldReq + " → proposal " + s.ProposalWorld + " · report " + s.ReportWorld
	}
	line2 := "world: " + world +
		" · containment: " + s.Containment + " · jail: " + jail +
		" · network: " + s.Network
	if s.Degraded && s.Reason != "" {
		line2 += " (" + s.Reason + ")"
	}
	return []string{line1, line2}
}

// startREPL runs the interactive read-eval-print loop.
// Builds infrastructure (config, provider, tools, DB, MCP) once per session
// and reuses it across prompts.
func startREPL() error {
	fmt.Print(repl.Banner(version))

	// Build the agent session once for the entire REPL lifetime.
	sess, err := newAgentSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	fmt.Fprintf(os.Stderr, "\n  %s %s/%s\n", Dim("provider:"), sess.ProviderName(), sess.ModelName())
	printWorldBanner(sess.WorldStatus())

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		fmt.Print(repl.Prompt())

		if !scanner.Scan() {
			fmt.Println()
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		switch repl.ParseSlashCommand(input) {
		case repl.CmdExit:
			return nil
		case repl.CmdClear:
			fmt.Print("\x1b[2J\x1b[H")
			continue
		case repl.CmdHelp:
			printHelp()
			continue
		case repl.CmdCompact:
			sess.Compact()
			continue
		case repl.CmdReloadRoles:
			sess.reloadRoles()
			continue
		case repl.CmdLogin:
			replLogin(sess)
			continue
		case repl.CmdLogout:
			replLogout(sess)
			continue
		case repl.CmdPlan, repl.CmdBuild:
			mode := "plan"
			if repl.ParseSlashCommand(input) == repl.CmdBuild {
				mode = "build"
			}
			if err := sess.SetMode(mode); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", replYellow("error: "+err.Error()))
				continue
			}
			fmt.Printf("%s\n", repl.Dim("session mode: "+mode))
			continue
		}

		if err := repl.AppendHistory(input); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save history: %v\n", err)
		}

		tv := newTerminalView()
		tv.start()
		sess.SetView(tv)
		response, streamed, err := sess.RunPrompt(context.Background(), input)
		// tv.HandleEvent(DoneEvent) already handled spinner + trailing newlines
		if err != nil {
			if errors.Is(err, agent.MaxIterationsError{}) {
				fmt.Fprintf(os.Stderr, "%s\n", replYellow("max iterations reached."))
				fmt.Fprint(os.Stderr, replYellow("continue? [Y/n]: "))
				if scanner.Scan() {
					reply := strings.ToLower(strings.TrimSpace(scanner.Text()))
					if reply == "" || reply == "y" || reply == "yes" {
						response, streamed, err = sess.RunPrompt(context.Background(), "")
						if err != nil {
							fmt.Fprintf(os.Stderr, "%s\n", replYellow("error: "+truncateError(err.Error())))
							continue
						}
						if !streamed && response != "" {
							fmt.Println(response)
							fmt.Println()
						}
					}
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "%s\n", replYellow("error: "+truncateError(err.Error())))
		} else if !streamed && response != "" {
			fmt.Println(response)
			fmt.Println()
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	return nil
}

// replYellow is a quick color helper for the REPL (avoids import cycle).
func replYellow(s string) string {
	if os.Getenv("NO_COLOR") == "" {
		return "\x1b[33m" + s + "\x1b[0m"
	}
	return s
}

func replRed(s string) string {
	if os.Getenv("NO_COLOR") == "" {
		return "\x1b[31m" + s + "\x1b[0m"
	}
	return s
}

func truncateError(s string) string {
	if len(s) > 500 {
		return s[:497] + "..."
	}
	return s
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// printHelp displays the available slash commands.
func printHelp() {
	fmt.Printf("  %s  %s\n", repl.Bold("/exit"), repl.Dim("quit yaah"))
	fmt.Printf("  %s  %s\n", repl.Bold("/clear"), repl.Dim("clear the screen"))
	fmt.Printf("  %s  %s\n", repl.Bold("/compact"), repl.Dim("summarize old messages to free context"))
	fmt.Printf("  %s  %s\n", repl.Bold("/login"), repl.Dim("authenticate with an OAuth provider"))
	fmt.Printf("  %s  %s\n", repl.Bold("/logout"), repl.Dim("remove stored OAuth credentials"))
	fmt.Printf("  %s  %s\n", repl.Bold("/plan"), repl.Dim("read-only planning mode"))
	fmt.Printf("  %s  %s\n", repl.Bold("/build"), repl.Dim("full coding mode"))
	fmt.Printf("  %s  %s\n", repl.Bold("/?"), repl.Dim("show this help"))
	fmt.Println()
}

// replLogin handles the /login slash command in the REPL.
func replLogin(sess *agentSession) {
	names := oauthProviderNames(sess.cfg)
	if len(names) == 0 {
		fmt.Println("No OAuth providers configured. Add auth: oauth to a provider in ~/.yaah/config.yaml.")
		return
	}

	var name string
	if len(names) == 1 {
		name = names[0]
	} else {
		fmt.Println("OAuth providers:")
		picked, err := pickProviderNumber(names)
		if err != nil {
			fmt.Printf("%s\n", err)
			return
		}
		name = picked
	}

	if err := runInteractiveLogin(sess.cfg, name); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", replYellow("error: "+err.Error()))
	}
}

// replLogout handles the /logout slash command in the REPL.
func replLogout(sess *agentSession) {
	names := oauthProviderNames(sess.cfg)
	if len(names) == 0 {
		fmt.Println("No OAuth providers configured.")
		return
	}

	var name string
	if len(names) == 1 {
		name = names[0]
	} else {
		fmt.Println("OAuth providers:")
		picked, err := pickProviderNumber(names)
		if err != nil {
			fmt.Printf("%s\n", err)
			return
		}
		name = picked
	}

	if err := runInteractiveLogout(sess.cfg, name); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", replYellow("error: "+err.Error()))
	}
}
