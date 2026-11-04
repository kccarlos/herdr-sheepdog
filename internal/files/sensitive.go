package files

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The sensitive names (docs/dev/design/file-browser.md, section 5), matched
// on one path segment under fold: the way a case-folding filesystem (APFS)
// sees a name, so "id_r\u017Fa" (long s) is "id_rsa".
var (
	sensitiveNames = foldSet(
		".git", ".env", ".envrc", ".ssh", ".aws", ".gnupg", ".netrc", ".npmrc", ".pypirc",
		".docker", ".kube", ".git-credentials", ".pgpass",
		"credentials", "credentials.json", "secrets.json", "secrets.yaml", "secrets.yml",
	)
	// envTemplates are the .env.* files meant to be shared.
	envTemplates    = foldSet(".env.example", ".env.sample", ".env.template")
	envPrefix       = fold(".env.")
	sensitivePrefix = foldAll("id_rsa", "id_ed25519", "id_ecdsa", "id_dsa")
	sensitiveSuffix = foldAll(".pem", ".key", ".p12", ".pfx", ".keystore", ".p8", ".tfvars")
	// nodeModules is the folder a search skips.
	nodeModules = fold("node_modules")
)

// fold normalises s to NFC and folds each rune to the smallest rune of its
// Unicode case-folding orbit, so names that differ only in case, or in a
// spelling a filesystem treats as the same (U+017F, the Kelvin sign), are
// equal.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		low := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			low = min(low, f)
		}
		return low
	}, norm.NFC.String(s))
}

func foldAll(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fold(n)
	}
	return out
}

func foldSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range foldAll(names...) {
		out[n] = true
	}
	return out
}

// Sensitive reports whether a file or folder name is hidden for safety:
// not listed, not searchable, not readable, and nothing under it either.
func Sensitive(name string) bool {
	n := fold(name)
	if sensitiveNames[n] {
		return true
	}
	if strings.HasPrefix(n, envPrefix) {
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

// SensitivePath reports whether any segment of a slash-separated path is
// Sensitive.
func SensitivePath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if Sensitive(seg) {
			return true
		}
	}
	return false
}
