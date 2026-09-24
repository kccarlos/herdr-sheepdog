package config

import (
	"fmt"
	"strconv"

	"github.com/BurntSushi/toml"
)

// renamedBrainKeys are [brain] keys that were renamed: the old name is
// still read (file and SHEEPDOG_* env) when the new one is not set, with
// a deprecation note.
var renamedBrainKeys = []struct {
	old, new       string // TOML keys under [brain]
	oldEnv, newEnv string
	dst            func(*Brain) *int
}{
	{"propose_timeout_s", "utterance_timeout_s", "SHEEPDOG_BRAIN_PROPOSE_TIMEOUT_S", "SHEEPDOG_BRAIN_UTTERANCE_TIMEOUT_S", func(b *Brain) *int { return &b.UtteranceTimeoutS }},
	{"enrich_timeout_s", "announce_turn_timeout_s", "SHEEPDOG_BRAIN_ENRICH_TIMEOUT_S", "SHEEPDOG_BRAIN_ANNOUNCE_TURN_TIMEOUT_S", func(b *Brain) *int { return &b.AnnounceTurnTimeoutS }},
}

// droppedBrainKeys are [brain] keys that no longer do anything: an old
// file or environment that still sets one loads, with a note.
var droppedBrainKeys = []struct{ key, env, why string }{
	{"ask_timeout_s", "SHEEPDOG_BRAIN_ASK_TIMEOUT_S", "the ask turn is gone; utterance_timeout_s bounds every shepherd turn"},
}

// Deprecations lists the deprecated config keys and environment
// variables the last Load read, one human-readable note each ("" when
// there were none). The daemon logs them once at startup
// (daemon.config_deprecated) and `sheepdog config show` prints them.
func (c Config) Deprecations() []string { return c.deprecations }

// applyDeprecatedKeys reads the renamed and dropped [brain] keys from the
// file: a renamed key sets its new field unless the new key is in the
// file too (the new name wins); a dropped key is only noted.
func applyDeprecatedKeys(cfg *Config, data []byte, md toml.MetaData) error {
	var old struct {
		Brain map[string]any `toml:"brain"`
	}
	if _, err := toml.Decode(string(data), &old); err != nil {
		return err
	}
	for _, k := range renamedBrainKeys {
		if !md.IsDefined("brain", k.old) {
			continue
		}
		if md.IsDefined("brain", k.new) {
			cfg.deprecations = append(cfg.deprecations, fmt.Sprintf("brain.%s is deprecated and ignored: brain.%s is set too", k.old, k.new))
			continue
		}
		n, ok := old.Brain[k.old].(int64)
		if !ok {
			return fmt.Errorf("brain.%s: want an integer", k.old)
		}
		*k.dst(&cfg.Brain) = int(n)
		cfg.deprecations = append(cfg.deprecations, fmt.Sprintf("brain.%s is deprecated: rename it to brain.%s", k.old, k.new))
	}
	for _, k := range droppedBrainKeys {
		if md.IsDefined("brain", k.key) {
			cfg.deprecations = append(cfg.deprecations, fmt.Sprintf("brain.%s is no longer used (%s): remove it", k.key, k.why))
		}
	}
	return nil
}

// applyDeprecatedEnv is applyDeprecatedKeys for the environment: an old
// variable sets the new field unless the new variable is set too (it is
// applied after, by applyBrainEnv, and wins). Run before applyEnv.
func applyDeprecatedEnv(cfg *Config, lookup func(string) (string, bool)) error {
	for _, k := range renamedBrainKeys {
		v, ok := lookup(k.oldEnv)
		if !ok {
			continue
		}
		if _, set := lookup(k.newEnv); set {
			cfg.deprecations = append(cfg.deprecations, fmt.Sprintf("%s is deprecated and ignored: %s is set too", k.oldEnv, k.newEnv))
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: %s=%q: want a positive integer", k.oldEnv, v)
		}
		*k.dst(&cfg.Brain) = n
		cfg.deprecations = append(cfg.deprecations, fmt.Sprintf("%s is deprecated: rename it to %s", k.oldEnv, k.newEnv))
	}
	for _, k := range droppedBrainKeys {
		if _, ok := lookup(k.env); ok {
			cfg.deprecations = append(cfg.deprecations, fmt.Sprintf("%s is no longer used (%s): unset it", k.env, k.why))
		}
	}
	return nil
}
