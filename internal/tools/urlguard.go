package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ssrfGuardEnabled controls whether URL validation is active. Tests
// disable it to allow httptest servers on 127.0.0.1; production never
// touches it.
var ssrfGuardEnabled = true

// blockedCIDRs are network ranges that must not be reachable via the
// webfetch or http tools. Covers loopback, RFC-1918 private, link-local
// (cloud metadata), and IPv6 equivalents.
var blockedCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"::1/128",
		"fe80::/10",
		"fc00::/7",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// maxRedirects mirrors net/http's default redirect budget.
const maxRedirects = 10

// validateURL checks that a URL does not target a blocked network
// range (loopback, private, link-local/cloud-metadata). Returns an
// error if the URL is unsafe. DNS failures fail CLOSED: an
// unresolvable host is denied rather than passed to the fetcher.
func validateURL(rawURL string) error {
	if !ssrfGuardEnabled {
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported scheme %q (only http/https allowed)", u.Scheme)
	}

	host := u.Hostname()

	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("access to %s is blocked (private/loopback range)", host)
		}
		return nil
	}

	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("access to %s is blocked (loopback)", host)
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		// Fail closed: a lookup failure must not silently allow the
		// request through to the fetcher's own resolver.
		return fmt.Errorf("access to %s denied: cannot resolve host", host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("access to %s is blocked (resolves to private/loopback range %s)", host, ip)
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	for _, n := range blockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// newGuardedHTTPClient builds the shared client for tool-issued
// requests. Two layers defeat SSRF beyond validateURL's pre-flight
// check:
//
//   - CheckRedirect re-validates every redirect target, so an allowed
//     public host cannot bounce the client to 169.254.169.254.
//   - The transport's DialContext validates the actual IPs it connects
//     to, so the name validated before the request and the address
//     opened at dial time cannot diverge (DNS-rebinding TOCTOU).
func newGuardedHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if err := validateURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
		Transport: &http.Transport{
			DialContext: guardedDialContext,
		},
	}
}

// guardedDialContext resolves the target host and dials only IPs that
// pass the blocked-range check. See newGuardedHTTPClient.
func guardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{}
	if !ssrfGuardEnabled {
		return dialer.DialContext(ctx, network, addr)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: malformed address %q", addr)
	}

	// Literal IPs are checked directly.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return nil, fmt.Errorf("ssrf guard: access to %s is blocked (private/loopback range)", host)
		}
		return dialer.DialContext(ctx, network, addr)
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: resolve %s: %w", host, err)
	}
	var lastErr error = fmt.Errorf("ssrf guard: %s resolves only to blocked ranges", host)
	dialed := false
	for _, ipAddr := range ips {
		if isBlockedIP(ipAddr.IP) {
			continue
		}
		// Dial by resolved IP. TLS SNI still comes from the request
		// URL host (net/http sets ServerName from the URL, not the
		// dialed address), so https keeps working.
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		dialed = true
	}
	if dialed {
		return nil, lastErr
	}
	return nil, lastErr
}
