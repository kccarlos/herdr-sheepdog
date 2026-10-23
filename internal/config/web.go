package config

import (
	"net"
)

// Web configures the HTTP server (plan §3.8). It binds to Bind:Port,
// loopback only by default; a non-loopback Bind (a LAN or Tailscale
// address, or 0.0.0.0 for every interface) is how a phone reaches the
// daemon. Off this computer it serves TLS only, and only to paired
// devices (docs/dev/design/security.md).
type Web struct {
	// Bind is the listen address (default 127.0.0.1).
	Bind string `toml:"bind"`
	// Port is the TCP port (default 7777).
	Port int `toml:"port"`
	// Dev enables developer-only routes: /testdata/<name>.wav serves the
	// audio fixtures so the /voice page can use one as a fake microphone
	// (WP6 file-source test mode). Off by default; `serve --dev` sets it.
	Dev bool `toml:"dev"`
	// DevTestdataDir is where /testdata/ reads from when Dev is on
	// (default testdata/audio relative to the working directory).
	DevTestdataDir string `toml:"dev_testdata_dir"`
	// Shell lets paired devices open the phone shell and send
	// attachments. On by default; the setup page switches it.
	Shell bool `toml:"shell"`
	// Advertise lists more host:port addresses for the pairing code,
	// after the daemon's own: a router's port forward, which the daemon
	// cannot see (docs/remote-access.md).
	Advertise []string `toml:"advertise"`
	// TailscaleHTTPS serves a real Let's Encrypt certificate, fetched
	// with `tailscale cert`, to browsers that open the computer's
	// *.ts.net name. Off by default: turning it on logs the name
	// publicly (certificate transparency). The app never uses it; it
	// pins the daemon's own key. The setup page switches it.
	TailscaleHTTPS bool `toml:"tailscale_https"`
}

// IsLoopback reports whether Bind reaches only this machine (127.0.0.1,
// ::1, or the hostname "localhost"): no phone can reach it.
func (w Web) IsLoopback() bool {
	if w.Bind == "localhost" {
		return true
	}
	ip := net.ParseIP(w.Bind)
	return ip != nil && ip.IsLoopback()
}

// DefaultDevTestdataDir is the directory /testdata/ serves in dev mode
// when web.dev_testdata_dir is empty: the repo's audio fixtures.
const DefaultDevTestdataDir = "testdata/audio"

// DefaultWebPort is the port the web server listens on by default.
const DefaultWebPort = 7777

// DefaultWebBind is the web.bind built-in: loopback only.
const DefaultWebBind = "127.0.0.1"
