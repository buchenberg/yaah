// Package update handles checking for and applying yaah updates from
// GitHub Releases. In v0.1 only --check (read-only) is implemented;
// the full download + verify + atomic-replace flow is wired but the
// actual binary swap is a no-op until we have signed releases.
package update

import (
	"fmt"
	"strings"
)

// ParseVersionFromTag extracts the semver version from a git tag.
// "v0.1.0" → "0.1.0", "0.1.0" → "0.1.0".
func ParseVersionFromTag(tag string) string {
	return strings.TrimPrefix(tag, "v")
}

// IsNewer compares two semver strings (major.minor.patch) and returns
// true if latest is newer than current.
func IsNewer(current, latest string) bool {
	cur := parseSemver(current)
	lat := parseSemver(latest)

	if lat[0] != cur[0] {
		return lat[0] > cur[0]
	}
	if lat[1] != cur[1] {
		return lat[1] > cur[1]
	}
	return lat[2] > cur[2]
}

// parseSemver splits "1.2.3" into [1, 2, 3]. Non-numeric parts are treated as 0.
func parseSemver(v string) [3]int {
	var parts [3]int
	segments := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(segments); i++ {
		n := 0
		for _, c := range segments[i] {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		parts[i] = n
	}
	return parts
}

// AssetName returns the expected release asset filename for a platform.
func AssetName(goos, goarch string) string {
	name := fmt.Sprintf("yaah-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}
