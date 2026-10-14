package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

func fakePairAddrs(t *testing.T) {
	t.Helper()
	old := pairAddrs
	pairAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}, {Iface: "utun4", IP: net.ParseIP("100.101.102.103")}}, nil
	}
	t.Cleanup(func() { pairAddrs = old })
}

// The code lists both addresses; the token is in the code, never in the
// text.
func TestPair(t *testing.T) {
	fakePairAddrs(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	const token = "pairing-token-not-to-be-printed-0123456789"
	if err := os.WriteFile(cfg, []byte("[web]\nbind = \"0.0.0.0\"\nport = 7777\ntoken = \""+token+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetWebEnv(t)
	var out, errb strings.Builder
	if code := run(context.Background(), []string{"pair", "--config", cfg, "--env-file", filepath.Join(dir, "none.env")}, &out, &errb); code == 0 {
		t.Fatal("a missing --env-file was accepted")
	}
	env := filepath.Join(dir, "x.env")
	_ = os.WriteFile(env, nil, 0o600)
	out.Reset()
	if code := run(context.Background(), []string{"pair", "--config", cfg, "--env-file", env}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	text := out.String()
	for _, want := range []string{"lan       http://192.168.1.20:7777", "tailscale http://100.101.102.103:7777", "not printed", "▀"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, token) || strings.Contains(errb.String(), token) {
		t.Error("the token was printed")
	}
}

func TestPairLoopback(t *testing.T) {
	fakePairAddrs(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfg, []byte("[web]\nbind = \"127.0.0.1\"\n"), 0o600)
	env := filepath.Join(dir, "x.env")
	_ = os.WriteFile(env, nil, 0o600)
	unsetWebEnv(t)
	var out, errb strings.Builder
	if code := run(context.Background(), []string{"pair", "--config", cfg, "--env-file", env}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "web.bind") {
		t.Errorf("exit %d: %q", code, errb.String())
	}
}

// unsetWebEnv clears the web overrides for the test (an empty value
// still counts as set).
func unsetWebEnv(t *testing.T) {
	for _, k := range []string{"SHEEPDOG_WEB_TOKEN", "SHEEPDOG_WEB_BIND", "SHEEPDOG_WEB_PORT"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// The herdr plugin opens `pair --compact` in a popup of 72 columns by 42
// rows (herdr-plugin.toml), less 2 each for the frame, and its wrapper
// adds 2 rows (a blank line and the "press Enter" prompt). With a long
// token and three addresses the output must still fit, the code included.
func TestPairCompactFitsThePluginPopup(t *testing.T) {
	old := pairAddrs
	pairAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{
			{Iface: "en0", IP: net.ParseIP("192.168.100.200")},
			{Iface: "utun4", IP: net.ParseIP("100.101.102.103")},
			{Iface: "en1", IP: net.ParseIP("10.20.30.40")},
		}, nil
	}
	t.Cleanup(func() { pairAddrs = old })
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	token := strings.Repeat("0123456789abcdef", 4)
	if err := os.WriteFile(cfg, []byte("[web]\nbind = \"0.0.0.0\"\nport = 7777\ntoken = \""+token+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetWebEnv(t)
	env := filepath.Join(dir, "x.env")
	_ = os.WriteFile(env, nil, 0o600)
	var out, errb strings.Builder
	if code := run(context.Background(), []string{"pair", "--compact", "--plain", "--config", cfg, "--env-file", env}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), token) {
		t.Error("the token was printed")
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	width := 0
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > width {
			width = n
		}
	}
	t.Logf("compact pair output: %d columns x %d rows", width, len(lines))
	if width > 70 || len(lines) > 36 {
		t.Errorf("%d x %d does not fit the 72 x 42 popup", width, len(lines))
	}
}
