package tools

import (
	"slices"
	"strings"
)

// Belt is the set of tools a session may call.
//
// The belt is a construction constraint, not a cleanup pass: the registry
// consults it on every registration, so a tool outside the belt is never
// installed in the first place. There is no window in which it exists and
// is removed later, and a late registration (the runtime-wired tools
// register after the leaf tools) is refused like any other.
//
// Every tool the model does not use still ships its schema on every
// request, so a belt that names only the tools a lane needs is a prompt
// saving on every call (yaah-w8x).
type Belt struct {
	allowAll bool
	allowed  map[string]bool
	refused  []string
	known    map[string]bool
}

// NewBelt builds a belt from the configured allowlist. An empty list
// allows every tool — the default, and the belt of a session that did not
// configure one.
func NewBelt(names []string) *Belt {
	if len(names) == 0 {
		return &Belt{allowAll: true}
	}
	b := &Belt{allowed: make(map[string]bool, len(names)), known: make(map[string]bool)}
	for _, name := range names {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		b.allowed[n] = true
	}
	if len(b.allowed) == 0 {
		b.allowAll = true
	}
	return b
}

// Allows reports whether the belt admits a tool name.
func (b *Belt) Allows(name string) bool {
	if b == nil || b.allowAll {
		return true
	}
	return b.allowed[name]
}

// Know records a tool name the session knows about, whether or not it was
// installed: a tool that exists but was left out for another reason (a
// disabled lane, an absent database, an isolated workspace) is not a typo
// in the allowlist. The registry calls it for every tool it accepts.
func (b *Belt) Know(names ...string) {
	if b == nil || b.allowAll {
		return
	}
	if b.known == nil {
		b.known = make(map[string]bool)
	}
	for _, name := range names {
		b.known[name] = true
	}
}

// Refuse records a tool the belt turned away. The registry calls it when
// a registration is refused; the session prompt and record read the list
// back so the model is told what is missing and the transcript can say
// "the model couldn't" apart from "the model didn't".
func (b *Belt) Refuse(name string) {
	if b == nil || b.allowAll {
		return
	}
	if !slices.Contains(b.refused, name) {
		b.refused = append(b.refused, name)
	}
	// A refused tool still exists — it is known, so an allowlist that
	// names it is not a typo.
	b.Know(name)
}

// Refused returns the tool names the belt turned away, sorted.
func (b *Belt) Refused() []string {
	if b == nil {
		return nil
	}
	out := slices.Clone(b.refused)
	slices.Sort(out)
	return out
}

// Unknown returns the allowlist names that match no tool the session
// knows about — a typo, which would silently leave the session without a
// tool it asked for. A tool that exists but was not installed for another
// reason (the retained lane is off, the database is absent, the workspace
// is isolated) is known, so it never reads as a typo.
func (b *Belt) Unknown() []string {
	if b == nil || b.allowAll {
		return nil
	}
	var unknown []string
	for name := range b.allowed {
		if !b.known[name] {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	return unknown
}

// Allowed returns the belt's allowlist, sorted.
func (b *Belt) Allowed() []string {
	if b == nil || b.allowAll {
		return nil
	}
	out := make([]string, 0, len(b.allowed))
	for name := range b.allowed {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
