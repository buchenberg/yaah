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
)

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

	fmt.Fprintf(os.Stderr, "\n  %s %s/%s\n\n", Dim("provider:"), sess.ProviderName(), sess.ModelName())

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
			sess.compactContext()
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
