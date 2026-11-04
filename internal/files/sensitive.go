package files

import "strings"

// The sensitive names (docs/dev/design/file-browser.md, section 5), matched
// on one path segment, case-insensitively.
var (
	sensitiveNames = map[string]bool{
		".git": true, ".env": true, ".ssh": true, ".aws": true, ".gnupg": true,
		".netrc": true, ".npmrc": true, ".pypirc": true, ".docker": true, ".kube": true,
		"credentials": true, "credentials.json": true,
		"secrets.json": true, "secrets.yaml": true, "secrets.yml": true,
	}
	// envTemplates are the .env.* files meant to be shared.
	envTemplates    = map[string]bool{".env.example": true, ".env.sample": true, ".env.template": true}
	sensitivePrefix = []string{"id_rsa", "id_ed25519", "id_ecdsa"}
	sensitiveSuffix = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".p8"}
)

// Sensitive reports whether a file or folder name is hidden for safety:
// not listed, not searchable, not readable, and nothing under it either.
func Sensitive(name string) bool {
	n := strings.ToLower(name)
	if sensitiveNames[n] {
		return true
	}
	if strings.HasPrefix(n, ".env.") {
		return !envTemplates[n]
	}
	for _, p := range sensitivePrefix {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range sensitiveSuffix {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}
