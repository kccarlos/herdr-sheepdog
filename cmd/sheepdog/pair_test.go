package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// fakePairDaemon answers GET /api/pair on loopback like a running daemon
// and returns a config file that points at it.
func fakePairDaemon(t *testing.T, addrs []pair.Address, errText string) (cfgPath, envPath, code string) {
	t.Helper()
	code = "one-time-code-not-to-be-printed"
	link := pair.Link(pair.LinkInfo{DaemonID: "AAAAAAAAAAAAAAAAAAAAAA", Name: "Studio Mac", Pin: "pin", Code: code, Addresses: addrs})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pair" || r.URL.Query().Get("new") != "1" {
			http.NotFound(w, r)
			return
		}
		exp := time.Date(2026, 10, 21, 12, 10, 0, 0, time.UTC)
		resp := map[string]any{"addresses": addrs, "link": link, "expires": exp}
		if errText != "" {
			resp = map[string]any{"addresses": []pair.Address{}, "error": errText}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("[web]\nbind = \"0.0.0.0\"\nport = %s\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath = filepath.Join(dir, "x.env")
	_ = os.WriteFile(envPath, nil, 0o600)
	unsetWebEnv(t)
	return cfgPath, envPath, code
}

var twoAddrs = []pair.Address{{URL: "https://100.101.102.103:7777", Kind: pair.KindTailscale}, {URL: "https://192.168.1.20:7777", Kind: pair.KindLAN}}

// The code comes from the running daemon and lists both addresses; the
// one-time code is in the QR only, never in the text.
func TestPair(t *testing.T) {
	cfg, env, code := fakePairDaemon(t, twoAddrs, "")
	var out, errb strings.Builder
	if c := run(context.Background(), []string{"pair", "--config", cfg, "--env-file", filepath.Join(filepath.Dir(cfg), "none.env")}, &out, &errb); c == 0 {
		t.Fatal("a missing --env-file was accepted")
	}
	out.Reset()
	if c := run(context.Background(), []string{"pair", "--config", cfg, "--env-file", env}, &out, &errb); c != 0 {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	text := out.String()
	for _, want := range []string{"lan        192.168.1.20:7777", "tailscale  100.101.102.103:7777", "works once", "▀", "Uses your Tailscale address 100.101.102.103"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, code) || strings.Contains(errb.String(), code) {
		t.Error("the code was printed")
	}
}

// No reachable address, or no daemon: a plain error.
func TestPairNoAddressOrNoDaemon(t *testing.T) {
	cfg, env, _ := fakePairDaemon(t, nil, "the daemon listens on 127.0.0.1 only, so a phone cannot reach it: set web.bind")
	var out, errb strings.Builder
	if c := run(context.Background(), []string{"pair", "--config", cfg, "--env-file", env}, &out, &errb); c != 1 || !strings.Contains(errb.String(), "web.bind") {
		t.Errorf("exit %d: %q", c, errb.String())
	}
	dir := t.TempDir()
	none := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(none, []byte("[web]\nport = 1\n"), 0o600)
	errb.Reset()
	if c := run(context.Background(), []string{"pair", "--config", none, "--env-file", env}, &out, &errb); c != 1 || !strings.Contains(errb.String(), "not answering") {
		t.Errorf("no daemon: exit %d: %q", c, errb.String())
	}
}

// unsetWebEnv clears the web overrides for the test (an empty value
// still counts as set).
func unsetWebEnv(t *testing.T) {
	for _, k := range []string{"SHEEPDOG_WEB_BIND", "SHEEPDOG_WEB_PORT"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// The herdr plugin opens `pair --compact` in a popup of 72 columns by 42
// rows (herdr-plugin.toml), less 2 each for the frame, and its wrapper
// adds 2 rows (a blank line and the "press Enter" prompt). With three
// addresses the output must still fit, the code included.
func TestPairCompactFitsThePluginPopup(t *testing.T) {
	addrs := []pair.Address{
		{URL: "https://100.101.102.103:7777", Kind: pair.KindTailscale},
		{URL: "https://192.168.100.200:7777", Kind: pair.KindLAN},
		{URL: "https://10.20.30.40:7777", Kind: pair.KindLAN},
	}
	cfg, env, _ := fakePairDaemon(t, addrs, "")
	var out, errb strings.Builder
	if c := run(context.Background(), []string{"pair", "--compact", "--plain", "--config", cfg, "--env-file", env}, &out, &errb); c != 0 {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	width := 0
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > width {
			width = n
		}
	}
	t.Logf("compact pair output: %d columns x %d rows", width, len(lines))
	if width > 70 || len(lines) > 40 {
		t.Errorf("%d x %d does not fit the 72 x 42 popup", width, len(lines))
	}
}
