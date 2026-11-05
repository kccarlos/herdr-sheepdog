package procprio

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// NiceDelta is the positive niceness applied to worker and CLI sub-agent
// process groups on Unix so heavy builds yield CPU time to the voice daemon.
const NiceDelta = 10

// OOMScoreAdj is the Linux /proc/<pid>/oom_score_adj value applied to CLI
// sub-agents so the kernel reclaims a runaway child before the daemon.
const OOMScoreAdj = 500

// WrapArgv wraps argv for exec.Command on the current OS. On macOS it runs the
// child under QOS_CLASS_UTILITY via taskpolicy -c utility (never background
// -b, which pins builds to Apple Silicon efficiency cores).
func WrapArgv(argv []string) []string {
	return WrapArgvForOS(runtime.GOOS, exec.LookPath, argv)
}

// WrapArgvForOS wraps argv for exec.Command on goos using lookPath. If the
// target binary is missing, argv is returned unchanged so exec.Command.Start
// still reports a direct start error instead of running the wrapper and
// exiting 1.
func WrapArgvForOS(goos string, lookPath func(string) (string, error), argv []string) []string {
	if len(argv) == 0 || lookPath == nil {
		return argv
	}
	if goos != "darwin" {
		return argv
	}
	if _, err := lookPath(argv[0]); err != nil {
		return argv
	}
	tp, err := resolveTaskpolicy(lookPath)
	if err != nil {
		return argv
	}
	out := make([]string, 0, 3+len(argv))
	out = append(out, tp, "-c", "utility")
	out = append(out, argv...)
	return out
}

// ShellPrefix returns the shell words to prepend when launching an interactive
// worker command on the current OS.
func ShellPrefix(lookPath func(string) (string, error)) []string {
	return ShellPrefixForOS(runtime.GOOS, lookPath)
}

// ShellPrefixForOS returns the shell words to prepend when launching an
// interactive worker command on goos.
func ShellPrefixForOS(goos string, lookPath func(string) (string, error)) []string {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	var prefix []string
	if _, err := lookPath("nice"); err == nil {
		prefix = append(prefix, "nice", "-n", strconv.Itoa(NiceDelta))
	}
	switch goos {
	case "darwin":
		if _, err := lookPath("taskpolicy"); err == nil {
			prefix = append(prefix, "taskpolicy", "-c", "utility")
		} else if _, err := lookPath("/usr/sbin/taskpolicy"); err == nil {
			prefix = append(prefix, "/usr/sbin/taskpolicy", "-c", "utility")
		}
	case "linux":
		// -t ignores ioprio_set failures inside unprivileged containers so the
		// worker command still starts.
		if _, err := lookPath("ionice"); err == nil {
			prefix = append(prefix, "ionice", "-c", "2", "-n", "7", "-t")
		}
	}
	return prefix
}

func resolveTaskpolicy(lookPath func(string) (string, error)) (string, error) {
	if tp, err := lookPath("taskpolicy"); err == nil && tp != "" {
		return tp, nil
	}
	return lookPath("/usr/sbin/taskpolicy")
}

// UnwrapArgv strips leading priority and environment wrapper commands (nice,
// taskpolicy, ionice, choom, env) so callers inspecting process argv see the
// target program first.
func UnwrapArgv(argv []string) []string {
	for len(argv) > 0 {
		base := path.Base(argv[0])
		var next int
		switch base {
		case "nice":
			next = unwrapNice(argv)
		case "taskpolicy":
			next = unwrapFlagsWithArg(argv, "-c", "-t", "-p")
		case "ionice":
			next = unwrapFlagsWithArg(argv, "-c", "--class", "-n", "--classdata", "-p", "--pid", "-P", "--pgid", "-u", "--uid")
		case "choom":
			next = unwrapFlagsWithArg(argv, "-n", "--adjust", "-p", "--pid")
		case "env":
			next = unwrapEnv(argv)
		default:
			return argv
		}
		if next >= len(argv) {
			return nil
		}
		argv = argv[next:]
	}
	return nil
}

func unwrapNice(argv []string) int {
	i := 1
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			return i + 1
		}
		if a == "-n" || a == "--adjustment" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-n") || strings.HasPrefix(a, "--adjustment=") {
			i++
			continue
		}
		if len(a) > 1 && a[0] == '-' && (a[1] == '-' || a[1] == '+' || (a[1] >= '0' && a[1] <= '9')) {
			i++
			continue
		}
		break
	}
	return i
}

func unwrapFlagsWithArg(argv []string, withArg ...string) int {
	i := 1
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			return i + 1
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		step := 1
		for _, f := range withArg {
			if a == f {
				step = 2
				break
			}
			if strings.HasPrefix(f, "--") && strings.HasPrefix(a, f+"=") {
				step = 1
				break
			}
			if !strings.HasPrefix(f, "--") && len(f) == 2 && strings.HasPrefix(a, f) && len(a) > 2 {
				step = 1
				break
			}
		}
		i += step
	}
	return i
}

func unwrapEnv(argv []string) int {
	i := 1
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			return i + 1
		}
		if a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" || a == "-S" || a == "--split-string" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			i++
			continue
		}
		if strings.Contains(a, "=") {
			i++
			continue
		}
		break
	}
	return i
}

func writeOOMScoreAdj(procRoot string, pid int, score int) error {
	if pid <= 0 || procRoot == "" {
		return nil
	}
	p := filepath.Join(procRoot, strconv.Itoa(pid), "oom_score_adj")
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if cur, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && cur >= score {
		return nil
	}
	return os.WriteFile(p, []byte(strconv.Itoa(score)+"\n"), 0o644)
}
