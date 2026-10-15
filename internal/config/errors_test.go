package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigErrorsNameFileLineAndKey(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       []string
	}{
		{"bad value", "[web]\nport = 7777\n\n[supervisor]\nchattiness = \"loud\"\n", []string{"line 5", "key supervisor.chattiness", `"loud" is not valid`, "quiet, normal, chatty"}},
		{"wrong type", "[web]\nport = \"7777\"\n", []string{"line 2", "key web.port", "want an integer, got a string"}},
		{"syntax", "[web\nport = 1\n", []string{"line ", "expected"}},
		{"log level", "[log]\nlevel = \"loud\"\n", []string{"line 2", "key log.level", "debug, info, warn, error"}},
		{"retention", "[events]\nretention_days = -1\n", []string{"line 2", "key events.retention_days"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := load(path, func(string) (string, bool) { return "", false })
			if err == nil {
				t.Fatal("no error")
			}
			msg := err.Error()
			for _, w := range append([]string{path}, c.want...) {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q lacks %q", msg, w)
				}
			}
		})
	}
}

func TestLogAndEventsDefaults(t *testing.T) {
	d := Default()
	if d.Log.Level != "info" || d.Log.MaxSizeMB != 10 || d.Log.MaxFiles != 3 || d.Events.RetentionDays != 14 {
		t.Errorf("defaults: %+v %+v", d.Log, d.Events)
	}
	cfg, err := load(filepath.Join(t.TempDir(), "none.toml"), func(k string) (string, bool) {
		v, ok := map[string]string{"SHEEPDOG_EVENTS_RETENTION_DAYS": "0", "SHEEPDOG_LOG_LEVEL": "debug"}[k]
		return v, ok
	})
	if err != nil || cfg.Events.RetentionDays != 0 || cfg.Log.Level != "debug" {
		t.Errorf("env overrides: %v %+v %+v", err, cfg.Events, cfg.Log)
	}
}
