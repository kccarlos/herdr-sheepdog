package main

// The daemon's user-facing lifecycle: sheepdog start, stop, restart,
// status, logs and open. Each works whether or not the login service is
// installed: with it, start and stop go through launchd / systemd (whose
// KeepAlive / Restart would bring back a daemon that was only signalled);
// without it, start runs `serve --background` as a detached process and
// stop signals the pid in <data_dir>/sheepdog.pid.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/service"
)

// lifecycle is the loaded setup a lifecycle command works on.
type lifecycle struct {
	cfg        config.Config
	configPath string // as given ("" = default), passed on to serve
	envFile    string // as given ("" = default), passed on to serve
	mgr        service.Manager
	out, errw  io.Writer
}

// lifecycleFlags adds --config and --env-file to fs.
func lifecycleFlags(fs *flag.FlagSet) (configPath, envFile *string) {
	return fs.String("config", "", "config file"), fs.String("env-file", "", "secret file")
}

// loadLifecycle loads the env file and config the daemon would use.
func loadLifecycle(ctx context.Context, name, configPath, envFile string, stdout, stderr io.Writer) (*lifecycle, int, bool) {
	if code, ok := loadServeEnvFile(envFile, stderr); !ok {
		return nil, code, false
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fprintf(stderr, "sheepdog %s: %v\n", name, err)
		return nil, 1, false
	}
	l := &lifecycle{cfg: cfg, configPath: configPath, envFile: envFile, out: stdout, errw: stderr}
	if spec, err := serviceSpecFor(cfg, configPath, envFile, ""); err == nil {
		l.mgr = service.Manager{Spec: spec, Run: serviceRunner}
	}
	_ = ctx
	return l, 0, true
}

// serviceInstalled reports whether the login service's unit file exists.
func (l *lifecycle) serviceInstalled() bool { return l.mgr.Run != nil && l.mgr.Installed() }

// url is the daemon's local base URL from config.
func (l *lifecycle) url() string { return daemon.LocalURL(l.cfg.Web.Bind, l.cfg.Web.Port) }

// running is the running daemon: from the pid file, or (an older
// sheepdog that writes none, or one on another data dir) whatever
// sheepdog answers on the configured port. pidKnown is false then.
func (l *lifecycle) running(ctx context.Context) (info daemon.Info, pidKnown, ok bool) {
	if info, ok := daemon.Running(l.cfg.DataDir); ok {
		if info.URL == "" {
			info.URL = l.url()
		}
		return info, true, true
	}
	if st, h := daemon.ProbePort(ctx, l.cfg.Web.Bind, l.cfg.Web.Port); st == daemon.PortSheepdog {
		return daemon.Info{URL: l.url(), Version: h.Version}, false, true
	}
	return daemon.Info{}, false, false
}

func alreadyRunning(info daemon.Info, pidKnown bool) string {
	if pidKnown {
		return (&daemon.ErrRunning{Info: info}).Error()
	}
	return fmt.Sprintf("sheepdog is already running (%s; no pid file: started by an older sheepdog or with another --data-dir)", info.URL)
}

func printStartUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog start [--config PATH] [--env-file PATH]")
	fprintln(w, "")
	fprintln(w, "Starts the daemon in the background: through the login service when it is")
	fprintln(w, "installed (sheepdog service install), otherwise as a detached process that")
	fprintln(w, "writes its pid to <data_dir>/sheepdog.pid. Waits until it answers, then prints")
	fprintln(w, "the voice page's URL. Does nothing when it is already running.")
}

// startTimeout bounds the wait for a started daemon to answer.
var startTimeout = 20 * time.Second

