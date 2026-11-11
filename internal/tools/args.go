package tools

// Compatibility aliases: the strict-argument helpers live in
// internal/toolargs so packages that cannot import tools (internal/dispatch)
// share one implementation. New code should import internal/toolargs.
import "github.com/buchenberg/yaah/internal/toolargs"

type UnknownArgumentError = toolargs.UnknownArgumentError

var (
	DecodeStrictObject  = toolargs.DecodeStrictObject
	StrictToolArgs      = toolargs.StrictToolArgs
	SchemaPropertyNames = toolargs.SchemaPropertyNames
	CloseSchema         = toolargs.CloseSchema
)
