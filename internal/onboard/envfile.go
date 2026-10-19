package onboard

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// ReadEnvFile parses a KEY=VALUE file the way config.LoadEnvFile does
// (blank lines and # comments skipped, one layer of matching quotes
// stripped) without touching the process environment. A missing file
// is an empty map.
func ReadEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}

// UpsertEnvFile sets each key of set in the KEY=VALUE file at path:
// an existing line for the key is replaced in place, new keys are
// appended (sorted), every other line and comment is kept. The file is
// written to a temporary file and renamed, with mode 0600, its directory
// created 0700. Values are never printed.
func UpsertEnvFile(path string, set map[string]string) error {
	return EditEnvFile(path, set, nil)
}

// EditEnvFile is UpsertEnvFile that also removes the lines of the keys
// in del (a choice undone: SHEEPDOG_BRAIN_MODEL when the orchestrator
// goes back to the configured one).
func EditEnvFile(path string, set map[string]string, del []string) error {
	drop := map[string]bool{}
	for _, k := range del {
		if _, both := set[k]; !both {
			drop[k] = true
		}
	}
	for k, v := range set {
		if k == "" || strings.ContainsAny(k, "= \t\r\n#") {
			return fmt.Errorf("onboard: env key %q: not a variable name", k)
		}
		if strings.ContainsAny(v, "\r\n") || v != strings.TrimSpace(v) {
			return fmt.Errorf("onboard: the value of %s has a line break or surrounding spaces", k)
		}
	}
	var lines []string
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}
	done := map[string]bool{}
	kept := lines[:0]
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			kept = append(kept, line)
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(k)
		if ok && drop[k] {
			continue
		}
		if v, want := set[k]; ok && want {
			if done[k] {
				continue // a duplicate line of a key being set
			}
			line = k + "=" + v
			done[k] = true
		}
		kept = append(kept, line)
	}
	lines = kept
	var add []string
	for k := range set {
		if !done[k] {
			add = append(add, k)
		}
	}
	sort.Strings(add)
	for _, k := range add {
		lines = append(lines, k+"="+set[k])
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".env-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// NewToken returns a random web token: 32 bytes, base64url (43
// characters).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HasToken reports whether a web token exists anywhere the daemon reads
// one: the process environment, the env file (env) or the config.
// A token that exists is never replaced silently: the phone is paired
// with it (`sheepdog init`, the setup page's "new token" is explicit).
func HasToken(env map[string]string, cfg config.Config) bool {
	return strings.TrimSpace(os.Getenv("SHEEPDOG_WEB_TOKEN")) != "" || strings.TrimSpace(env["SHEEPDOG_WEB_TOKEN"]) != "" || cfg.Web.Token != ""
}

// WriteConfig writes a new config file: the commented template with the
// chosen orchestrator (gemini, deepseek or compat) filled in, mode 0600.
func WriteConfig(path, orch string, compat *config.OpenAICompat) error {
	t := config.Template()
	var lines string
	switch orch {
	case brain.ProviderDeepSeek:
		lines = fmt.Sprintf("provider = %q\nmodel = %q\n", brain.ProviderDeepSeek, brain.DefaultDeepSeekModel)
	case "compat":
		lines = fmt.Sprintf("provider = %q\n", compat.Name)
	}
	if lines != "" {
		if !strings.Contains(t, "\n[brain]\n") {
			return errors.New("config template has no [brain] table")
		}
		t = strings.Replace(t, "\n[brain]\n", "\n[brain]\n"+lines, 1)
	}
	if compat != nil {
		t += "\n" + CompatTOML(*compat)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(t), 0o600)
}

// CompatTOML renders one [[brain.openai_compat]] table.
func CompatTOML(c config.OpenAICompat) string {
	return fmt.Sprintf("[[brain.openai_compat]]\nname = %q\nbase_url = %q\napi_key_env = %q\nmodel = %q\n", c.Name, c.BaseURL, c.APIKeyEnv, c.Model)
}

// EnsureCompat makes the config at path define the OpenAI-compatible
// provider c: a new config (the template, provider set to it) when there
// is none, else its table appended when no provider of that name exists.
// An existing table of that name is left as it is. The result must load.
func EnsureCompat(path string, c config.OpenAICompat) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return WriteConfig(path, "compat", &c)
	}
	cur, err := config.Load(path)
	if err != nil {
		return err
	}
	if _, ok := cur.Brain.CompatProvider(c.Name); ok {
		return nil
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := strings.TrimRight(string(old), "\n") + "\n\n" + CompatTOML(c)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	if _, err := config.Load(path); err != nil {
		_ = os.WriteFile(path, old, 0o600)
		return fmt.Errorf("the config with the new provider does not load (restored it): %w", err)
	}
	return nil
}

