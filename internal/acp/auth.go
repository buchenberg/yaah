package acp

// staticAuthMethods returns the compiled-in catalog of auth methods the
// initialize response advertises. Every entry is an agent-typed method
// (no "type" field — see AuthMethod): yaah reads the provider credential
// itself, either from an ${ENV_VAR} placeholder in ~/.yaah/config.yaml or
// from an OAuth token obtained via the web UI's login action, so there is
// no client-side
// credential handshake to delegate.
//
// The catalog MUST stay static: no config load, no environment lookup,
// no file reads, no provider resolution. The ACP registry runs its auth
// check on a cold machine — empty allowlisted environment, HOME at a
// fresh temp dir, no API keys — where anything config-derived serializes
// to an empty list and fails the check. Do not make this dynamic "so the
// methods match the configured providers": a non-empty static list is
// the contract being tested.
func staticAuthMethods() []AuthMethod {
	return []AuthMethod{
		{
			ID:   "openai-api-key",
			Name: "OpenAI API key",
			Description: "An OpenAI-compatible provider key, supplied through the environment and " +
				"referenced from ~/.yaah/config.yaml as api_key: ${OPENAI_API_KEY}. yaah substitutes " +
				"${VAR} placeholders when it loads the config; it does not read bare environment " +
				"variables by name. The default scaffold wires DEEPSEEK_API_KEY the same way.",
		},
		{
			ID:   "anthropic-api-key",
			Name: "Anthropic API key",
			Description: "A native Anthropic key referenced from ~/.yaah/config.yaml as " +
				"api_key: ${ANTHROPIC_API_KEY}. yaah is the sole credential resolver, so a bare " +
				"ANTHROPIC_AUTH_TOKEN in the environment is never picked up implicitly.",
		},
		{
			ID:   "providers-config",
			Name: "Provider config file",
			Description: "Credentials already configured in ~/.yaah/config.yaml (or " +
				"$YAAH_HOME/config.yaml) under providers.<name>.api_key, or an OAuth token stored " +
				"via the web UI login action. Nothing else is needed on the client side.",
		},
	}
}

// validAuthMethod reports whether id names one of the advertised methods.
// authenticate validates the name only: it deliberately does not check
// that a credential is present and makes no network call, so a client may
// declare its method before provisioning a key.
func validAuthMethod(id string) bool {
	for _, m := range staticAuthMethods() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// authMethodsFor returns the static agent methods plus the terminal method
// when the client can launch an interactive login. Terminal methods are
// advertised only when clientCapabilities.auth.terminal is true; they are
// never accepted by authenticate.
func authMethodsFor(caps *ClientCaps) []AuthMethod {
	methods := staticAuthMethods()
	if caps != nil && caps.Auth != nil && caps.Auth.Terminal {
		methods = append(methods, AuthMethod{
			ID:          "terminal-login",
			Name:        "Log in from the terminal",
			Description: "Launch `yaah login` interactively, then reconnect.",
			Type:        "terminal",
			Args:        []string{"login"},
		})
	}
	return methods
}
