package hc_test

import (
	"path/filepath"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func TestDefaultSocket(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		home      string
		uid       int
		runExists bool
		want      string
	}{
		{
			name: "explicit socket wins",
			env: map[string]string{
				"HC_SOCKET":       "/tmp/custom.sock",
				"XDG_RUNTIME_DIR": "/tmp/runtime",
				"HC_SESSION":      "scratch",
			},
			home: "/home/user",
			uid:  501,
			want: "/tmp/custom.sock",
		},
		{
			name: "runtime directory with custom session",
			env: map[string]string{
				"XDG_RUNTIME_DIR": "/tmp/runtime",
				"HC_SESSION":      "scratch",
			},
			home: "/home/user",
			uid:  501,
			want: filepath.Join("/tmp/runtime", "herdr-core", "scratch.sock"),
		},
		{
			name: "run user directory",
			env: map[string]string{
				"HC_SESSION": "work",
			},
			home:      "/home/user",
			uid:       501,
			runExists: true,
			want:      filepath.Join("/run/user/501/herdr-core", "work.sock"),
		},
		{
			name: "xdg state fallback",
			env: map[string]string{
				"HC_SESSION":     "work",
				"XDG_STATE_HOME": "/tmp/state",
			},
			home: "/home/user",
			uid:  501,
			want: filepath.Join("/tmp/state", "herdr-core", "work", "work.sock"),
		},
		{
			name: "home state fallback with default session",
			env:  map[string]string{},
			home: "/home/user",
			uid:  501,
			want: filepath.Join("/home/user", ".local", "state", "herdr-core", "default", "default.sock"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			got := hc.DefaultSocket(getenv, tt.home, tt.uid, func(path string) bool {
				wantPath := filepath.Join("/run/user/501/herdr-core")
				if path != wantPath {
					t.Fatalf("exists check path = %q, want %q", path, wantPath)
				}
				return tt.runExists
			})
			if got != tt.want {
				t.Fatalf("DefaultSocket() = %q, want %q", got, tt.want)
			}
		})
	}
}
