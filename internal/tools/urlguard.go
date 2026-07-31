package tools

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ssrfGuardEnabled controls whether the URL validation guard is active.
// Tests disable it to allow httptest servers on 127.0.0.1.
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

// validateURL checks that a URL does not target a blocked network range
// (loopback, private, link-local/cloud-metadata). Returns an error if
// the URL is unsafe.
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
		return nil
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
