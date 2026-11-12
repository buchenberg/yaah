// Package spec defines the typed v1 workflow document over dispatch and
// proposal settlement: strict YAML and JSON decoding with path-bearing
// errors, canonical JSON encoding, the stable spec hash, and the generated
// JSON Schema.
//
// The Go types in this package are the single source of truth for the
// document surface. Both parsers normalize into the same structs, so a
// YAML document and its JSON twin always produce byte-identical canonical
// JSON and the same SpecHash. The package intentionally contains no
// execution or validation logic; semantic rules (WF001+) live in the
// workflow validators built on top of these types.
package spec
