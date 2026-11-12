package config

import "testing"

func TestIsRealKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"sk-abc123", true},
		{"", false},
		{"(not set)", false},
		{"(too short)", false},
		{"${DASHSCOPE_API_KEY}", false},
		{"prefix-${VAR}-suffix", false},
		{"ollama", true}, // scaffold placeholder for a keyless local server
	}
	for _, tc := range cases {
		if got := IsRealKey(tc.key); got != tc.want {
			t.Errorf("IsRealKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestIsKeylessEndpoint(t *testing.T) {
	cases := []struct {
		url    string
		want   bool
		reason string
	}{
		{"http://localhost:11434/v1", true, "ollama scaffold"},
		{"http://127.0.0.1:8080/v1", true, "loopback IPv4"},
		{"http://127.1.2.3:9", true, "loopback range"},
		{"http://[::1]:11434/v1", true, "loopback IPv6"},
		{"http://model.localhost:8000/v1", true, "localhost subdomain"},
		{"http://host.docker.internal:11434/v1", true, "container lane to host"},
		{"", false, "no base URL — official endpoint, key required"},
		{"https://api.deepseek.com/v1", false, "remote cloud provider"},
		{"http://api.ollama.example.com:11434", false, "localhost word inside a remote host"},
		{"not a url %", false, "unparseable fails closed"},
	}
	for _, tc := range cases {
		if got := IsKeylessEndpoint(tc.url); got != tc.want {
			t.Errorf("IsKeylessEndpoint(%q) = %v, want %v (%s)", tc.url, got, tc.want, tc.reason)
		}
	}
}

// TestHasCredential pins the shared makeProvider/doctor/providerUsable gate:
// a remote base URL never excuses a missing credential — only a keyless
// local endpoint does.
func TestHasCredential(t *testing.T) {
	// Pin the referenced var empty so the table is hermetic: on a machine
	// where DASHSCOPE_API_KEY really is set, the substitution succeeds and
	// the key is real — the PR #1573 failure only reproduces without it.
	t.Setenv("DASHSCOPE_API_KEY", "")
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"real key, remote base", Provider{APIKey: "sk-1", BaseURL: "https://api.deepseek.com/v1"}, true},
		{"real key, no base URL", Provider{APIKey: "sk-1"}, true},
		{"empty key, keyless local", Provider{BaseURL: "http://localhost:11434/v1"}, true},
		{"scaffold placeholder key, local", Provider{APIKey: "ollama", BaseURL: "http://localhost:11434/v1"}, true},
		{"empty key, remote base — the PR #1573 401", Provider{BaseURL: "https://api.deepseek.com/v1"}, false},
		{"unset env ref, remote base", Provider{APIKey: "${DASHSCOPE_API_KEY}", BaseURL: "https://api.deepseek.com/v1"}, false},
		{"no key, no base URL", Provider{}, false},
	}
	for _, tc := range cases {
		if got := tc.p.HasCredential(); got != tc.want {
			t.Errorf("%s: HasCredential() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The unset ${VAR} must collapse to a missing credential exactly as an empty
// literal key does — substitution is part of the predicate, not the caller's
// responsibility.
func TestHasCredential_ResolvesEnvRefs(t *testing.T) {
	t.Setenv("YAAH_TEST_MISSING_KEY_VAR", "")
	p := Provider{APIKey: "${YAAH_TEST_MISSING_KEY_VAR}", BaseURL: "https://api.example.com/v1"}
	if p.HasCredential() {
		t.Error("an unset ${VAR} reference must not count as a credential")
	}
}
