package docscheck

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

// skipRepoDirs are transient, VCS, or dependency directories never committed.
var skipRepoDirs = map[string]bool{
	".git":               true,
	".claude":            true,
	".swiftpm":           true,
	"node_modules":       true,
	".build":             true,
	"build":              true,
	"DerivedData":        true,
	"Sheepdog.xcodeproj": true,
	"bin":                true,
	"dist":               true,
}

// skipRecordedRunRelPaths matches links_test.go: recorded benchmark and
// research run outputs are frozen generated artifacts, not source/docs/fixtures.
func skipRecordedRunRelPath(rel string) bool {
	return rel == "docs/dev/bench/results" ||
		rel == "docs/dev/research/live-resume-context-results"
}

// isHistoricalArchiveDoc reports whether rel is a dated historical report or
// audit note that documents past local paths (docs/dev/public-release-check.md
// P7). Secret, email, phone, and hostname checks STILL run on these files.
func isHistoricalArchiveDoc(rel string) bool {
	if rel == "docs/dev/public-release-check.md" ||
		rel == "docs/dev/handover-2026-10-04.md" ||
		rel == "docs/dev/bench/tier3-2026-10-03.md" {
		return true
	}
	return strings.HasPrefix(rel, "docs/dev/history/") ||
		strings.HasPrefix(rel, "docs/dev/research/") ||
		strings.HasPrefix(rel, "docs/dev/eval/")
}

// isSyntheticSecretFixture reports whether rel is an automated test or media
// fixture that intentionally constructs fake keys/tokens to test redaction,
// onboarding, or auth headers (docs/dev/public-release-check.md S2).
// Personal-data checks and real multi-line PEM block checks STILL run on them.
func isSyntheticSecretFixture(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") ||
		strings.HasSuffix(rel, ".test.mjs") ||
		strings.HasSuffix(rel, ".test.ts") ||
		strings.HasSuffix(rel, "Tests.swift") {
		return true
	}
	switch rel {
	case "testdata/eval/extract/2026-01-02.jsonl",
		"scripts/e2e/scenario.sh":
		return true
	}
	return strings.HasPrefix(rel, "scripts/readme-media/")
}

// forbiddenRepoFileReason flags secret-bearing file names (.env, APNs .p8,
// private keys, PKCS#12 bundles) if one ever lands in the worktree.
func forbiddenRepoFileReason(rel string) string {
	base := filepath.Base(rel)
	lower := strings.ToLower(base)
	if base == ".env" || (strings.HasPrefix(base, ".env.") &&
		!strings.HasSuffix(lower, ".example") &&
		!strings.HasSuffix(lower, ".sample") &&
		!strings.HasSuffix(lower, ".template")) {
		return "environment secret file must not be in repo"
	}
	switch filepath.Ext(lower) {
	case ".p8", ".p12", ".pfx", ".key", ".pem":
		return fmt.Sprintf("private key / certificate bundle (%s) must not be in repo", filepath.Ext(lower))
	}
	return ""
}

