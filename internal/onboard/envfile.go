package onboard

import (
	"bufio"
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

// PhoneBind is the web.bind phone access sets: every address of this
// computer (its LAN and Tailscale ones), TLS only and paired devices
// only. web.bind is one address, so this is the narrowest setting that
// covers both.
const PhoneBind = "0.0.0.0"

// SetPhoneAccess writes phone access to the env file: enable sets
// SHEEPDOG_WEB_BIND to PhoneBind, off goes back to loopback.
func SetPhoneAccess(envPath string, enable bool) error {
	bind := config.DefaultWebBind
	if enable {
		bind = PhoneBind
	}
	return UpsertEnvFile(envPath, map[string]string{"SHEEPDOG_WEB_BIND": bind})
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
	return strings.Join(setKeysAt(lines, header, kv), "\n")
}

// setKeysAt sets each key = "value" in the table whose header is
// lines[header] (it ends at the next line starting with '['): an
// uncommented assignment is replaced, a missing key is added right
// under the header. Comments and other keys stay as they are.
func setKeysAt(lines []string, header int, kv [][2]string) []string {
	end := tableEnd(lines, header)
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
	return append(out, lines[header+1:]...)
}

// tableEnd is the index of the line after the table starting at header.
func tableEnd(lines []string, header int) int {
	for i := header + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			return i
		}
	}
	return len(lines)
}

// UpsertCompat makes the config at path define the OpenAI-compatible
// provider c with c's base_url, api_key_env and model: a new config (the
// template, provider set to it) when there is none, the table appended
// when no provider of that name exists, else those three keys set in
// place in the existing table (its label and comments stay). The result
// must load; otherwise the old file is restored.
func UpsertCompat(path string, c config.OpenAICompat) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return WriteConfig(path, "compat", &c)
	}
	cur, err := config.Load(path)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var body string
	if _, ok := cur.Brain.CompatProvider(c.Name); !ok {
		body = strings.TrimRight(string(old), "\n") + "\n\n" + CompatTOML(c)
	} else {
		lines := strings.Split(string(old), "\n")
		header := compatHeader(lines, c.Name)
		if header < 0 {
			return fmt.Errorf("cannot find the [[brain.openai_compat]] table named %q to update; edit it by hand", c.Name)
		}
		body = strings.Join(setKeysAt(lines, header, [][2]string{{"base_url", c.BaseURL}, {"api_key_env", c.APIKeyEnv}, {"model", c.Model}}), "\n")
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	if _, err := config.Load(path); err != nil {
		_ = os.WriteFile(path, old, 0o600)
		return fmt.Errorf("the config with the provider does not load (restored it): %w", err)
	}
	return nil
}

// compatHeader is the line of the [[brain.openai_compat]] header whose
// table has name = "<name>" (-1: none).
func compatHeader(lines []string, name string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) != "[[brain.openai_compat]]" {
			continue
		}
		for j := i + 1; j < tableEnd(lines, i); j++ {
			t := strings.TrimSpace(lines[j])
			k, v, ok := strings.Cut(t, "=")
			if !ok || strings.HasPrefix(t, "#") || strings.TrimSpace(k) != "name" {
				continue
			}
			v, _, _ = strings.Cut(strings.TrimSpace(v), " #")
			if strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`)) == name {
				return i
			}
		}
	}
	return -1
}