// runStart implements `sheepdog start`.
func runStart(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printStartUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	l, code, ok := loadLifecycle(ctx, "start", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	return l.start(ctx)
}

func (l *lifecycle) start(ctx context.Context) int {
	if info, pidKnown, ok := l.running(ctx); ok {
		fprintln(l.out, alreadyRunning(info, pidKnown))
		return 0
	}
	if msg := portBusyMessage(ctx, l.cfg, l.cfg.Web.Port); msg != "" {
		fprintln(l.errw, msg)
		return 1
	}
	if err := app.Check(l.cfg, app.Options{}); err != nil {
		fprintf(l.errw, "sheepdog cannot start: %s\n", strings.TrimSuffix(strings.TrimPrefix(err.Error(), "app: "), "; use --voice=off to run without it"))
		fprintln(l.errw, "Run `sheepdog setup` to set it up, or `sheepdog doctor` to see what is missing.")
		return 1
	}
	stdio, logFile := daemon.StdioFile(l.cfg.DataDir), daemon.LogFile(l.cfg.DataDir)
	from, logFrom := fileSize(stdio), fileSize(logFile)
	var exited <-chan error
	how := "in the background"
	if l.serviceInstalled() {
		if _, _, err := l.mgr.Start(ctx); err != nil {
			fprintf(l.errw, "sheepdog start: the login service did not start: %v\n", err)
			return 1
		}
		how = "by the login service"
	} else {
		ch, err := spawnDetached(l.mgr.Spec.Args(), stdio, l.mgr.Spec.Home)
		if err != nil {
			fprintf(l.errw, "sheepdog start: %v\n", err)
			return 1
		}
		exited = ch
	}
	info, err := l.waitUp(ctx, exited)
	if err != nil {
		fprintf(l.errw, "sheepdog did not start: %v\n", err)
		tail := newOutput(stdio, from)
		if t := newOutput(logFile, logFrom); t != "" {
			tail = strings.TrimSpace(tail + "\n" + tailOf(t, 15))
		}
		if tail != "" {
			fprintf(l.errw, "\nIt printed:\n%s\n", tail)
		}
		fprintf(l.errw, "\nThe log: %s (sheepdog logs)\n", logFile)
		return 1
	}
	fprintf(l.out, "sheepdog started %s (pid %d)\n", how, info.PID)
	fprintf(l.out, "  voice page  %s/voice\n", info.URL)
	fprintln(l.out, "  logs        sheepdog logs -f")
	if onboard.CheckVoice(l.cfg).Status == onboard.Fail {
		fprintf(l.out, "  setup       no voice key yet, so the voice is off: run `sheepdog setup` (%s/setup)\n", info.URL)
	}
	return 0
}

// waitUp waits until the daemon holds its lock, has written its URL and
// answers /api/hello, or exited reports that the detached process died.
func (l *lifecycle) waitUp(ctx context.Context, exited <-chan error) (daemon.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	for {
		if info, ok := daemon.Running(l.cfg.DataDir); ok && info.URL != "" {
			if _, err := daemon.GetHello(ctx, info.URL); err == nil {
				return info, nil
			}
		}
		select {
		case err := <-exited:
			if err == nil {
				err = errors.New("it exited")
			}
			return daemon.Info{}, fmt.Errorf("it exited (%v)", err)
		case <-ctx.Done():
			return daemon.Info{}, fmt.Errorf("it did not answer within %s", startTimeout)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// spawnDetached starts argv in a new session with stdin from /dev/null
// and stdout/stderr appended to stdio, and reports its exit on the
// channel. Tests replace it.
var spawnDetached = func(argv []string, stdio, dir string) (<-chan error, error) {
	if err := os.MkdirAll(filepath.Dir(stdio), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(stdio, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = f, f
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return ch, nil
}

// tailOf is the last n lines of s.
func tailOf(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		if len(l) > 300 {
			lines[i] = l[:300] + "…"
		}
	}
	return strings.Join(lines, "\n")
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// newOutput is what path gained after offset from (at most 4 KB, the end).
func newOutput(path string, from int64) string {
	b, err := os.ReadFile(path)
	if err != nil || int64(len(b)) <= from {
		return ""
	}
	b = b[from:]
	if len(b) > 4096 {
		b = b[len(b)-4096:]
	}
	return strings.TrimSpace(string(b))
}

func printStopUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog stop [--config PATH] [--env-file PATH]")
	fprintln(w, "")
	fprintln(w, "Stops the daemon: unloads the login service when it is installed (it starts")
	fprintln(w, "again at the next login or with sheepdog start), otherwise sends SIGTERM to the")
	fprintln(w, "pid in <data_dir>/sheepdog.pid and waits for a clean shutdown.")
}

// stopTimeout bounds the wait for a clean shutdown before SIGKILL.
var stopTimeout = 10 * time.Second

// runStop implements `sheepdog stop`.
func runStop(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printStopUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	l, code, ok := loadLifecycle(ctx, "stop", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	return l.stop(ctx)
}

// stop stops the daemon and returns the exit code.
func (l *lifecycle) stop(ctx context.Context) int {
	info, pidKnown, running := l.running(ctx)
	if l.serviceInstalled() {
		if _, err := l.mgr.Stop(ctx); err != nil {
			fprintf(l.errw, "sheepdog stop: %v\n", err)
			return 1
		}
		if running && pidKnown {
			if !waitGone(ctx, l.cfg.DataDir, info.PID, stopTimeout) {
				fprintf(l.errw, "sheepdog stop: the login service was unloaded but pid %d is still running\n", info.PID)
				return 1
			}
		}
		if running {
			fprintln(l.out, "sheepdog stopped (login service unloaded; it starts again at login or with sheepdog start)")
			return 0
		}
	}
	switch {
	case !running:
		fprintln(l.out, "sheepdog is not running")
		return 0
	case !pidKnown:
		fprintf(l.errw, "a sheepdog is running at %s but left no pid file (an older version, or started with another --data-dir).\n", info.URL)
		fprintln(l.errw, "Stop it where it was started (Ctrl-C in its terminal), or find its pid with:")
		fprintf(l.errw, "  lsof -nP -iTCP:%d -sTCP:LISTEN\n", l.cfg.Web.Port)
		return 1
	}
	if err := syscall.Kill(info.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		fprintf(l.errw, "sheepdog stop: signal pid %d: %v\n", info.PID, err)
		return 1
	}
	if !waitGone(ctx, l.cfg.DataDir, info.PID, stopTimeout) {
		fprintf(l.errw, "sheepdog stop: pid %d did not exit within %s; killing it\n", info.PID, stopTimeout)
		_ = syscall.Kill(info.PID, syscall.SIGKILL)
		waitGone(ctx, l.cfg.DataDir, info.PID, 2*time.Second)
	}
	fprintf(l.out, "sheepdog stopped (pid %d)\n", info.PID)
	return 0
}

// waitGone waits until pid has released dataDir's lock and exited.
func waitGone(ctx context.Context, dataDir string, pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, held := daemon.Running(dataDir); !held && !daemon.Alive(pid) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

// runRestart implements `sheepdog restart`: stop (when running), start.
func runRestart(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(stderr, "usage: sheepdog restart [--config PATH] [--env-file PATH]")
		fprintln(stderr, "")
		fprintln(stderr, "Stops the daemon when it runs, then starts it (sheepdog stop, sheepdog start).")
	}
	configPath, envFile := lifecycleFlags(fs)
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	l, code, ok := loadLifecycle(ctx, "restart", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	if code := l.stop(ctx); code != 0 {
		return code
	}
	return l.start(ctx)
}

// flagExit maps a flag parse error to an exit code (help: 0).
func flagExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// openBrowser opens url in the user's browser (tests replace it).
var openBrowser = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, url).Start()
}

// pages are what `sheepdog open` opens.
var pages = map[string]string{"voice": "/voice", "setup": "/setup", "debug": "/debug"}

// runOpen implements `sheepdog open [voice|setup|debug]`.
func runOpen(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(stderr, "usage: sheepdog open [voice|setup|debug] [--config PATH]")
		fprintln(stderr, "")
		fprintln(stderr, "Opens a page of the running daemon in your browser (default: voice).")
	}
	configPath, envFile := lifecycleFlags(fs)
	page := "voice"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		page, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	path, known := pages[page]
	if !known {
		fprintf(stderr, "sheepdog open: unknown page %q (want voice, setup or debug)\n", page)
		return 2
	}
	l, code, ok := loadLifecycle(ctx, "open", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	info, _, running := l.running(ctx)
	if !running {
		fprintln(stderr, "sheepdog is not running. Start it with: sheepdog start")
		return 1
	}
	url := info.URL + path
	if page == "setup" && pageMissing(ctx, url) {
		fprintln(stderr, "This version of sheepdog has no setup page yet. Run `sheepdog init` in a terminal to set it up.")
		return 1
	}
	fprintf(stdout, "opening %s\n", url)
	if l.cfg.Web.Token != "" {
		fprintln(stdout, "The page asks once for the web token: web.token in your config, or SHEEPDOG_WEB_TOKEN in "+config.DefaultEnvPath()+".")
	}
	if err := openBrowser(url); err != nil {
		fprintf(stderr, "sheepdog open: could not start a browser (%v); open the URL above yourself\n", err)
		return 1
	}
	return 0
}

// pageMissing reports whether GET url answers 404.
func pageMissing(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return true
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusNotFound
}

// getJSON fetches base+path with the web token into v.
func getJSON(ctx context.Context, base, path, token string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
