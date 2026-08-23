package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// pollingSafetyMargin is added to the server-specified polling interval
// to avoid hitting the server slightly too early due to clock skew.
const pollingSafetyMargin = 3 * time.Second

// OAuthConfig holds the parameters for an OAuth 2.0 Device Authorization
// Grant (RFC 8628) flow. All fields are sourced from provider config.
type OAuthConfig struct {
	// ClientID is the OAuth application client ID (public).
	ClientID string

	// Scope is the OAuth scope to request (e.g. "read:user").
	Scope string

	// Domain is the OAuth authorization server domain.
	// Device code endpoint: https://{Domain}/login/device/code
	// Token endpoint:       https://{Domain}/login/oauth/access_token
	Domain string
}

// DeviceCodeResponse is the response from the device code endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// TokenResponse is the response from the access token endpoint.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	Interval    int    `json:"interval"`
}

// OAuthToken is the persisted token stored on disk.
type OAuthToken struct {
	AccessToken     string    `json:"access_token"`
	Scope           string    `json:"scope,omitempty"`
	AuthenticatedAt time.Time `json:"authenticated_at"`
}

// DeviceFlowHooks carries the presentation callbacks for DeviceFlow.
// All are optional; nil hooks produce silent execution (useful in tests).
type DeviceFlowHooks struct {
	// Status reports progress and results as human-readable text.
	Status func(msg string)
}

// DeviceFlow runs the full device-code login for providerName:
// existing-token check → start → user-code presentation → polling →
// token persistence. The cmd layer resolves and validates the provider
// config and supplies the token store; everything with protocol or
// token-store side effects happens here so login logic is not
// duplicated across REPL/TUI/web surfaces.
func DeviceFlow(ctx context.Context, providerName string, cfg OAuthConfig, store OAuthTokenStore, hooks DeviceFlowHooks) error {
	status := hooks.Status
	if status == nil {
		status = func(string) {}
	}

	existing, err := store.Load(providerName)
	if err != nil {
		return fmt.Errorf("check existing token: %w", err)
	}
	if existing != nil {
		status(fmt.Sprintf("Already authenticated with %q (since %s). Log out first to re-authenticate.", providerName, existing.AuthenticatedAt.Format("2006-01-02 15:04")))
		return nil
	}

	status(fmt.Sprintf("Starting OAuth authentication with %q...", providerName))
	dcr, err := StartDeviceFlow(ctx, cfg)
	if err != nil {
		return fmt.Errorf("start device flow: %w", err)
	}

	status(fmt.Sprintf("Open %s and enter code: %s", dcr.VerificationURI, dcr.UserCode))

	tr, err := PollForToken(ctx, cfg, dcr)
	if err != nil {
		return fmt.Errorf("authorization failed: %w", err)
	}

	token := &OAuthToken{
		AccessToken:     tr.AccessToken,
		Scope:           tr.Scope,
		AuthenticatedAt: time.Now(),
	}
	if err := store.Save(providerName, token); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	status(fmt.Sprintf("✓ Authenticated with %q.", providerName))
	return nil
}

// StartDeviceFlow initiates the OAuth 2.0 Device Authorization Grant.
// It returns the device code response containing the verification URI
// and user code that the user must enter in their browser.
func StartDeviceFlow(ctx context.Context, cfg OAuthConfig) (*DeviceCodeResponse, error) {
	body, err := json.Marshal(map[string]string{
		"client_id": cfg.ClientID,
		"scope":     cfg.Scope,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal device code request: %w", err)
	}

	url := fmt.Sprintf("https://%s/login/device/code", cfg.Domain)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create device code request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yaah")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device code request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("device code endpoint returned %d: %s", resp.StatusCode, string(respBody))
	}

	var dcr DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dcr); err != nil {
		return nil, fmt.Errorf("decode device code response: %w", err)
	}

	return &dcr, nil
}

// PollForToken polls the access token endpoint until the user completes
// authorization or the context is cancelled. It handles authorization_pending
// and slow_down responses per RFC 8628.
func PollForToken(ctx context.Context, cfg OAuthConfig, dcr *DeviceCodeResponse) (*TokenResponse, error) {
	interval := time.Duration(dcr.Interval) * time.Second

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		body, err := json.Marshal(map[string]string{
			"client_id":   cfg.ClientID,
			"device_code": dcr.DeviceCode,
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		})
		if err != nil {
			return nil, fmt.Errorf("marshal token request: %w", err)
		}

		url := fmt.Sprintf("https://%s/login/oauth/access_token", cfg.Domain)
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create token request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "yaah")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("token request failed: %w", err)
		}

		var tr TokenResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&tr)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode token response: %w", decodeErr)
		}

		if tr.AccessToken != "" {
			return &tr, nil
		}

		switch tr.Error {
		case "authorization_pending":
			// User hasn't authorized yet — keep polling.
		case "slow_down":
			// RFC 8628 §3.5: increase interval by 5 seconds.
			interval += 5 * time.Second
			if tr.Interval > 0 {
				interval = time.Duration(tr.Interval) * time.Second
			}
		case "expired_token":
			return nil, fmt.Errorf("device code expired — run 'yaah login' again")
		case "access_denied":
			return nil, fmt.Errorf("authorization denied by user")
		default:
			if tr.Error != "" {
				return nil, fmt.Errorf("token error: %s", tr.Error)
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval + pollingSafetyMargin):
		}
	}
}

// --- Token persistence ---

// OAuthTokenStore persists OAuth tokens under Dir (typically the yaah
// config directory, injected by the composition root so this package
// does not reach into config for paths — finding C4).
type OAuthTokenStore struct {
	Dir string
}

// Path returns the stored-token path for a provider.
func (s OAuthTokenStore) Path(providerName string) string {
	return filepath.Join(s.Dir, fmt.Sprintf("oauth-%s.json", providerName))
}

// Save persists an OAuth token for the named provider.
func (s OAuthTokenStore) Save(providerName string, token *OAuthToken) error {
	path := s.Path(providerName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads the stored OAuth token for the named provider.
// Returns nil, nil if no token file exists.
func (s OAuthTokenStore) Load(providerName string) (*OAuthToken, error) {
	path := s.Path(providerName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read token file: %w", err)
	}
	var token OAuthToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("parse token file: %w", err)
	}
	if token.AccessToken == "" {
		return nil, nil
	}
	return &token, nil
}

// Delete removes the stored token for the named provider. Missing files
// are not an error.
func (s OAuthTokenStore) Delete(providerName string) error {
	err := os.Remove(s.Path(providerName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
