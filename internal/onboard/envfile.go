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
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(k)
		if v, want := set[k]; ok && want {
			lines[i] = k + "=" + v
			done[k] = true
		}
	}
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
