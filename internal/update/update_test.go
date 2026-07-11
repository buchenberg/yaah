package update

import "testing"

func TestParseLatestVersion_extractsSemverFromTag(t *testing.T) {
	tests := []struct {
		tag  string
		want string
	}{
		{"v0.1.0", "0.1.0"},
		{"v1.2.3", "1.2.3"},
		{"0.1.0", "0.1.0"},
		{"v0.0.1", "0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got := ParseVersionFromTag(tt.tag)
			if got != tt.want {
				t.Errorf("ParseVersionFromTag(%q) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

func TestCompareVersions_detectsNewerRelease(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool // true = latest is newer
	}{
		{"same version", "0.0.1", "0.0.1", false},
		{"newer patch", "0.0.1", "0.0.2", true},
		{"newer minor", "0.1.0", "0.2.0", true},
		{"newer major", "1.0.0", "2.0.0", true},
		{"older release", "0.2.0", "0.1.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNewer(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestAssetNameForPlatform(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"darwin", "arm64", "yaah-darwin-arm64"},
		{"linux", "amd64", "yaah-linux-amd64"},
		{"windows", "amd64", "yaah-windows-amd64.exe"},
	}

	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			got := AssetName(tt.goos, tt.goarch)
			if got != tt.want {
				t.Errorf("AssetName(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
			}
		})
	}
}
