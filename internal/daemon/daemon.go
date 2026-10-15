// Package daemon holds the running daemon's on-disk footprint and the
// single-instance rule: where its runtime log and pid file live, the lock
// that keeps a second `sheepdog serve` from starting on the same data
// directory, and the probe that tells "sheepdog is already running" from
// "another program has the port".
//
// Every path is derived from config data_dir, so a scratch daemon with
// its own --data-dir (bench, eval) has its own lock, log and pid file.
//
//	<data_dir>/sheepdog.pid          JSON {pid, url, version, started_at}; flock'd while the daemon runs
//	<data_dir>/logs/sheepdog.log     the runtime log (rotated: sheepdog.log.1 …)
//	<data_dir>/logs/stdio.log        what a background daemon printed before its log opened, and crashes
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// LogDir is the directory of the runtime log.
func LogDir(dataDir string) string { return filepath.Join(dataDir, "logs") }

// LogFile is the runtime log (`sheepdog logs`).
func LogFile(dataDir string) string { return filepath.Join(LogDir(dataDir), "sheepdog.log") }

// StdioFile receives a background daemon's stdout and stderr: lines
// printed before the runtime log is open (a config error) and a Go
// panic. Normally empty.
func StdioFile(dataDir string) string { return filepath.Join(LogDir(dataDir), "stdio.log") }

// PidFile is the running daemon's pid file.
func PidFile(dataDir string) string { return filepath.Join(dataDir, "sheepdog.pid") }

// Info is the pid file's content.
type Info struct {
	PID       int       `json:"pid"`
	URL       string    `json:"url,omitempty"`
	Version   string    `json:"version,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// Background is true for a daemon started by `sheepdog start` or the
	// login service (serve --background).
	Background bool `json:"background,omitempty"`
}

// Lock is the held single-instance lock (the pid file, flock'd).
type Lock struct {
	f    *os.File
	path string
}

// ErrRunning is returned by Acquire when another daemon holds the lock.
type ErrRunning struct {
	Info Info
}

func (e *ErrRunning) Error() string {
	if e.Info.PID == 0 {
		return "sheepdog is already running"
	}
	if e.Info.URL == "" {
		return fmt.Sprintf("sheepdog is already running (pid %d)", e.Info.PID)
	}
	return fmt.Sprintf("sheepdog is already running (pid %d, %s)", e.Info.PID, e.Info.URL)
}

// Acquire takes the single-instance lock of dataDir. Another live daemon
// on the same data dir makes it return *ErrRunning with that daemon's
// Info. A crashed daemon's lock is released by the kernel, so a stale pid
// file never blocks a start.
func Acquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := PidFile(dataDir)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			info, _ := readInfo(path)
			return nil, &ErrRunning{Info: info}
		}
		return nil, err
	}
	return &Lock{f: f, path: path}, nil
}

// Write records info in the pid file.
func (l *Lock) Write(info Info) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err = l.f.WriteAt(append(b, '\n'), 0)
	return err
}

// Release empties the pid file and drops the lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(0)
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func readInfo(path string) (Info, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if len(strings.TrimSpace(string(b))) == 0 {
		return info, nil
	}
	if err := json.Unmarshal(b, &info); err != nil {
		// An older pid file holding just the number.
		if n, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
			return Info{PID: n}, nil
		}
		return Info{}, err
	}
	return info, nil
}

// Running reports whether a daemon holds dataDir's lock, and its Info.
// It never takes the lock for longer than the check.
func Running(dataDir string) (Info, bool) {
	path := PidFile(dataDir)
	f, err := os.Open(path)
	if err != nil {
		return Info{}, false
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		info, _ := readInfo(path)
		return info, true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return Info{}, false
}

// Hello is the part of GET /api/hello a probe reads.
type Hello struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	VoiceProvider string `json:"voice_provider"`
	VoiceModel    string `json:"voice_model"`
}

// PortState is what listens on the daemon's port.
type PortState int

const (
	// PortFree: nothing listens.
	PortFree PortState = iota
	// PortSheepdog: a sheepdog daemon answers /api/hello.
	PortSheepdog
	// PortOther: something else listens.
	PortOther
)

// ProbePort reports what listens on host:port: nothing, a sheepdog (with
// its hello), or another program. host "" or unspecified (0.0.0.0, ::)
// is probed on 127.0.0.1.
func ProbePort(ctx context.Context, host string, port int) (PortState, Hello) {
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return PortFree, Hello{}
	}
	_ = conn.Close()
	h, err := GetHello(ctx, "http://"+addr)
	if err != nil || h.Name != "sheepdog" {
		return PortOther, Hello{}
	}
	return PortSheepdog, h
}

// GetHello fetches base/api/hello (public, no token).
func GetHello(ctx context.Context, base string) (Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/hello", nil)
	if err != nil {
		return Hello{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Hello{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Hello{}, fmt.Errorf("/api/hello: %s", resp.Status)
	}
	var h Hello
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return Hello{}, err
	}
	return h, nil
}

// LocalURL is the URL the CLI reaches a daemon on for web.bind and port.
func LocalURL(bind string, port int) string {
	host := bind
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// Alive reports whether a process with pid exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
