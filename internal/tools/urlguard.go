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
// webfetch or http tools. Covers loopback, "this network", RFC-1918
// private, link-local (cloud metadata), and IPv6 equivalents.
var blockedCIDRs = func() []*net.IPNet {
	cidrs := []string{
		// 0.0.0.0/8 is "this network" (RFC 1122 §3.2.1.3). Linux routes
		// every address in it over the loopback interface, so 0.0.0.0
		// and 0.0.0.1 are loopback doors that 127.0.0.0/8 alone does
		// not close (bead yaah-6mb).
		"0.0.0.0/8",
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		// ::/128 is the unspecified address — the 0.0.0.0 of IPv6.
		"::/128",
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
// range (loopback, "this network", private, link-local/cloud-metadata).
// Returns an error if the URL is unsafe. DNS failures fail CLOSED: an
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

// isBlockedIP reports whether ip is a destination the tools must not
// reach. A nil IP is blocked: every caller got here from a resolver, so
// "no address" means the picture is not clear enough to allow.
//
// Every spelling of the same destination is checked, not just the one the
// caller handed over (see ipForms). Matching only the literal form the
// guard was given is how an IPv4 blocklist gets bypassed by an IPv6
// spelling of the same address.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	for _, form := range ipForms(ip) {
		for _, n := range blockedCIDRs {
			if n.Contains(form) {
				return true
			}
		}
	}
	return false
}

// ipForms returns the representations of ip that name the same destination.
// net.IP.To4 converts the 4-byte and IPv4-mapped (::ffff:a.b.c.d) forms, so
// those are covered by adding it. The deprecated IPv4-compatible form
// (::a.b.c.d, RFC 4291 §2.5.5.1) is not converted by To4, yet it still
// embeds an IPv4 destination, so it is extracted here explicitly — that is
// what stops ::ffff:0.0.0.0 and ::0.0.0.0 from slipping past the 0.0.0.0/8
// block.
func ipForms(ip net.IP) []net.IP {
	forms := make([]net.IP, 1, 2)
	forms[0] = ip
	if v4 := ip.To4(); v4 != nil {
		if len(v4) != len(ip) {
			forms = append(forms, v4)
		}
		return forms
	}
	if len(ip) == net.IPv6len && isZeroBytes(ip[:12]) {
		forms = append(forms, net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4())
	}
	return forms
}

// isZeroBytes reports whether every byte of b is zero.
func isZeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
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
