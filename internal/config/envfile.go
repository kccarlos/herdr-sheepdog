package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnvFile reads path as a small dotenv-style file — one KEY=VALUE per
// line, blank lines and lines starting with # ignored — and sets each KEY
// in the process environment via os.Setenv, so a later config.Load (which
// reads SHEEPDOG_*/GEMINI_API_KEY* from the environment) picks the values
// up. It intentionally does not support "export KEY=VALUE", quoting
// beyond a single matching pair of surrounding quotes, or any shell
// expansion (no $VAR, no command substitution, no globbing) — the file is
// a flat list of literal values, not a shell script.
//
// A KEY already set in the real environment is left alone: the
// environment always wins over the file, so `SHEEPDOG_WEB_PORT=8080
// sheepdog serve` still overrides an .env entry. Values are never
// logged, by any caller of this function or of config.Load.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("config: env-file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("config: env-file %s:%d: not KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("config: env-file %s:%d: empty key", path, lineNo)
		}
		if _, set := os.LookupEnv(key); set {
			continue // the real environment always wins
		}
		if err := os.Setenv(key, unquoteEnvValue(strings.TrimSpace(val))); err != nil {
			return fmt.Errorf("config: env-file %s:%d: setenv %s: %w", path, lineNo, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("config: env-file %s: %w", path, err)
	}
	return nil
}

// unquoteEnvValue strips one layer of surrounding matching quotes
// ("value" or 'value'), the one bit of dotenv convenience worth
// supporting without any real shell parsing.
func unquoteEnvValue(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