// PhoneBind is the web.bind phone access sets: every address of this
// computer (its LAN and Tailscale ones), behind the web token. web.bind
// is one address, so this is the narrowest setting that covers both.
const PhoneBind = "0.0.0.0"

// SetPhoneAccess writes phone access to the env file: enable sets
// SHEEPDOG_WEB_BIND to PhoneBind (off: back to loopback) and creates a
// token when none exists anywhere (HasToken); newToken replaces the
// token, which a paired phone must then scan again. It reports whether
// a token was written. cfg is the config in force (its token counts).
func SetPhoneAccess(envPath string, cfg config.Config, enable, newToken bool) (bool, error) {
	saved, err := ReadEnvFile(envPath)
	if err != nil {
		return false, err
	}
	set := map[string]string{"SHEEPDOG_WEB_BIND": config.DefaultWebBind}
	if enable {
		set["SHEEPDOG_WEB_BIND"] = PhoneBind
	}
	created := false
	if newToken || (enable && !HasToken(saved, cfg)) {
		if newToken && saved["SHEEPDOG_WEB_TOKEN"] == "" && strings.TrimSpace(os.Getenv("SHEEPDOG_WEB_TOKEN")) != "" && os.Getenv("SHEEPDOG_WEB_TOKEN") == cfg.Web.Token {
			return false, errors.New("the token comes from the environment variable SHEEPDOG_WEB_TOKEN, not the env file; change it there")
		}
		tok, err := NewToken()
		if err != nil {
			return false, err
		}
		set["SHEEPDOG_WEB_TOKEN"] = tok
		created = true
	}
	return created, UpsertEnvFile(envPath, set)
}

// SetVertexConfig writes project and, when not empty, location into the
// config's [gemini.vertex] table (the key stays in the env file): a new
// config from the template when there is none, the values set in place
// (uncommented) when the table exists, else the table appended. The result
// must load; otherwise the old file is restored.
func SetVertexConfig(path, project, location string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := WriteConfig(path, brain.ProviderGemini, nil); err != nil {
			return err
		}
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	set := [][2]string{{"project", project}}
	if location != "" {
		set = append(set, [2]string{"location", location})
	}
	body := setTOMLKeys(string(old), "gemini.vertex", set)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	if _, err := config.Load(path); err != nil {
		_ = os.WriteFile(path, old, 0o600)
		return fmt.Errorf("the config with the Vertex project does not load (restored it): %w", err)
	}
	return nil
}

// setTOMLKeys sets each key = "value" in table of the TOML text body:
// replacing an uncommented assignment of that key inside the table, else
// adding it right under the table header; a missing table is appended.
func setTOMLKeys(body, table string, kv [][2]string) string {
	lines := strings.Split(body, "\n")
	header := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "["+table+"]" {
			header = i
			break
		}
	}
	if header < 0 {
		out := strings.TrimRight(body, "\n") + "\n\n[" + table + "]\n"
		for _, p := range kv {
			out += fmt.Sprintf("%s = %q\n", p[0], p[1])
		}
		return out
	}
	end := len(lines)
	for i := header + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	var add []string
	for _, p := range kv {
		line := fmt.Sprintf("%s = %q", p[0], p[1])
		found := false
		for i := header + 1; i < end; i++ {
			t := strings.TrimSpace(lines[i])
			if k, _, ok := strings.Cut(t, "="); ok && !strings.HasPrefix(t, "#") && strings.TrimSpace(k) == p[0] {
				lines[i], found = line, true
				break
			}
		}
		if !found {
			add = append(add, line)
		}
	}
	out := append([]string{}, lines[:header+1]...)
	out = append(out, add...)
	out = append(out, lines[header+1:]...)
	return strings.Join(out, "\n")
}
