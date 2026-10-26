package acp

import "context"

// ModelCatalog supplies the model choices a client may offer. It is
// injected into the Server rather than resolved inside this package:
// discovery needs config and provider credentials, both of which live
// outside the protocol package (cmd/yaah builds the catalog from the same
// machinery as the TUI and web model pickers). Keeping it an interface
// means internal/acp imports neither internal/config nor
// internal/providers, and a client that cannot discover models wires no
// catalog at all.
//
// The contract is best-effort by design: Options reports what it has and
// never reports an error. Model discovery is a convenience, so no caller
// may fail session/new because a provider endpoint was slow or down.
type ModelCatalog interface {
	// Options returns the models to offer. It returns nil when discovery
	// is disabled or nothing is known (no configured provider, feature
	// off). Implementations cache their result for the process lifetime;
	// callers own nothing and must not mutate the returned slice.
	Options(ctx context.Context) []ModelOption
}
