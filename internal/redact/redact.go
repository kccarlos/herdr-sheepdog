// Package redact removes secrets from text before it is written to the
// runtime log or put in a diagnostic report (sheepdog doctor --report).
//
// A Redactor knows two kinds of secret: exact values (every key and token
// in the loaded config, and every environment variable whose name says it
// is a secret) and shapes (Google API keys, sk- keys, bearer tokens,
// token= / key= query parameters, PEM private keys), so a secret the
// config does not know about is still caught when it looks like one.
package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// Mask replaces every secret.
const Mask = "[REDACTED]"

// minSecretLen is the shortest exact value that is redacted: shorter
// values ("1", "on") would mangle ordinary text and are not secrets.
const minSecretLen = 6

// shapes are the secret patterns redacted wherever they appear. Each
// keeps its first group (the label) and masks the rest.
var shapes = []*regexp.Regexp{
	// Google API keys (AI Studio, Vertex express).
	regexp.MustCompile(`()AIza[0-9A-Za-z_\-]{20,}`),
	// A key a provider echoed back half-masked (sk-abcd****1234).
	regexp.MustCompile(`()\bsk-[0-9A-Za-z_\-]*\*{3,}[0-9A-Za-z_\-]*`),
	// OpenAI / DeepSeek / OpenRouter / Anthropic style keys.
	regexp.MustCompile(`()\bsk-[0-9A-Za-z_\-]{12,}`),
	// Authorization headers.
	regexp.MustCompile(`(?i)(bearer\s+)[0-9A-Za-z._~+/=\-]{8,}`),
	// token=, key=, api_key=, secret=, password= in URLs, env lines and
	// key: value text.
	regexp.MustCompile(`(?i)((?:^|[^0-9A-Za-z])(?:api[_-]?key|(?:access|auth|refresh|id|bearer)[_-]?token|client[_-]?secret|password)["']?\s*[=:]\s*["']?)[^\s"'&,;}]{6,}`),
	regexp.MustCompile(`(?i)([?&](?:key|token)=)[^\s"'&,;}]{6,}`),
	// PEM private keys (an APNs .p8 pasted into an issue).
	regexp.MustCompile(`(?s)(-----BEGIN [A-Z ]*PRIVATE KEY-----).*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// secretEnvName matches environment variables that hold a secret.
var secretEnvName = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS?)(S|_\d+)?$`)

func IsSecretEnvName(name string) bool { return secretEnvName.MatchString(name) }

// Redactor replaces secrets in text. The zero value redacts shapes only.
type Redactor struct {
	values []string // longest first, so a key that contains another goes first
}

// New returns a Redactor for the given exact values (empty and short
// ones are ignored) plus the built-in shapes. Configured values that JSON
// escaping alters (quotes, backslashes, newlines, HTML characters) also
// include their single- and double-escaped JSON string interiors so raw
// text containing serialized JSON masks them cleanly without breaking
// surrounding escapes.
func New(values ...string) *Redactor {
	seen := map[string]bool{}
	r := &Redactor{}
	add := func(s string) {
		if len(s) < minSecretLen || seen[s] {
			return
		}
		seen[s] = true
		r.values = append(r.values, s)
	}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < minSecretLen {
			continue
		}
		add(v)
		e1False := jsonEscapeString(v, false)
		e1True := jsonEscapeString(v, true)
		add(e1False)
		add(e1True)
		for _, e1 := range []string{e1False, e1True} {
			add(jsonEscapeString(e1, false))
			add(jsonEscapeString(e1, true))
		}
	}
	sort.Slice(r.values, func(i, j int) bool {
		if len(r.values[i]) != len(r.values[j]) {
			return len(r.values[i]) > len(r.values[j])
		}
		return r.values[i] < r.values[j]
	})
	return r
}

