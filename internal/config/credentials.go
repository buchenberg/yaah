package config

import (
	"net/url"
	"strings"
)

// IsRealKey reports whether an API key looks like a real key: not empty, not
// one of the placeholder values, and not an unsubstituted ${VAR} reference
// (which means substitution never ran — or ran against an unset variable in
// a config path that keeps raw values).
func IsRealKey(key string) bool {
	if key == "" || key == "(not set)" || key == "(too short)" {
		return false
	}
	if strings.Contains(key, "${") {
		return false
	}
	return true
}

// IsKeylessEndpoint reports whether a base URL points at a local inference
// server that authenticates nothing (Ollama, llama.cpp, LM Studio, local
// vLLM). An empty key is a configuration error everywhere else: an empty or
// unresolved api_key against a remote base URL cannot authenticate, and the
// request fails 401 no matter how often it is replayed. An empty or
// unparseable URL is never keyless — providers without a base URL use their
// API's official endpoint, which requires the key.
func IsKeylessEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if host == "::1" || strings.HasPrefix(host, "127.") {
		return true
	}
	// A container lane reaching an inference server on the host (Ollama
	// bound to the host network): still local, still keyless.
	return host == "host.docker.internal"
}

// HasCredential reports whether the provider entry can authenticate — or
// needs no authentication: the resolved API key is real, or the resolved base
// URL points at a keyless local endpoint. Substitution runs first so an
// unset ${VAR} collapses to the empty key it will be on the wire.
//
// OAuth providers are excluded: their credential lives in the token store,
// not in config, so callers gate the OAuth lane (auth: oauth) before
// consulting this predicate.
func (p Provider) HasCredential() bool {
	r := Resolve(p)
	return IsRealKey(r.APIKey) || IsKeylessEndpoint(r.BaseURL)
}
