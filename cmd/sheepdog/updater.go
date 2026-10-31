package main

// The update notice and the restart after an upgrade (docs/dev/update.md):
// the daemon's goroutines (the daily check, the executable watcher with
// its idle gate) and the CLI's notices.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/service"
	"github.com/kccarlos/herdr-sheepdog/internal/update"
)

// exitUpdateRestart is the exit code of a daemon that stops to let the
// login service start the new executable. launchd (KeepAlive) restarts on
// any exit; systemd's Restart=on-failure needs a non-zero one. 75 is
// EX_TEMPFAIL.
const exitUpdateRestart = 75

var (
	// updateStartDelay is the wait before the first check after startup.
	updateStartDelay = 30 * time.Second
	// updateTick is how often the watcher looks for a new executable and
	// whether the daemon is idle.
	updateTick = time.Minute
	// execSelf replaces this process with the new executable (tests
	// replace it).
	execSelf = syscall.Exec
	// underService reports whether the login service (launchd, systemd)
	// started this daemon, so that exiting makes it start the new one.
	underService = func() bool {
		switch runtime.GOOS {
		case "darwin":
			return os.Getenv("XPC_SERVICE_NAME") == service.Label
		case "linux":
			return os.Getenv("INVOCATION_ID") != ""
		}
		return false
	}
)

// installHow is the upgrade command for how this binary was installed.
func installHow() string {
	exe, _ := osExecutable()
	return upgradeCommand(detectInstall(exe))
}

// startUpdateWatchers runs the daily check (when update.check is on) and
// the executable watcher until ctx ends. idle returns "" when nobody
// uses the daemon; trigger starts the restart.
func startUpdateWatchers(ctx context.Context, cfg config.Config, tr *update.Tracker, idle func() string, trigger func(from, to string) error, logf func(string, ...any)) {
	if cfg.Update.Check {
		go update.Run(ctx, tr, updateStartDelay, func(c context.Context) (release, error) { return latestRelease(c) }, time.Now, logf)
	}
	exe, err := osExecutable()
	if err != nil {
		return
	}
	launch := update.LaunchPath(os.Args[0], exe, exec.LookPath)
	w, err := update.NewWatch(launch)
	if err != nil {
		logf("update: cannot watch %s for an upgrade: %v", launch, err)
		return
	}
	r := &update.Restarter{
		Watch: w, Tracker: tr, Auto: cfg.Update.AutoRestart,
		Running:   versionString(version, commit, date),
		Probe:     update.ProbeVersion,
		Idle:      idle,
		Attempted: func(to string) bool { return update.Attempted(cfg.DataDir, to, time.Now()) },
		Trigger:   trigger,
		Interval:  updateTick,
		Logf:      logf,
	}
	go r.Run(ctx)
}

// updateRestart is what restartForUpdate needs from runServe.
type updateRestart struct {
	cfg      config.Config
	args     []string // runServe's arguments (after "serve")
	envFile  string   // absolute path of the env file this run loaded ("" = none)
	baseEnv  []string // the process environment before the env file
	from, to string   // the two `sheepdog version` lines
	stdout   io.Writer
	stderr   io.Writer
	flush    func()
	stopDemo func()
	release  func() // release the single-instance lock
}

// restartForUpdate ends this daemon in favour of the new executable and
// returns only when the daemon should carry on: resumed is true when the
// exec failed (to is then the version that could not start). Otherwise
// code is runServe's exit code (exitUpdateRestart under the login
// service). The caller has stopped the app (Run returned) and its port is
// free.
//
//   - Under the login service the daemon exits non-zero after a clean
//     shutdown; launchd (KeepAlive) and systemd (Restart=on-failure) start
//     the executable at the service's own path.
//   - Otherwise (sheepdog start, or serve in a terminal) it releases the
//     lock and re-executes the new executable in place with the same
//     arguments: same pid, so the pid file the new daemon writes is right.
//
// daemon.restart (old and new versions, and via exec or service) is logged
// when the restart is asked for, while the event log is still open.
//
// Either way update-restart.json records the target, so the next daemon
// never restarts into the same executable again.
func restartForUpdate(r updateRestart) (to string, code int, resumed bool) {
	fromV, toV := update.VersionOf(r.from), update.VersionOf(r.to)
	_ = update.WriteRecord(r.cfg.DataDir, update.Record{From: r.from, To: r.to, At: time.Now().UTC()})
	if underService() {
		fprintf(r.stdout, "sheepdog %s is installed (running %s); exiting so the login service starts it\n", toV, fromV)
		r.stopDemo()
		return "", exitUpdateRestart, false
	}
	exe, err := osExecutable()
	if err != nil {
		exe = ""
	}
	launch := update.LaunchPath(os.Args[0], exe, exec.LookPath)
	fprintf(r.stdout, "sheepdog %s is installed (running %s); restarting into it\n", toV, fromV)
	r.stopDemo()
	r.release()
	r.flush()
	restoreEnv(r.baseEnv)
	argv := append([]string{launch, "serve"}, argsWithEnvFile(r.args, r.envFile)...)
	err = execSelf(launch, argv, os.Environ())
	// Only reached when exec failed.
	fprintf(r.stderr, "sheepdog: could not start %s (%v); staying on %s\n", launch, err, fromV)
	return toV, 0, true
}

// argsWithEnvFile is serve's arguments with --env-file envFile in front,
// so a re-exec reads the file this run loaded and not whatever ./.env the
// working directory offers. An --env-file already in args wins.
func argsWithEnvFile(args []string, envFile string) []string {
	if envFile == "" || flagValue(args, "env-file") != "" {
		return args
	}
	return append([]string{"--env-file", envFile}, args...)
}

// noticeCommands are the commands that end with the update notices.
var noticeCommands = map[string]bool{"setup": true, "init": true, "doctor": true, "pair": true, "start": true, "open": true}

// flagValue is the value of --name in args ("" when absent): a loose scan,
// enough to find --config and --env-file again after the command ran.
func flagValue(args []string, name string) string {
	for i, a := range args {
		a = strings.TrimLeft(a, "-")
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

// cliNotices prints, after a command, the lines about a daemon running an
// older sheepdog than the installed one and about a newer release.
func cliNotices(ctx context.Context, args []string, stderr io.Writer) {
	if len(args) == 0 || !noticeCommands[args[0]] {
		return
	}
	l, _, ok := loadLifecycle(ctx, args[0], flagValue(args, "config"), flagValue(args, "env-file"), io.Discard, io.Discard)
	if !ok {
		return
	}
	printCLINotices(ctx, l, stderr, time.Now())
}

// printCLINotices writes the restart line every time it applies and the
// update line at most once a day.
func printCLINotices(ctx context.Context, l *lifecycle, w io.Writer, now time.Time) {
	if info, _, ok := l.running(ctx); ok {
		if h, err := daemon.GetHello(ctx, info.URL); err == nil && h.Version != "" && h.Version != version {
			fprintf(w, "The running sheepdog is %s; %s is installed. Run `sheepdog restart` to use it.\n", h.Version, version)
		}
	}
	if !l.cfg.Update.Check {
		return
	}
	c, _ := update.ReadCache(l.cfg.DataDir)
	st := update.Compute(version, c, installHow())
	if !st.Available || !update.NoticeDue(l.cfg.DataDir, now) {
		return
	}
	fprintf(w, "sheepdog %s is available (you have %s). To update: %s, then sheepdog restart\n", st.Latest, st.Current, st.How)
	update.NoticeShown(l.cfg.DataDir, now)
}