var (
	// shapeOnlyRedactor applies internal/redact's built-in shapes both to detect
	// credential shapes and to ensure any error output masks the offending value.
	shapeOnlyRedactor = redact.New()

	// Real multi-line PEM private key blocks (header followed by base64 body).
	pemBlockRe  = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----\r?\n[A-Za-z0-9+/=]{16,}`)
	pemHeaderRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

	// Unambiguous credential prefixes (internal/redact shapes + common cloud/VCS tokens).
	directSecretTokenRe = regexp.MustCompile(`(?:AIza[0-9A-Za-z_\-]{20,}|\bsk-[0-9A-Za-z_\-]{12,}|\bsk-[0-9A-Za-z_\-]*\*{3,}[0-9A-Za-z_\-]*|\bgh[pousr]_[0-9A-Za-z]{20,}\b|\bgithub_pat_[0-9A-Za-z_]{20,}\b|\bxox[baprs]-[0-9A-Za-z\-]{10,}\b|\bAKIA[0-9A-Z]{16}\b)`)

	// Literal secrets in Bearer headers, URL query params, or key/token/secret/password assignments.
	bearerLiteralRe = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=\-]{16,})`)
	queryLiteralRe  = regexp.MustCompile(`(?i)[?&](?:key|token)=([A-Za-z0-9._~+/=\-]{12,})`)
	assignLiteralRe = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])(?:api[_-]?key|(?:access|auth|refresh|id|bearer)[_-]?token|client[_-]?secret|password)["']?\s*[=:]\s*["']?([A-Za-z0-9._~+/=\-]{16,})`)
	allCapsEnvRe    = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)
	kebabIdentifier = regexp.MustCompile(`^[a-z]+(?:-[a-z]+)+$`)

	// Home directory paths on macOS, Linux, and Windows.
	unixHomePathRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])/(?:Users|home)/([A-Za-z0-9._-]+)`)
	winHomePathRe  = regexp.MustCompile(`\b[A-Za-z]:\\Users\\([A-Za-z0-9._-]+)`)

	// Private mDNS (<host>.local) and Tailscale (<host>.<tailnet>.ts.net) names.
	mdnsHostRe   = regexp.MustCompile(`\b([A-Za-z0-9][A-Za-z0-9-]*)\.local\b`)
	tsnetHostRe  = regexp.MustCompile(`\b([A-Za-z0-9-]+)\.([A-Za-z0-9-]+)\.ts\.net\b`)
	emailAddrRe  = regexp.MustCompile(`\b([A-Za-z0-9._%+-]+)@([A-Za-z0-9.-]+\.[A-Za-z]{2,})\b`)
	uuidShapeRe  = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	parenPhoneRe = regexp.MustCompile(`(?:\+1[-.\s]?)?\(([2-9]\d{2})\)[-\s.]?([2-9]\d{2})[-\s.](\d{4})\b`)
	intlPhoneRe  = regexp.MustCompile(`\+[1-9]\d{0,2}[-\s]([2-9]\d{2})[-\s]([2-9]\d{2})[-\s](\d{4})\b`)
	dashPhoneRe  = regexp.MustCompile(`(?:^|[\s"':=])([2-9]\d{2})-([2-9]\d{2})-(\d{4})\b`)

	bAIza       = []byte("AIza")
	bSkDash     = []byte("sk-")
	bGHP        = []byte("gh")
	bXox        = []byte("xox")
	bAKIA       = []byte("AKIA")
	bPrivKey    = []byte("PRIVATE KEY")
	bSlashUsers = []byte("/Users/")
	bSlashHome  = []byte("/home/")
	bWinUsers   = []byte(`\Users\`)
	bDotLocal   = []byte(".local")
	bDotTSNet   = []byte(".ts.net")

	secretFoldNeedles = [][]byte{
		[]byte("bearer"),
		[]byte("api_key"),
		[]byte("api-key"),
		[]byte("apikey"),
		[]byte("access_token"),
		[]byte("access-token"),
		[]byte("accesstoken"),
		[]byte("auth_token"),
		[]byte("auth-token"),
		[]byte("authtoken"),
		[]byte("refresh_token"),
		[]byte("refresh-token"),
		[]byte("refreshtoken"),
		[]byte("id_token"),
		[]byte("id-token"),
		[]byte("idtoken"),
		[]byte("client_secret"),
		[]byte("client-secret"),
		[]byte("clientsecret"),
		[]byte("password"),
		[]byte("?key="),
		[]byte("&key="),
		[]byte("?token="),
		[]byte("&token="),
	}
)

var allowedHomeUsers = map[string]bool{
	"you":       true,
	"yourname":  true,
	"your-name": true,
	"your-user": true,
	"your_user": true,
	"user":      true,
	"username":  true,
	"someone":   true,
	"me":        true,
	"a":         true,
	"u":         true,
	"x":         true,
	"alice":     true,
	"bob":       true,
	"alex":      true,
	"pat":       true,
	"sam":       true,
	"dev":       true,
	"developer": true,
	"runner":    true,
	"linuxbrew": true,
	"ubuntu":    true,
	"debian":    true,
	"ec2-user":  true,
	"root":      true,
	"shared":    true,
	"admin":     true,
	"test":      true,
	"tester":    true,
	"ci":        true,
	"builder":   true,
	"nobody":    true,
	"example":   true,
	"demo":      true,
}

var allowedMDNSHosts = map[string]bool{
	"sheepdog":    true,
	"mac":         true,
	"macbook":     true,
	"macbook-pro": true,
	"macbook-air": true,
	"my-mac":      true,
	"your-mac":    true,
	"dev-mac":     true,
	"office-mac":  true,
	"mini":        true,
	"mac-mini":    true,
	"studio":      true,
	"mac-studio":  true,
	"imac":        true,
	"laptop":      true,
	"desktop":     true,
	"host":        true,
	"myhost":      true,
	"localhost":   true,
	"computer":    true,
	"workstation": true,
	"server":      true,
	"box":         true,
	"node":        true,
	"raspberrypi": true,
}

var allowedTailnets = map[string]bool{
	"tailnet":      true,
	"my-tailnet":   true,
	"your-tailnet": true,
	"example":      true,
	"tail1234":     true,
	"tail12345":    true,
	"yak-bebop":    true,
}

var allowedExactEmails = map[string]bool{
	"kccarlos.opensource@gmail.com": true, // public security contact in SECURITY.md
}

var allowedEmailDomains = map[string]bool{
	"example.com":              true,
	"example.org":              true,
	"example.net":              true,
	"example.invalid":          true,
	"localhost.localdomain":    true,
	"users.noreply.github.com": true,
	"noreply.github.com":       true,
	"anthropic.com":            true,
	"openai.com":               true,
	"google.com":               true,
	"apple.com":                true,
	"iam.gserviceaccount.com":  true,
}

type privacyFinding struct {
	file   string
	line   int
	kind   string
	detail string
}

func (f privacyFinding) String() string {
	if f.line <= 0 {
		return fmt.Sprintf("%s: [%s] %s", f.file, f.kind, f.detail)
	}
	return fmt.Sprintf("%s:%d: [%s] %s", f.file, f.line, f.kind, f.detail)
}

func containsFoldBytes(b, needleLower []byte) bool {
	n := len(needleLower)
	if len(b) < n {
		return false
	}
	c0 := needleLower[0]
	var c0Upper byte
	hasUpper := c0 >= 'a' && c0 <= 'z'
	if hasUpper {
		c0Upper = c0 - ('a' - 'A')
	}
	rem := b
	for len(rem) >= n {
		i := bytes.IndexByte(rem, c0)
		if hasUpper {
			j := bytes.IndexByte(rem, c0Upper)
			if i < 0 || (j >= 0 && j < i) {
				i = j
			}
		}
		if i < 0 || i+n > len(rem) {
			return false
		}
		if bytes.EqualFold(rem[i:i+n], needleLower) {
			return true
		}
		rem = rem[i+1:]
	}
	return false
}

func hasSecretShapeCandidateBytes(b []byte) bool {
	if bytes.Contains(b, bAIza) ||
		bytes.Contains(b, bSkDash) ||
		bytes.Contains(b, bGHP) ||
		bytes.Contains(b, bXox) ||
		bytes.Contains(b, bAKIA) ||
		bytes.Contains(b, bPrivKey) {
		return true
	}
	for _, kw := range secretFoldNeedles {
		if containsFoldBytes(b, kw) {
			return true
		}
	}
	return false
}

func hasPhoneCandidateBytes(b []byte) bool {
	if i := bytes.IndexByte(b, '('); i >= 0 {
		for rem := b[i:]; len(rem) >= 12; {
			if rem[4] == ')' && rem[1] >= '2' && rem[1] <= '9' {
				return true
			}
			next := bytes.IndexByte(rem[1:], '(')
			if next < 0 {
				break
			}
			rem = rem[1+next:]
		}
	}
	if i := bytes.IndexByte(b, '+'); i >= 0 {
		for rem := b[i:]; len(rem) >= 12; {
			if rem[1] >= '1' && rem[1] <= '9' && (rem[2] == ' ' || rem[2] == '-' || (rem[2] >= '0' && rem[2] <= '9')) {
				if len(rem) < 6 || rem[3] != ':' {
					return true
				}
			}
			next := bytes.IndexByte(rem[1:], '+')
			if next < 0 {
				break
			}
			rem = rem[1+next:]
		}
	}
	for rem := b; len(rem) >= 12; {
		i := bytes.IndexByte(rem, '-')
		if i < 0 || i+8 >= len(rem) {
			break
		}
		if i >= 3 && rem[i+4] == '-' && rem[i-3] >= '2' && rem[i-3] <= '9' && rem[i+1] >= '2' && rem[i+1] <= '9' {
			return true
		}
		rem = rem[i+1:]
	}
	return false
}

func isQuoteByte(b byte) bool {
	return b == '"' || b == '\'' || b == '`'
}

func isLiteralSecretValue(val string) bool {
	if val == "" || strings.Contains(val, redact.Mask) {
		return false
	}
	// Env var names ("VERTEX_API_KEY") and DOM/CSS kebab-case IDs ("anthropic-result") are identifiers, not secrets.
	if allCapsEnvRe.MatchString(val) || kebabIdentifier.MatchString(val) {
		return false
	}
	// Go/JS member access (`cfg.Anthropic.APIKey`) contains '.', whereas JWTs start with `eyJ`.
	if strings.IndexByte(val, '.') >= 0 && !strings.HasPrefix(val, "eyJ") {
		return false
	}
	lower := strings.ToLower(val)
	for _, placeholder := range []string{
		"pick-something-long-and-random",
		"paste-your-key-here",
		"your-key",
		"your_key",
		"example",
		"placeholder",
	} {
		if strings.Contains(lower, placeholder) {
			return false
		}
	}
	return true
}

func hasSecretLineMatch(rel, line string) bool {
	if strings.Contains(line, redact.Mask) {
		return false
	}
	if pemHeaderRe.MatchString(line) && !strings.Contains(line, "${") {
		return true
	}
	for _, tok := range directSecretTokenRe.FindAllString(line, -1) {
		if rel == "internal/redact/redact.go" && tok == "sk-abcd****1234" {
			continue
		}
		if isLiteralSecretValue(tok) {
			return true
		}
	}
	// Check redact.New() assignment/header/query shapes when the captured value is a literal secret.
	if shapeOnlyRedactor.String(line) == line {
		return false
	}
	for _, re := range []*regexp.Regexp{bearerLiteralRe, queryLiteralRe, assignLiteralRe} {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			if isLiteralSecretValue(m[1]) {
				return true
			}
		}
	}
	return false
}

var maskExtraTokensRe = regexp.MustCompile(`(?:\bgh[pousr]_[0-9A-Za-z]{20,}\b|\bgithub_pat_[0-9A-Za-z_]{20,}\b|\bxox[baprs]-[0-9A-Za-z\-]{10,}\b|\bAKIA[0-9A-Z]{16}\b)`)

func safeRedactedPreview(line string) string {
	s := shapeOnlyRedactor.String(strings.TrimSpace(line))
	return maskExtraTokensRe.ReplaceAllString(s, redact.Mask)
}

func scanPrivacyLine(rel string, lineNo int, line string, checkSecretShapes bool) []privacyFinding {
	return scanPrivacyLineBytes(rel, lineNo, []byte(line), checkSecretShapes, true)
}

func scanPrivacyLineBytes(rel string, lineNo int, raw []byte, checkSecretShapes, checkHomePaths bool) []privacyFinding {
	var out []privacyFinding

	needSecret := checkSecretShapes && hasSecretShapeCandidateBytes(raw)
	needUnixHome := checkHomePaths && (bytes.Contains(raw, bSlashUsers) || bytes.Contains(raw, bSlashHome))
	needWinHome := checkHomePaths && bytes.Contains(raw, bWinUsers)
	needMDNS := bytes.Contains(raw, bDotLocal)
	needTSNet := bytes.Contains(raw, bDotTSNet)
	needEmail := bytes.IndexByte(raw, '@') >= 0
	needPhone := hasPhoneCandidateBytes(raw)

	if !needSecret && !needUnixHome && !needWinHome && !needMDNS && !needTSNet && !needEmail && !needPhone {
		return nil
	}

	line := string(raw)

	if needSecret {
		if strings.Contains(line, "PRIVATE KEY") && pemHeaderRe.MatchString(line) && !strings.Contains(line, "${") {
			out = append(out, privacyFinding{
				file:   rel,
				line:   lineNo,
				kind:   "secret-pem",
				detail: "PEM private key header found",
			})
		} else if hasSecretLineMatch(rel, line) {
			out = append(out, privacyFinding{
				file:   rel,
				line:   lineNo,
				kind:   "secret-shape",
				detail: safeRedactedPreview(line),
			})
		}
	}

	if needUnixHome {
		for _, m := range unixHomePathRe.FindAllStringSubmatch(line, -1) {
			user := m[1]
			if !allowedHomeUsers[strings.ToLower(user)] {
				out = append(out, privacyFinding{
					file:   rel,
					line:   lineNo,
					kind:   "home-path",
					detail: fmt.Sprintf("non-placeholder home directory user %q", user),
				})
			}
		}
	}
	if needWinHome {
		for _, m := range winHomePathRe.FindAllStringSubmatch(line, -1) {
			user := m[1]
			if !allowedHomeUsers[strings.ToLower(user)] {
				out = append(out, privacyFinding{
					file:   rel,
					line:   lineNo,
					kind:   "home-path",
					detail: fmt.Sprintf("non-placeholder Windows home directory user %q", user),
				})
			}
		}
	}

	if needMDNS {
		for _, loc := range mdnsHostRe.FindAllStringSubmatchIndex(line, -1) {
			start, end := loc[0], loc[1]
			if start > 0 && (line[start-1] == '.' || line[start-1] == '$') {
				continue
			}
			isURLScheme := start >= 3 && line[start-3:start] == "://"
			if start > 0 && line[start-1] == '/' && !isURLScheme {
				continue
			}
			host := line[loc[2]:loc[3]]
			isAtHost := start > 0 && line[start-1] == '@'
			hasPort := end < len(line) && line[end] == ':'
			isQuoted := start > 0 && end < len(line) && isQuoteByte(line[start-1]) && (isQuoteByte(line[end]) || line[end] == '/' || line[end] == ':')
			hasHyphen := strings.IndexByte(host, '-') >= 0
			if !isURLScheme && !isAtHost && !hasPort && !isQuoted && !hasHyphen {
				continue
			}
			lower := strings.ToLower(host)
			if !allowedMDNSHosts[lower] {
				out = append(out, privacyFinding{
					file:   rel,
					line:   lineNo,
					kind:   "private-host",
					detail: fmt.Sprintf("non-placeholder .local hostname %q", host+".local"),
				})
			}
		}
	}

	if needTSNet {
		for _, m := range tsnetHostRe.FindAllStringSubmatch(line, -1) {
			host, tailnet := m[1], m[2]
			if !allowedTailnets[strings.ToLower(tailnet)] {
				out = append(out, privacyFinding{
					file:   rel,
					line:   lineNo,
					kind:   "private-tailnet",
					detail: fmt.Sprintf("non-placeholder Tailscale hostname %q", host+"."+tailnet+".ts.net"),
				})
			}
		}
	}

	if needEmail {
		for _, loc := range emailAddrRe.FindAllStringSubmatchIndex(line, -1) {
			start := loc[0]
			if start > 0 && (line[start-1] == '@' || line[start-1] == '/') {
				continue
			}
			full := strings.ToLower(line[loc[0]:loc[1]])
			domain := strings.ToLower(line[loc[4]:loc[5]])
			// Skip Retina asset scale names like `launchlogo@2x.png` / `@3x.png`.
			if strings.HasPrefix(domain, "2x.") || strings.HasPrefix(domain, "3x.") {
				continue
			}
			if allowedExactEmails[full] ||
				allowedEmailDomains[domain] ||
				strings.HasSuffix(domain, ".example.com") ||
				strings.HasSuffix(domain, ".invalid") ||
				strings.HasSuffix(domain, ".test") ||
				strings.HasSuffix(domain, ".gserviceaccount.com") {
				continue
			}
			out = append(out, privacyFinding{
				file:   rel,
				line:   lineNo,
				kind:   "email",
				detail: fmt.Sprintf("unallowlisted email address %q", full),
			})
		}
	}

	if needPhone {
		scrubbed := uuidShapeRe.ReplaceAllString(line, "UUID")
		checkPhone := func(area, exch, sub string) {
			if exch == "555" && strings.HasPrefix(sub, "01") {
				return
			}
			out = append(out, privacyFinding{
				file:   rel,
				line:   lineNo,
				kind:   "phone",
				detail: fmt.Sprintf("phone-number shape (%s-%s-XXXX)", area, exch),
			})
		}
		for _, m := range parenPhoneRe.FindAllStringSubmatch(scrubbed, -1) {
			checkPhone(m[1], m[2], m[3])
		}
		for _, m := range intlPhoneRe.FindAllStringSubmatch(scrubbed, -1) {
			checkPhone(m[1], m[2], m[3])
		}
		for _, m := range dashPhoneRe.FindAllStringSubmatch(scrubbed, -1) {
			checkPhone(m[1], m[2], m[3])
		}
	}

	return out
}

func TestPrivacyScannerDetectors(t *testing.T) {
	// Construct test inputs via concatenation so this test file itself holds no
	// literal personal-data or credential shapes on disk.
	mustFlag := []struct {
		name string
		line string
		kind string
	}{
		{"google api key", "GEMINI_API_KEY=" + "AIza" + "SyD9876543210abcdefghijklmnop", "secret-shape"},
		{"sk api key", "OPENAI_API_KEY=" + "sk-" + "proj-0123456789abcdef01234567", "secret-shape"},
		{"github pat", "token " + "ghp_" + "0123456789abcdefghij0123456789", "secret-shape"},
		{"bearer jwt", "Authorization: Bearer " + "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", "secret-shape"},
		{"query key param", "https://api.example.com/v1?key=" + "secretQueryKey98765", "secret-shape"},
		{"pem private key", "-----BEGIN EC " + "PRIVATE KEY-----", "secret-pem"},
		{"macos personal home", "stored under /" + "Users/jdoe/.config/sheepdog/config.toml", "home-path"},
		{"linux personal home", "path: /" + "home/jdoe/projects/app", "home-path"},
		{"windows personal home", `C:\` + `Users\jdoe\AppData\Local\sheepdog`, "home-path"},
		{"personal mdns host", "connect to https://jdoe-mbp" + ".local:7777", "private-host"},
		{"personal single-word mdns url", "connect to http://jdoemac" + ".local:7777", "private-host"},
		{"personal tailnet", "open https://workbox.corp-acme" + ".ts.net:7777", "private-tailnet"},
		{"personal email", "contact jane.doe" + "@" + "gmail.com for details", "email"},
		{"us phone parens", "call (415) " + "867-5309 now", "phone"},
		{"intl phone", "reach +1 415 " + "867 5309", "phone"},
		{"us phone dashes", `phone: "415-` + `867-5309"`, "phone"},
	}
	for _, tc := range mustFlag {
		got := scanPrivacyLine("docs/example.md", 1, tc.line, true)
		matched := false
		for _, f := range got {
			if f.kind == tc.kind {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("%s: expected finding kind %q for %q, got %v", tc.name, tc.kind, tc.line, got)
		}
	}

	mustAllow := []struct {
		name string
		line string
	}{
		{"generic home user", "files live in /Users/you/.config/sheepdog and /home/alice/repo"},
		{"security contact", "Email kccarlos.opensource@gmail.com with steps"},
		{"example email", "user@example.com and bot@my-proj.iam.gserviceaccount.com"},
		{"asset scale filename", `"filename": "LaunchLogo@2x.png"`},
		{"go selector api key", "APIKey: cfg.Anthropic.APIKey, BaseURL: cfg.Anthropic.BaseURL"},
		{"env name reference", `env_http_headers = { "x-goog-api-key" = "VERTEX_API_KEY" }`},
		{"generic mdns", "open https://my-mac.local:7777 or https://macbook.local:7777"},
		{"code property .local", "let tz = TimeZone.local; return state.local.count"},
		{"generic tailnet", "https://mac-mini.tailnet.ts.net and box.yak-bebop.ts.net"},
		{"fictional 555-01xx phone", "example (415) 555-0199 or 415-555-0101"},
		{"uuid and iso date", "id dda19d26-9a6f-43a3-b88b-66f64e63940c on 2026-10-06T00:00:00+00:00"},
		{"already redacted", "Authorization: Bearer [REDACTED]"},
	}
	for _, tc := range mustAllow {
		if got := scanPrivacyLine("docs/example.md", 1, tc.line, true); len(got) > 0 {
			t.Errorf("%s: unexpected findings for %q: %v", tc.name, tc.line, got)
		}
	}

	if r := forbiddenRepoFileReason(".env"); r == "" {
		t.Error("expected .env to be forbidden")
	}
	if r := forbiddenRepoFileReason("keys/AuthKey_ABC123.p8"); r == "" {
		t.Error("expected .p8 push key to be forbidden")
	}
	if r := forbiddenRepoFileReason(".env.example"); r != "" {
		t.Errorf(".env.example should be allowed, got %q", r)
	}
}

type fileWork struct {
	abs     string
	rel     string
	symlink bool
}

func scanRepoFile(w fileWork) ([]privacyFinding, error) {
	var findings []privacyFinding
	if reason := forbiddenRepoFileReason(w.rel); reason != "" {
		findings = append(findings, privacyFinding{
			file:   w.rel,
			kind:   "forbidden-file",
			detail: reason,
		})
		return findings, nil
	}

	findings = append(findings, scanPrivacyLine(w.rel, 0, w.rel, false)...)

	if w.symlink {
		target, err := os.Readlink(w.abs)
		if err == nil {
			findings = append(findings, scanPrivacyLine(w.rel, 0, target, false)...)
		}
		return findings, nil
	}

	data, err := os.ReadFile(w.abs)
	if err != nil {
		return nil, err
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return findings, nil
	}

	// Real multi-line PEM private keys are rejected everywhere except redact_test.go.
	if w.rel != "internal/redact/redact_test.go" && bytes.Contains(data, bPrivKey) && pemBlockRe.Match(data) {
		findings = append(findings, privacyFinding{
			file:   w.rel,
			line:   1,
			kind:   "secret-pem",
			detail: "multi-line PEM private key block found",
		})
	}

	checkSecrets := !isSyntheticSecretFixture(w.rel)
	checkHomePaths := !isHistoricalArchiveDoc(w.rel)

	hasCandidate := (checkSecrets && hasSecretShapeCandidateBytes(data)) ||
		(checkHomePaths && (bytes.Contains(data, bSlashUsers) || bytes.Contains(data, bSlashHome) || bytes.Contains(data, bWinUsers))) ||
		bytes.Contains(data, bDotLocal) ||
		bytes.Contains(data, bDotTSNet) ||
		bytes.IndexByte(data, '@') >= 0 ||
		hasPhoneCandidateBytes(data)
	if !hasCandidate {
		return findings, nil
	}

	lineNo := 0
	for rem := data; len(rem) > 0; {
		lineNo++
		var line []byte
		if nl := bytes.IndexByte(rem, '\n'); nl >= 0 {
			line = rem[:nl]
			rem = rem[nl+1:]
		} else {
			line = rem
			rem = nil
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		findings = append(findings, scanPrivacyLineBytes(w.rel, lineNo, line, checkSecrets, checkHomePaths)...)
	}
	return findings, nil
}

func TestNoSecretsOrPersonalDataInRepo(t *testing.T) {
	root := repoRoot(t)
	var files []fileWork

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		// Skip .git whether it is a directory (clone) or a pointer file (worktree).
		if name == ".git" || name == ".DS_Store" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipRepoDirs[name] || skipRecordedRunRelPath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, fileWork{
			abs:     path,
			rel:     rel,
			symlink: d.Type()&fs.ModeSymlink != 0,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 2 {
		workers = 2
	}
	workCh := make(chan fileWork, 64)
	var (
		mu       sync.Mutex
		findings []privacyFinding
		firstErr error
		wg       sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range workCh {
				got, err := scanRepoFile(w)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				if len(got) > 0 {
					mu.Lock()
					findings = append(findings, got...)
					mu.Unlock()
				}
			}
		}()
	}
	for _, w := range files {
		workCh <- w
	}
	close(workCh)
	wg.Wait()

	if firstErr != nil {
		t.Fatal(firstErr)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].file != findings[j].file {
			return findings[i].file < findings[j].file
		}
		return findings[i].line < findings[j].line
	})
	for _, f := range findings {
		t.Error(f.String())
	}
	t.Logf("scanned %d repo files, %d findings", len(files), len(findings))
}
