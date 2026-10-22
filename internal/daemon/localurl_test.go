package daemon

import "testing"

// The CLI always talks to loopback: plain HTTP is served only there, and a
// daemon bound to one LAN address listens on loopback too.
func TestLocalURLIsAlwaysLoopback(t *testing.T) {
	for bind, want := range map[string]string{
		"":              "http://127.0.0.1:7777",
		"0.0.0.0":       "http://127.0.0.1:7777",
		"127.0.0.1":     "http://127.0.0.1:7777",
		"192.168.1.20":  "http://127.0.0.1:7777",
		"100.101.102.1": "http://127.0.0.1:7777",
		"::1":           "http://[::1]:7777",
	} {
		if got := LocalURL(bind, 7777); got != want {
			t.Errorf("LocalURL(%q) = %s, want %s", bind, got, want)
		}
	}
}
