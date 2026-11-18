package spec

// ValidSubworkflowName reports whether name is a legal subworkflow name: the
// identifier a call node's `workflow` field carries, resolved against a named
// registry rather than a file path (the plan's open question 6, adopted by
// yaah-0u0v.23).
//
// The shape is deliberately strict — letters or digits first, then letters,
// digits, underscore, or hyphen — because the alternative the registry design
// exists to refuse is a path: "../secrets", "sub/../../etc", a separator or a
// leading dot is how a name tries to become a file somewhere its operator did
// not declare. A name that cannot leave this shape cannot travel, so the
// registry's containment check is defense in depth rather than the only door.
func ValidSubworkflowName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '_' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}
