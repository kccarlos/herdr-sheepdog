// Package config loads the sheepdog daemon configuration.
//
// Configuration is read from ~/.config/sheepdog/config.toml when the file
// exists, then overridden by SHEEPDOG_* environment variables (and, for the
// Gemini key pool, GEMINI_API_KEY*). Only the keys needed by the herdr
// client, state table and voice session are defined so far; later work
// packages add their own sections (llm, cli, web) here.
//
// String() must never print secrets: Gemini keys are rendered as a count.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

// Config is the fully resolved daemon configuration.
type Config struct {
	// DataDir is where the daemon keeps its files (state, event log).
	DataDir string `toml:"data_dir"`
	// DebounceMS is how long a `blocked` state must persist before it becomes
	// a transition (plan §3.1).
	DebounceMS int `toml:"debounce_ms"`
	// SnapshotIntervalS is the period of the session.snapshot diff loop.
	SnapshotIntervalS int `toml:"snapshot_interval_s"`
	// Aliases maps pane_id -> spoken alias, e.g. "w9:p1" = "auth".
	Aliases map[string]string `toml:"aliases"`

	Herdr  Herdr  `toml:"herdr"`
	State  State  `toml:"state"`
	Gemini Gemini `toml:"gemini"`
}

// Gemini configures the voice session (plan §6).
type Gemini struct {
	// APIKeys is the key pool. The voice session binds to one key per
	// connection and rotates to the next on a rate-limit error at connect.
	// Never logged.
	APIKeys []string `toml:"api_keys"`
	// Model is the Live model name.
	Model string `toml:"model"`
	// Voice is the prebuilt voice name.
	Voice string `toml:"voice"`
}

// Herdr configures the herdr socket client.
type Herdr struct {
	// Socket is the path of the herdr unix socket.
	Socket string `toml:"socket"`
}

// State configures the persisted state table.
type State struct {
	// Path is the JSON file the state table is persisted to.
	Path string `toml:"path"`
}

// Default returns the built-in defaults, resolved against the current user's
// home directory. If the home directory cannot be determined, relative
// paths are used so the daemon still starts.
func Default() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dataDir := filepath.Join(home, ".local", "share", "sheepdog")
	return Config{
		DataDir:           dataDir,
		DebounceMS:        3000,
		SnapshotIntervalS: 30,
		Aliases:           map[string]string{},
		Herdr:             Herdr{Socket: filepath.Join(home, ".config", "herdr", "herdr.sock")},
		State:             State{Path: filepath.Join(dataDir, "state.json")},
		Gemini:            Gemini{Model: "gemini-3.8-live", Voice: "Puck"},
	}
}

// DefaultPath returns the default location of the config file.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "sheepdog", "config.toml")
}

// Load reads the TOML file at path (DefaultPath() when path is empty) on top
// of Default(), then applies SHEEPDOG_* environment overrides. A missing file
// is not an error; a malformed one is.
func Load(path string) (Config, error) {
	return load(path, os.LookupEnv)
}

func load(path string, lookup func(string) (string, bool)) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if _, err := toml.Decode(string(data), &cfg); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// No file: defaults + env only.
	default:
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	if err := applyEnv(&cfg, lookup); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overrides cfg from SHEEPDOG_* variables. Recognised names:
//
//	SHEEPDOG_DATA_DIR, SHEEPDOG_DEBOUNCE_MS, SHEEPDOG_SNAPSHOT_INTERVAL_S,
//	SHEEPDOG_HERDR_SOCKET, SHEEPDOG_STATE_PATH, SHEEPDOG_GEMINI_MODEL,
//	SHEEPDOG_GEMINI_VOICE.
//
// The Gemini key pool is collected from GEMINI_API_KEY, then
// GEMINI_API_KEY_1 … GEMINI_API_KEY_9, in that order; if any is set the
// pool from the file is replaced. Aliases are only configurable through
// the TOML file.
func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup("SHEEPDOG_GEMINI_MODEL"); ok {
		cfg.Gemini.Model = v
	}
	if v, ok := lookup("SHEEPDOG_GEMINI_VOICE"); ok {
		cfg.Gemini.Voice = v
	}
	if keys := geminiKeysFromEnv(lookup); len(keys) > 0 {
		cfg.Gemini.APIKeys = keys
	}
	if v, ok := lookup("SHEEPDOG_DATA_DIR"); ok {
		cfg.DataDir = v
	}
	if v, ok := lookup("SHEEPDOG_HERDR_SOCKET"); ok {
		cfg.Herdr.Socket = v
	}
	if v, ok := lookup("SHEEPDOG_STATE_PATH"); ok {
		cfg.State.Path = v
	}
	if v, ok := lookup("SHEEPDOG_DEBOUNCE_MS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("config: SHEEPDOG_DEBOUNCE_MS=%q: want a non-negative integer", v)
		}
		cfg.DebounceMS = n
	}
	if v, ok := lookup("SHEEPDOG_SNAPSHOT_INTERVAL_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SNAPSHOT_INTERVAL_S=%q: want a positive integer", v)
		}
		cfg.SnapshotIntervalS = n
	}
	return nil
}

// geminiKeysFromEnv returns the non-empty values of GEMINI_API_KEY and
// GEMINI_API_KEY_1..9 in that order.
func geminiKeysFromEnv(lookup func(string) (string, bool)) []string {
	var keys []string
	if v, ok := lookup("GEMINI_API_KEY"); ok && v != "" {
		keys = append(keys, v)
	}
	for i := 1; i <= 9; i++ {
		if v, ok := lookup(fmt.Sprintf("GEMINI_API_KEY_%d", i)); ok && v != "" {
			keys = append(keys, v)
		}
	}
	return keys
}

// String renders the configuration for logs. Secrets are rendered as
// counts only; keep it that way when secret-bearing fields are added.
func (c Config) String() string {
	return fmt.Sprintf("data_dir=%s herdr.socket=%s state.path=%s debounce_ms=%d snapshot_interval_s=%d aliases=%d gemini.model=%s gemini.voice=%s gemini.api_keys=%d",
		c.DataDir, c.Herdr.Socket, c.State.Path, c.DebounceMS, c.SnapshotIntervalS, len(c.Aliases), c.Gemini.Model, c.Gemini.Voice, len(c.Gemini.APIKeys))
}
