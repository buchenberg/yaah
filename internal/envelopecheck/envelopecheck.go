// Package envelopecheck implements the result-envelope/doc parity gate
// (yaah-c3z3.4) shared by every package that emits model-facing envelopes.
//
// The schema-property gate covers tool arguments, but result envelopes have
// their own JSON fields, and a field can ship without ever appearing in the
// model-facing doc — which is how world/warning/notes went undocumented. This
// gate names every JSON field of every registered envelope in the doc the
// model actually receives.
//
// The gate used to be duplicated verbatim in internal/tools and
// internal/dispatch, so a fix to the harness had to be made twice. Packages
// now supply only their registry and exemptions.
//
// It is imported from _test.go files only, so it never links into the binary.
package envelopecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/prompts"
)

// Entry registers one model-facing result envelope and the prompt doc that
// must describe it.
type Entry struct {
	// Type is the envelope's struct type.
	Type reflect.Type
	// Doc is the prompt name passed to prompts.ToolDescription, i.e. the
	// model-facing doc file without its extension.
	Doc string
}

// RegistryCoversNamedEnvelopes fails when a production struct type in the
// calling package's directory is named *Envelope or *Summary but appears in
// neither entries nor exemptions.
//
// The registry is explicit rather than derived from reflection because a type
// can be platform-gated and therefore absent from reflection on another build.
// Such a type must be registered on a build where it exists, or exempted with
// the bead that removes the exemption — it is never silently skipped.
func RegistryCoversNamedEnvelopes(t *testing.T, entries []Entry, exemptions map[string]string) {
	t.Helper()

	registered := make(map[string]bool, len(entries))
	for _, entry := range entries {
		registered[entry.Type.Name()] = true
	}

	dirEntries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	seen := make(map[string]bool)
	fset := token.NewFileSet()
	for _, dirEntry := range dirEntries {
		name := dirEntry.Name()
		if dirEntry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || (!strings.HasSuffix(ts.Name.Name, "Envelope") && !strings.HasSuffix(ts.Name.Name, "Summary")) {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); !ok {
					continue
				}
				seen[ts.Name.Name] = true
				if bead, ok := exemptions[ts.Name.Name]; ok {
					t.Logf("%s: exempt — %s", ts.Name.Name, bead)
					continue
				}
				if !registered[ts.Name.Name] {
					t.Errorf("type %s is a production envelope but is not registered; add it and its model-facing doc, or record an exemption citing a bead", ts.Name.Name)
				}
			}
		}
	}
	for name, bead := range exemptions {
		if strings.TrimSpace(bead) == "" {
			t.Errorf("exemptions[%s] must cite the bead that removes the exemption", name)
		}
		if !seen[name] {
			t.Errorf("exemptions names %s, but no production *Envelope type exists", name)
		}
	}
}

// DocsNameEveryJSONField fails when a registered envelope emits a JSON field
// that its model-facing doc never names.
func DocsNameEveryJSONField(t *testing.T, entries []Entry) {
	t.Helper()

	for _, entry := range entries {
		text := prompts.ToolDescription(entry.Doc)
		for _, field := range JSONFields(entry.Type) {
			// Require the field in code formatting. A bare word in prose is
			// too weak: "an error" would satisfy an `error` field without
			// documenting it as a field.
			re, err := regexp.Compile("`" + regexp.QuoteMeta(field) + "`")
			if err != nil {
				t.Fatalf("compile pattern for %s.%s: %v", entry.Type, field, err)
			}
			if !re.MatchString(text) {
				t.Errorf("%s: %s.md never names result field `%s`; document it or stop emitting it", entry.Type, entry.Doc, field)
			}
		}
	}
}

// JSONFields returns the JSON names of all fields in a result envelope,
// including embedded snapshots. Anonymous fields are followed so an embedded
// snapshot's fields must be documented rather than hiding behind the
// embedding.
func JSONFields(typ reflect.Type) []string {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.Anonymous {
			if name == "-" {
				continue
			}
			if name != "" {
				// An explicit tag on an embedded field makes it a named
				// field; encoding/json does not flatten it.
				fields = append(fields, name)
				continue
			}
			if embedded := indirectStruct(field.Type); embedded != nil {
				fields = append(fields, JSONFields(embedded)...)
			} else {
				// An embedded non-struct is emitted under its type name.
				fields = append(fields, field.Name)
			}
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		fields = append(fields, name)
	}
	slices.Sort(fields)
	return slices.Compact(fields)
}

// indirectStruct follows pointers to an embedded struct type, returning nil
// for embedded non-structs and for named non-struct types.
func indirectStruct(typ reflect.Type) reflect.Type {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Struct {
		return typ
	}
	return nil
}
