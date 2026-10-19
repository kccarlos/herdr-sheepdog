package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// EnvRecord remembers which env file the last daemon loaded its keys
// from, so a restart (sheepdog restart, sheepdog start, the restart after
// an upgrade) reads the same file instead of looking in whatever working
// directory it runs from. It holds a path and a flag, never a value.
//
//	<data_dir>/env-file.json   JSON {path, had_keys, at}
type EnvRecord struct {
	// Path is the absolute path of the env file the daemon loaded.
	Path string `json:"path"`
	// HadKeys is true when that run came up with its keys (not in setup
	// mode).
	HadKeys bool      `json:"had_keys"`
	At      time.Time `json:"at"`
}

// EnvRecordFile is the record's file.
func EnvRecordFile(dataDir string) string { return filepath.Join(dataDir, "env-file.json") }

// ReadEnvRecord reads the record; the zero value when there is none.
func ReadEnvRecord(dataDir string) EnvRecord {
	var r EnvRecord
	if b, err := os.ReadFile(EnvRecordFile(dataDir)); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

// WriteEnvRecord saves the record (mode 0600).
func WriteEnvRecord(dataDir string, r EnvRecord) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := EnvRecordFile(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, EnvRecordFile(dataDir))
}
