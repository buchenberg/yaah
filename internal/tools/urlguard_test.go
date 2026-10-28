package tools

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// withGuardEnabled turns the SSRF guard on for the duration of a test.
// TestMain disables it package-wide so the httptest-on-loopback HTTP
// tool tests can run; guard behavior itself is tested here.
func withGuardEnabled(t *testing.T) {
	t.Helper()
	prev := ssrfGuardEnabled
	ssrfGuardEnabled = true
	t.Cleanup(func() { ssrfGuardEnabled = prev })
}

func TestValidateURL(t *testing.T) {
	withGuardEnabled(t)
	tests := []struct {
		name    string
		url     string
		wantErr string // empty = allowed; substring of expected error otherwise
	}{
		{"public literal allowed", "http://93.184.216.34/x", ""},
		{"loopback literal", "http://127.0.0.1/x", "blocked"},
		{"loopback with port", "http://127.0.0.1:8080/x", "blocked"},
		{"private 10.x", "http://10.0.0.5/x", "blocked"},
		{"private 192.168", "http://192.168.1.1/x", "blocked"},
		{"cloud metadata", "http://169.254.169.254/latest/meta-data", "blocked"},
		{"ipv6 loopback", "http://[::1]/x", "blocked"},
		{"localhost name", "http://localhost/x", "loopback"},
		{"subdomain localhost", "http://foo.localhost/x", "loopback"},
		{"file scheme", "file:///etc/passwd", "scheme"},
		{"ftp scheme", "ftp://example.com/x", "scheme"},
		// .invalid is reserved by RFC 2606 and must never resolve —
		// the guard must fail CLOSED on the lookup error, not let the
		// request through to the fetcher.
		{"unresolvable host fails closed", "http://yaah-test-host.invalid/x", "denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.url)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateURL(%q) = %v; want allowed", tt.url, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateURL(%q) allowed; want error containing %q", tt.url, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("validateURL(%q) error = %q; want substring %q", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestEgressGuard_blocksZeroNetworkAndMappedForms pins bead yaah-6mb: the
// 0.0.0.0/8 "this network" range reaches the loopback interface on Linux, so
// 0.0.0.0 and 0.0.0.1 are loopback doors that 127.0.0.0/8 alone does not
// close. The IPv6 spellings of the same destinations (unspecified, and the
// IPv4-mapped/compatible embeddings of a blocked IPv4 address) must not slip
// past either. Both legs of the defense are checked per case: the pre-flight
// validator and the dial-time guard, because a URL the first rejects must not
// be connectable by the second.
func TestEgressGuard_blocksZeroNetworkAndMappedForms(t *testing.T) {
	withGuardEnabled(t)
	tests := []struct{ name, url string }{
		{"zero network unspecified", "http://0.0.0.0/x"},
		{"zero network with port", "http://0.0.0.0:8080/x"},
		{"zero network second address", "http://0.0.0.1/x"},
		{"zero network upper bound", "http://0.255.255.255/x"},
		{"ipv6 unspecified", "http://[::]/x"},
		{"ipv6 loopback", "http://[::1]/x"},
		{"ipv4-mapped loopback", "http://[::ffff:127.0.0.1]/x"},
		{"ipv4-mapped zero network", "http://[::ffff:0.0.0.0]/x"},
		{"ipv4-compatible loopback", "http://[::127.0.0.1]/x"},
		{"ipv4-mapped cloud metadata", "http://[::ffff:169.254.169.254]/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.url)
			if err == nil {
				t.Fatalf("validateURL(%q) allowed; the destination is blocked for egress", tt.url)
			}
			if !strings.Contains(err.Error(), "blocked") {
				t.Errorf("validateURL(%q) error = %q; want a blocked-for-egress reason", tt.url, err)
			}

			u, perr := url.Parse(tt.url)
			if perr != nil {
				t.Fatalf("url.Parse(%q): %v", tt.url, perr)
			}
			addr := net.JoinHostPort(u.Hostname(), "80")
			_, derr := guardedDialContext(context.Background(), "tcp", addr)
			if derr == nil {
				t.Fatalf("dial to %s was allowed; the URL leg of the guard rejected it", addr)
			}
			if !strings.Contains(derr.Error(), "ssrf guard") {
				t.Errorf("dial to %s error = %q; want ssrf guard reason (a dial that fails for another reason does not prove the guard ran)", addr, derr)
			}
		})
	}
}

// TestGuardedClient_CheckRedirectBlocksLoopback pins the redirect leg
// of the SSRF defense: an allowed public host must not be able to
// bounce the client into a blocked range.
func TestGuardedClient_CheckRedirectBlocksLoopback(t *testing.T) {
	withGuardEnabled(t)
	client := newGuardedHTTPClient()
	if client.CheckRedirect == nil {
		t.Fatal("guarded client has no CheckRedirect policy")
	}

	target, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data", nil)
	origin, _ := http.NewRequest(http.MethodGet, "http://93.184.216.34/", nil)
	err := client.CheckRedirect(target, []*http.Request{origin})
	if err == nil {
		t.Fatal("redirect to cloud-metadata IP was allowed")
	}
	if !strings.Contains(err.Error(), "redirect blocked") {
		t.Errorf("error = %q; want redirect-blocked reason", err)
	}

	// Redirect to a public host stays allowed.
	target2, _ := http.NewRequest(http.MethodGet, "http://93.184.216.34/next", nil)
	if err := client.CheckRedirect(target2, []*http.Request{origin}); err != nil {
		t.Errorf("public redirect rejected: %v", err)
	}

	// Redirect budget enforced.
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = origin
	}
	if err := client.CheckRedirect(target2, via); err == nil {
		t.Error("redirect budget not enforced")
	}
}

// TestGuardedDialContext_blocksLiteralIPs pins the dial-time leg of the
// SSRF defense: connecting straight to a blocked literal is refused.
func TestGuardedDialContext_blocksLiteralIPs(t *testing.T) {
	withGuardEnabled(t)
	_, err := guardedDialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("dial to loopback literal was allowed")
	}
	if !strings.Contains(err.Error(), "ssrf guard") {
		t.Errorf("error = %q; want ssrf guard reason", err)
	}

	_, err = guardedDialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil {
		t.Fatal("dial to link-local literal was allowed")
	}
}