func jsonEscapeString(s string, escapeHTML bool) string {
	if escapeHTML {
		b, err := json.Marshal(s)
		if err != nil || len(b) < 2 {
			return s
		}
		return string(b[1 : len(b)-1])
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return s
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

// ConfigSecrets are every secret value in cfg: the Gemini key pool, the
// Vertex, DeepSeek and OpenAI voice keys, each OpenAI-compatible
// provider's key.
func ConfigSecrets(cfg config.Config) []string {
	out := append([]string{}, cfg.Gemini.APIKeys...)
	out = append(out, cfg.Gemini.Vertex.APIKey, cfg.DeepSeek.APIKey, cfg.Voice.OpenAI.APIKey, cfg.Anthropic.APIKey)
	for _, c := range cfg.Brain.OpenAICompat {
		out = append(out, c.APIKey)
	}
	return out
}

// EnvSecrets are the values of the environment variables whose names say
// they are secrets (…_KEY, …_TOKEN, …_SECRET, GEMINI_API_KEY_2, …).
func EnvSecrets(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && IsSecretEnvName(k) && !filepath.IsAbs(v) {
			out = append(out, v)
		}
	}
	return out
}

// ForConfig is the Redactor the daemon and the CLI use: cfg's secrets
// plus the secret-looking environment variables of this process (which
// include the env file's, once it is loaded).
func ForConfig(cfg config.Config) *Redactor {
	return New(append(ConfigSecrets(cfg), EnvSecrets(os.Environ())...)...)
}

// String returns s with every secret replaced by Mask.
func (r *Redactor) String(s string) string {
	if r != nil {
		for _, v := range r.values {
			s = strings.ReplaceAll(s, v, Mask)
		}
	}
	// Most context contains no credential syntax. Avoid running every
	// regular expression over ordinary prompt text on every model step.
	lower := strings.ToLower(s)
	candidates := [...]bool{
		strings.Contains(s, "AIza"),
		strings.Contains(s, "sk-"),
		strings.Contains(s, "sk-"),
		strings.Contains(lower, "bearer"),
		strings.ContainsAny(s, "=:") && (strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password")),
		strings.ContainsAny(s, "?&") && (strings.Contains(lower, "key") || strings.Contains(lower, "token")),
		strings.Contains(s, "-----BEGIN "),
	}
	for i, re := range shapes {
		if candidates[i] {
			s = re.ReplaceAllString(s, "${1}"+Mask)
		}
	}
	return s
}

// Writer returns an io.Writer that redacts each line before passing it
// to w. A partial line is held until its newline (or Flush), so a secret
// split across two Write calls is still caught.
func (r *Redactor) Writer(w io.Writer) *Writer {
	return &Writer{r: r, w: w}
}

// Writer is a line-buffered redacting writer (Redactor.Writer). It is
// safe for concurrent use.
type Writer struct {
	r   *Redactor
	w   io.Writer
	mu  sync.Mutex
	buf []byte
}

// maxPending bounds a line held for its newline.
const maxPending = 64 << 10

// Write implements io.Writer. It always reports len(p) written: a log
// write failure must not fail the caller.
func (x *Writer) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.buf = append(x.buf, p...)
	for {
		i := bytes.IndexByte(x.buf, '\n')
		if i < 0 {
			break
		}
		_, _ = io.WriteString(x.w, x.r.String(string(x.buf[:i+1])))
		x.buf = x.buf[i+1:]
	}
	if len(x.buf) > maxPending {
		_, _ = io.WriteString(x.w, x.r.String(string(x.buf)))
		x.buf = x.buf[:0]
	}
	return len(p), nil
}

// SetRedactor replaces the Redactor (the daemon reloads its keys in
// place: a key saved by sheepdog setup must be redacted from then on).
func (x *Writer) SetRedactor(r *Redactor) {
	x.mu.Lock()
	x.r = r
	x.mu.Unlock()
}

// Flush writes a held partial line.
func (x *Writer) Flush() {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.buf) > 0 {
		_, _ = io.WriteString(x.w, x.r.String(string(x.buf)))
		x.buf = x.buf[:0]
	}
}
