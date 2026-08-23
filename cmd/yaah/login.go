package yaah

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login [provider]",
	Short: "Authenticate with a provider via OAuth device flow",
	Long: `Authenticate with a provider using the OAuth 2.0 device code flow.

The provider must be configured in ~/.yaah/config.yaml with auth: oauth
and the required oauth_client_id, oauth_scope, and oauth_domain fields.

Example config:
  providers:
    copilot:
      api: copilot
      auth: oauth
      oauth_client_id: "Ov23li8tweQw6odWQebz"
      oauth_scope: "read:user"
      oauth_domain: "github.com"

Then run:
  yaah login copilot

If multiple OAuth providers are configured, running 'yaah login' without
an argument will prompt you to choose one.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout [provider]",
	Short: "Remove stored OAuth credentials for a provider",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runLogout,
}

func oauthProviderNames(cfg *config.Config) []string {
	var names []string
	for name, p := range cfg.Providers {
		if p.Auth == "oauth" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func pickOAuthProvider(cfg *config.Config, arg string) (string, error) {
	if arg != "" {
		p, ok := cfg.Providers[arg]
		if !ok {
			return "", fmt.Errorf("provider %q not found in config — run 'yaah config edit'", arg)
		}
		if p.Auth != "oauth" {
			return "", fmt.Errorf("provider %q does not use OAuth (auth: %q) — set auth: oauth in config", arg, p.Auth)
		}
		return arg, nil
	}

	names := oauthProviderNames(cfg)
	switch len(names) {
	case 0:
		return "", fmt.Errorf("no OAuth providers configured — add auth: oauth to a provider in ~/.yaah/config.yaml")
	case 1:
		return names[0], nil
	default:
		return promptProviderChoice(names)
	}
}

// promptProviderChoice prints a numbered picker for multiple providers.
func promptProviderChoice(names []string) (string, error) {
	fmt.Println("Multiple OAuth providers configured. Choose one:")
	fmt.Println()
	return pickProviderNumber(names)
}

// pickProviderNumber renders a numbered list and reads a selection from
// stdin. Shared by the login/logout CLI commands and REPL slash
// commands — previously copy-pasted four times (finding B3-adjacent).
func pickProviderNumber(names []string) (string, error) {
	for i, name := range names {
		fmt.Printf("  %d) %s\n", i+1, name)
	}
	fmt.Println()
	fmt.Print("Enter number: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "", fmt.Errorf("no selection made")
	}
	input := strings.TrimSpace(scanner.Text())

	idx, err := strconv.Atoi(input)
	if err != nil || idx < 1 || idx > len(names) {
		return "", fmt.Errorf("invalid selection: %q", input)
	}
	return names[idx-1], nil
}

// loginOAuth validates the provider's OAuth config and delegates the
// device-code flow to providers.DeviceFlow. Presentation flows back via
// the status callback.
func loginOAuth(cfg *config.Config, providerName string, status func(string)) error {
	p, ok := cfg.Providers[providerName]
	if !ok {
		return fmt.Errorf("provider %q not found in config", providerName)
	}
	r := config.Resolve(p)
	if r.Auth != "oauth" {
		return fmt.Errorf("provider %q does not use OAuth", providerName)
	}
	if r.OAuthClientID == "" {
		return fmt.Errorf("provider %q missing oauth_client_id in config", providerName)
	}
	if r.OAuthDomain == "" {
		return fmt.Errorf("provider %q missing oauth_domain in config", providerName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	return providers.DeviceFlow(ctx, providerName, providers.OAuthConfig{
		ClientID: r.OAuthClientID,
		Scope:    r.OAuthScope,
		Domain:   r.OAuthDomain,
	}, providers.DeviceFlowHooks{Status: status})
}

func logoutOAuth(cfg *config.Config, providerName string, status func(string)) error {
	if _, ok := cfg.Providers[providerName]; !ok {
		return fmt.Errorf("provider %q not found in config", providerName)
	}
	if err := providers.DeleteOAuthToken(providerName); err != nil {
		return fmt.Errorf("logout failed: %w", err)
	}
	status(fmt.Sprintf("Logged out — stored token for %q removed.", providerName))
	return nil
}

func runLogin(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	providerName, err := pickOAuthProvider(cfg, arg)
	if err != nil {
		return err
	}

	return loginOAuth(cfg, providerName, func(msg string) { cmd.Println(msg) })
}

func runLogout(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	providerName, err := pickOAuthProvider(cfg, arg)
	if err != nil {
		return err
	}

	return logoutOAuth(cfg, providerName, func(msg string) { cmd.Println(msg) })
}

func init() {
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}

func runInteractiveLogin(cfg *config.Config, providerName string) error {
	return loginOAuth(cfg, providerName, func(msg string) { fmt.Println(msg) })
}

func runInteractiveLogout(cfg *config.Config, providerName string) error {
	return logoutOAuth(cfg, providerName, func(msg string) { fmt.Println(msg) })
}
