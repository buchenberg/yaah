package tools

// Compatibility seam for the substrate extraction (bead yaah-5D85).
//
// The tool substrate — the policy and authority types the leaf tools are
// written against and that carry no tool implementation — now lives in
// internal/tools/substrate. The aliases and forwarders below keep the leaf
// tools reading unqualified names (and external callers reading tools.X)
// while the rest of the package is migrated. New code should import
// internal/tools/substrate directly; these names are transitional.

import (
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools/substrate"
)

// Belt is the set of tools a session may call. See substrate.Belt.
type Belt = substrate.Belt

// NewBelt builds a belt from the configured allowlist.
func NewBelt(names []string) *Belt { return substrate.NewBelt(names) }

// DispatchGrants is a dispatch world's declared write authority. See
// substrate.DispatchGrants.
type DispatchGrants = substrate.DispatchGrants

// ResolveGrants lowers the configured grants to absolute roots under worldRoot.
func ResolveGrants(grants []config.GrantConfig, worldRoot string) (DispatchGrants, error) {
	return substrate.ResolveGrants(grants, worldRoot)
}
