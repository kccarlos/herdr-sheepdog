package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// factoryDoctorRunner is overridden in CLI tests so no test touches the real
// machine's PATH, CLIs, herdr-core socket or ~/.config/sheepdog.
var factoryDoctorRunner = func(ctx context.Context, o factory.DoctorOptions) []onboard.Result {
	return append(factory.Doctor(ctx, o), factory.StartChecks(ctx, o)...)
}

// factoryStartChecks is overridden in CLI tests for the same reason.
var factoryStartChecks = factory.StartChecks

func printFactoryUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog factory <start|stop|status|log|plan|task|recap|report|doctor> [flags] [folder]")
	fprintln(w, "  start <folder> --goal \"<sentence>\" [--max-running N]   start the factory coordinator for a repo")
	fprintln(w, "  stop [<folder>]                      stop a started factory project and its runs")
	fprintln(w, "  status [<folder>]                    one line per task: id, title, stage, last logged rule, age and why")
	fprintln(w, "  log [<folder>] [-n N]                each engine step: time, task, rule, asked, decision, why")
	fprintln(w, "  plan apply <file>                    (coordinator only) apply a JSON plan")
	fprintln(w, "  plan amend <file>                    (coordinator only) change tasks' title, brief, outcomes, estimate_lines or add tasks, with a why")
	fprintln(w, "  task <park|unpark|drop|close|send-back|retry|say> <task> --why \"<why>\"  (coordinator only)")
	fprintln(w, "  recap decide <task> <choice> --why \"<why>\" [--child '<json>' ...]  (coordinator only) decide an open recap")
	fprintln(w, "  report ready [--proof \"<outcome>=<cmd>\" ...] [--artifact \"<outcome>=<path>\" ...] [--touched \"<places>\"] [--skipped \"<what was left out>\"] [--reject \"<why>\"]  (worker only) run proofs and report HEAD ready, or reject findings")
	fprintln(w, "  report ready --nothing-to-change --why \"<why>\"  (worker only) report that the task needs no change")
	fprintln(w, "  doctor                               check unattended factory prerequisites:")
	fprintln(w, "           herdr-core (hc), timed project-volume probe inside herdr-core")
	fprintln(w, "           (macOS consent prompts and external-disk stalls), at least one")
	fprintln(w, "           logged-in agent CLI (claude, codex or agy) plus a canary start")
	fprintln(w, "           inside herdr-core, gh login and matching origin fetch and push URLs, and")
	fprintln(w, "           codexbar. Exit 1 on FAIL.")
	fprintln(w, "flags: --goal TEXT, --max-running N, --proof OUTCOME=CMD, --artifact OUTCOME=PATH, --touched TEXT, --skipped TEXT, --reject WHY, --nothing-to-change, --why TEXT, --child JSON, --folder PATH, --config PATH, --hc-socket PATH, --no-canary")
}

// runFactory dispatches the `sheepdog factory` command group.
func runFactory(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printFactoryUsage(stderr)
		return 2
	}
	switch args[0] {
	case "start", "stop":
		return runFactoryCmd(ctx, args[0], args[1:], stdout, stderr)
	case "status":
		return runFactoryStatus(ctx, args[1:], stdout, stderr)
	case "log":
		return runFactoryLog(ctx, args[1:], stdout, stderr)
	case "plan":
		return runFactoryPlan(ctx, args[1:], stdout, stderr)
	case "recap":
		return runFactoryRecap(ctx, args[1:], stdout, stderr)
	case "task":
		return runFactoryTask(ctx, args[1:], stdout, stderr)
	case "report":
		return runFactoryReport(ctx, args[1:], stdout, stderr)
	case "doctor":
		return runFactoryDoctor(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printFactoryUsage(stdout)
		return 0
	default:
		fprintf(stderr, "sheepdog factory: unknown subcommand %q\n\n", args[0])
		printFactoryUsage(stderr)
		return 2
	}
}

func runFactoryCmd(ctx context.Context, sub string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	configPath, goal, maxRunning := fs.String("config", "", "config file"), fs.String("goal", "", "project goal"), fs.Int("max-running", 0, "maximum concurrent tasks")
	if err := fs.Parse(reorderFactoryDoctorArgs(args)); err != nil {
		return flagExit(err)
	}
	maxSet := false
	fs.Visit(func(f *flag.Flag) { maxSet = maxSet || f.Name == "max-running" })
	if (sub == "start" && (fs.NArg() != 1 || strings.TrimSpace(*goal) == "")) || (sub == "stop" && (fs.NArg() > 1 || *goal != "" || maxSet)) {
		printFactoryUsage(stderr)
		return 2
	}
	if maxSet && (*maxRunning < flow.MaxRunningMin || *maxRunning > flow.MaxRunningMax) {
		fprintf(stderr, "sheepdog factory start: max_running %d: allowed %d–%d\n", *maxRunning, flow.MaxRunningMin, flow.MaxRunningMax)
		return 1
	}
	folder := ""
	if fs.NArg() == 1 {
		home, _ := os.UserHomeDir()
		folder = resolveTargetFolder(fs.Arg(0), home)
	}
	cfg, err := config.Load(*configPath)
	if sub == "start" {
		if err != nil {
			fprintf(stderr, "sheepdog factory start: warning: could not read project settings (%v); codex check skipped\n", err)
		}
		var failed []string
		for _, r := range factoryStartChecks(ctx, factory.DoctorOptions{Folder: folder, DataDir: cfg.DataDir, HCSocket: cfg.Herdr.HCSocketPath()}) {
			if r.Status == onboard.Fail {
				failed = append(failed, r.Line())
			}
		}
		if len(failed) > 0 {
			fprintf(stderr, "sheepdog factory start: not starting; fix these first (sheepdog factory doctor %s shows every check):\n%s\n", folder, strings.Join(failed, "\n"))
			return 1
		}
	}
	if err == nil {
		body := map[string]any{"folder": folder, "project": folder, "goal": strings.TrimSpace(*goal), "token": os.Getenv(runTokenEnv), "max_running": *maxRunning}
		err = postJSON(ctx, daemon.LocalURL(cfg.Web.Bind, cfg.Web.Port), "/api/factory/"+sub, body)
	}
	if err != nil {
		fprintf(stderr, "sheepdog factory %s: %v\n", sub, err)
		return 1
	}
	if sub == "start" {
		fprintf(stdout, "factory started for %s\n", folder)
	} else if folder != "" {
		fprintf(stdout, "factory stopped for %s\n", folder)
	} else {
		fprintln(stdout, "factory stopped")
	}
	return 0
}

// runFactoryStatus prints one line per task from the running daemon (including
// unpersisted in-memory alerts) or falls back to stored state and log.jsonl
// when the daemon is stopped.
func runFactoryStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	if err := fs.Parse(reorderFactoryDoctorArgs(args)); err != nil {
		return flagExit(err)
	}
	if fs.NArg() > 1 {
		printFactoryUsage(stderr)
		return 2
	}
	folder := ""
	if fs.NArg() == 1 {
		home, _ := os.UserHomeDir()
		folder = resolveTargetFolder(fs.Arg(0), home)
	}
	cfg, err := config.Load(*configPath)
	var tasks []factory.TaskStatus
	if err == nil {
		var resp struct{ Tasks []factory.TaskStatus }
		if getJSON(ctx, daemon.LocalURL(cfg.Web.Bind, cfg.Web.Port), "/api/factory/status?"+url.Values{"folder": {folder}, "data_dir": {cfg.DataDir}}.Encode(), &resp) == nil {
			tasks = resp.Tasks
		} else {
			tasks, err = factory.ProjectStatus(factory.Paths{DataDir: cfg.DataDir}, folder)
		}
	}
	if err != nil {
		fprintf(stderr, "sheepdog factory status: %v\n", err)
		return 1
	}
	if len(tasks) == 0 {
		fprintln(stdout, "no tasks yet")
	}
	for _, ts := range tasks {
		fprintln(stdout, factory.FormatStatusLine(ts))
	}
	return 0
}

// runFactoryLog prints a project's log.jsonl, read straight from the data
// dir, one line per engine step with the rule behind it.
func runFactoryLog(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	configPath, n := fs.String("config", "", "config file"), fs.Int("n", 0, "last N lines")
	if err := fs.Parse(reorderFactoryDoctorArgs(args)); err != nil {
		return flagExit(err)
	}
	if fs.NArg() > 1 {
		printFactoryUsage(stderr)
		return 2
	}
	folder := ""
	if fs.NArg() == 1 {
		home, _ := os.UserHomeDir()
		folder = resolveTargetFolder(fs.Arg(0), home)
	}
	cfg, err := config.Load(*configPath)
	var lines []factory.LogLine
	var summary string
	if err == nil {
		lines, summary, err = factory.ProjectLog(ctx, factory.Paths{DataDir: cfg.DataDir}, folder, *n)
	}
	if err != nil {
		fprintf(stderr, "sheepdog factory log: %v\n", err)
		return 1
	}
	for _, l := range lines {
		l.At = l.At.Local()
		fprintln(stdout, factory.FormatLogLine(l))
	}
	if summary != "" {
		fprintln(stdout, summary)
	}
	return 0
}

// runTokenEnv names the run's token the engine puts in each run's environment.
const runTokenEnv = "SHEEPDOG_RUN_TOKEN"

// runFactoryPlan sends `plan apply|amend <file>` with the run's token; the engine checks it.
func runFactoryPlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || (args[0] != "apply" && args[0] != "amend") {
		printFactoryUsage(stderr)
		return 2
	}
	raw, err := os.ReadFile(args[1])
	if err == nil && !json.Valid(raw) {
		err = errors.New(args[1] + " is not valid JSON")
	}
	if err == nil {
		err = postFactoryReport(ctx, "plan "+args[0], json.RawMessage(raw))
	}
	if err != nil {
		fprintf(stderr, "sheepdog factory plan %s: %v\n", args[0], err)
		return 1
	}
	fprintf(stdout, "plan %s\n", map[string]string{"apply": "applied", "amend": "amended"}[args[0]])
	return 0
}

// runFactoryTask sends `task <action> <task> --why "<why>"` with the run's token.
func runFactoryTask(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory task", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	why := fs.String("why", "", "reason for the command")
	if err := fs.Parse(reorderFactoryDoctorArgs(args)); err != nil {
		return flagExit(err)
	}
	if fs.NArg() != 2 {
		printFactoryUsage(stderr)
		return 2
	}
	raw, _ := json.Marshal(map[string]string{"action": fs.Arg(0), "task": fs.Arg(1), "why": *why})
	if err := postFactoryReport(ctx, "task", raw); err != nil {
		fprintf(stderr, "sheepdog factory task %s: %v\n", fs.Arg(0), err)
		return 1
	}
	fprintf(stdout, "task %s: %s\n", fs.Arg(1), fs.Arg(0))
	return 0
}

// runFactoryRecap sends `recap decide <task> <choice> --why "<why>" [--child '<json>' ...]` with the run's token.
func runFactoryRecap(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "decide" {
		printFactoryUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("factory recap decide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	why := fs.String("why", "", "reason for the recap decision")
	var children []json.RawMessage
	fs.Func("child", "child task JSON (repeatable, for split)", func(v string) error {
		if !json.Valid([]byte(v)) {
			return errors.New("--child is not valid JSON")
		}
		children = append(children, json.RawMessage(v))
		return nil
	})
	if err := fs.Parse(reorderFactoryDoctorArgs(args[1:])); err != nil {
		return flagExit(err)
	}
	if fs.NArg() != 2 {
		printFactoryUsage(stderr)
		return 2
	}
	raw, _ := json.Marshal(map[string]any{"task": fs.Arg(0), "choice": fs.Arg(1), "why": *why, "children": children})
	if err := postFactoryReport(ctx, "recap decide", raw); err != nil {
		fprintf(stderr, "sheepdog factory recap decide: %v\n", err)
		return 1
	}
	fprintf(stdout, "recap %s: %s\n", fs.Arg(0), fs.Arg(1))
	return 0
}

var proofTimeout, proofOutputLimit = 5 * time.Minute, 64 * 1024

type limitedWriter struct {
	w   io.Writer
	rem int
	cut bool
	err error
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	n := min(len(p), max(0, l.rem))
	l.cut = l.cut || n < len(p)
	if n > 0 {
		_, err := l.w.Write(p[:n])
		l.rem, l.err = l.rem-n, cmp.Or(l.err, err)
	}
	return len(p), nil
}

func runFactoryReport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "ready" {
		printFactoryUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("factory report ready", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	why := fs.String("reject", "", "reject every finding of the latest review round with a reason")
	nothing := fs.Bool("nothing-to-change", false, "report that the task needs no change")
	nothingWhy := fs.String("why", "", "reason for --nothing-to-change")
	touched := fs.String("touched", "", "places the change had to reach (callers, tests, fixtures, config)")
	skipped := fs.String("skipped", "", "what was left out or not checked (empty means nothing skipped)")
	var proofs []flow.Proof
	for _, name := range []string{"proof", "artifact"} {
		fs.Func(name, name+` ("<outcome>=<value>", repeatable)`, func(s string) error {
			k, v, ok := strings.Cut(s, "=")
			if k, v = strings.TrimSpace(k), strings.TrimSpace(v); !ok || k == "" || v == "" {
				return errors.New("--" + name + ` must be "<outcome>=<value>"`)
			}
			p := flow.Proof{Outcome: k, Command: v}
			if name == "artifact" {
				p.Command, p.Artifact = "", v
			}
			proofs = append(proofs, p)
			return nil
		})
	}
	if err := fs.Parse(args[1:]); err != nil {
		return flagExit(err)
	}
	var reject, touchedArg, skippedArg *string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "reject":
			reject = why
		case "touched":
			touchedArg = touched
		case "skipped":
			skippedArg = skipped
		}
	})
	if fs.NArg() != 0 || (*nothing && reject != nil) || (!*nothing && *nothingWhy != "") || ((len(proofs) > 0 || touchedArg != nil || skippedArg != nil) && (*nothing || reject != nil)) {
		printFactoryUsage(stderr)
		return 2
	}
	if len(proofs) > 0 {
		if touchedArg == nil || strings.TrimSpace(*touchedArg) == "" {
			fprintln(stderr, `sheepdog factory report ready: A ready report with proofs must list what the change touched (--touched "<places>").`)
			return 1
		}
		if skippedArg == nil {
			fprintln(stderr, `sheepdog factory report ready: A ready report with proofs must state what was skipped or not checked (--skipped "<what was left out>", or --skipped "" when nothing was skipped).`)
			return 1
		}
	}
	socket := cmp.Or(os.Getenv("HC_SOCKET"), os.Getenv("SHEEPDOG_HC_SOCKET"), (config.Herdr{}).HCSocketPath())
	git := world.NewGit(hc.NewClient(socket), nil, nil)
	head, err := gitRevParse(ctx, git, "--verify", "HEAD^{commit}")
	if err == nil && len(proofs) > 0 {
		err = runProofs(ctx, git, head, proofs)
	}
	if err == nil {
		body := map[string]any{"head": head}
		if reject != nil {
			body["reject"] = *reject
		}
		if *nothing {
			body["nothing_to_change"] = *nothingWhy
		}
		if len(proofs) > 0 {
			body["proofs"] = proofs
		}
		if touchedArg != nil {
			body["touched"] = *touchedArg
		}
		if skippedArg != nil {
			body["skipped"] = *skippedArg
		}
		raw, _ := json.Marshal(body)
		err = postFactoryReport(ctx, "ready", raw)
	}
	if err != nil {
		fprintf(stderr, "sheepdog factory report ready: %v\n", err)
		return 1
	}
	if reject != nil {
		fprintf(stdout, "rejected %s\n", head)
		return 0
	}
	if *nothing {
		fprintf(stdout, "nothing to change at %s\n", head)
		return 0
	}
	fprintf(stdout, "ready %s\n", head)
	return 0
}

func gitRevParse(ctx context.Context, git *world.Git, args ...string) (string, error) {
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := git.Run(gctx, ".", append([]string{"rev-parse"}, args...)...)
	return strings.TrimSpace(string(r.Stdout)), err
}

func runProofs(ctx context.Context, git *world.Git, head string, proofs []flow.Proof) error {
	out, err := gitRevParse(ctx, git, "--absolute-git-dir", "--show-toplevel")
	gitDir, topLevel, _ := strings.Cut(out, "\n")
	if err != nil {
		return err
	}
	proofDir := filepath.Join(gitDir, "sheepdog-proofs")
	for i := range proofs {
		p := &proofs[i]
		if p.Artifact != "" {
			if abs, err := filepath.Abs(p.Artifact); err != nil {
				return errors.New("artifact for " + strconv.Quote(p.Outcome) + ": " + err.Error())
			} else if _, err = os.Stat(abs); err != nil {
				return errors.New("artifact for " + strconv.Quote(p.Outcome) + ": " + err.Error())
			} else {
				p.Artifact = abs
			}
			continue
		}
		if err := os.MkdirAll(proofDir, 0o700); err != nil {
			return err
		}
		p.OutputPath = filepath.Join(proofDir, head[:min(12, len(head))]+"-"+strconv.Itoa(i+1)+".log")
		f, err := os.OpenFile(p.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		lw := &limitedWriter{w: f, rem: proofOutputLimit}
		pctx, cancel := context.WithTimeout(ctx, proofTimeout)
		cmd := exec.CommandContext(pctx, "sh", "-c", p.Command)
		cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = topLevel, lw, lw, time.Second
		cmd.SysProcAttr, cmd.Cancel = &syscall.SysProcAttr{Setpgid: true}, func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		runErr := cmd.Run()
		var note string
		var ee *exec.ExitError
		if lw.cut {
			note = "\n[output truncated at " + strconv.Itoa(proofOutputLimit) + " bytes]\n"
		}
		switch {
		case runErr == nil:
		case errors.Is(pctx.Err(), context.DeadlineExceeded):
			p.Exit, note = 124, note+"\n[proof timed out after "+proofTimeout.String()+"]\n"
		case errors.As(runErr, &ee):
			p.Exit = ee.ExitCode()
		default:
			p.Exit, note = 1, note+"\n"+runErr.Error()+"\n"
		}
		cancel()
		if note != "" && lw.err == nil {
			_, lw.err = io.WriteString(f, note)
		}
		if err := errors.Join(lw.err, f.Close()); err != nil {
			return err
		}
	}
	return nil
}

func postFactoryReport(ctx context.Context, verb string, args json.RawMessage) error {
	// In a run's terminal the engine set SHEEPDOG_WEB_PORT: no config file needed.
	if port, err := strconv.Atoi(os.Getenv("SHEEPDOG_WEB_PORT")); err == nil && port > 0 {
		return postJSON(ctx, daemon.LocalURL("", port), "/api/factory/report", map[string]any{"token": os.Getenv(runTokenEnv), "verb": verb, "args": args})
	}
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	return postJSON(ctx, daemon.LocalURL(cfg.Web.Bind, cfg.Web.Port), "/api/factory/report", map[string]any{"token": os.Getenv(runTokenEnv), "verb": verb, "args": args})
}

func runFactoryDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	reordered := reorderFactoryDoctorArgs(args)
	fs := flag.NewFlagSet("factory doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	folderFlag := fs.String("folder", "", "project folder")
	configPath := fs.String("config", "", "config file")
	hcSocket := fs.String("hc-socket", "", "herdr-core socket")
	noCanary := fs.Bool("no-canary", false, "skip agent CLI canary start")
	if err := fs.Parse(reordered); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 || (fs.NArg() == 1 && *folderFlag != "") {
		fprintln(stderr, "sheepdog factory doctor: pass either --folder PATH or one [folder] argument, not both")
		printFactoryUsage(stderr)
		return 2
	}

	home, _ := os.UserHomeDir()
	folder := *folderFlag
	if fs.NArg() == 1 {
		folder = fs.Arg(0)
	}
	folder = resolveTargetFolder(folder, home)

	socket, dataDir, badCfg := resolveFactoryConfig(*hcSocket, *configPath, home)
	if badCfg != nil {
		fprintln(stdout, badCfg.Line())
		return 1
	}

	results := factoryDoctorRunner(ctx, factory.DoctorOptions{
		Folder:   folder,
		HCSocket: socket,
		Canary:   !*noCanary,
		Home:     home,
		DataDir:  dataDir,
	})
	for _, r := range results {
		fprintln(stdout, r.Line())
	}
	if onboard.Failed(results) {
		return 1
	}
	return 0
}

// reorderFactoryDoctorArgs allows flags to follow [folder] (e.g.
// `sheepdog factory doctor ~/repo --no-canary`) despite Go's flag.FlagSet
// stopping at the first positional argument.
func reorderFactoryDoctorArgs(args []string) []string {
	var flags, pos []string
	valueFlags := map[string]bool{
		"-goal": true, "--goal": true,
		"-max-running": true, "--max-running": true,
		"-why": true, "--why": true,
		"-child": true, "--child": true,
		"-folder": true, "--folder": true,
		"-config": true, "--config": true,
		"-hc-socket": true, "--hc-socket": true,
		"-n": true, "--n": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func resolveTargetFolder(raw, home string) string {
	f := expandHomePath(strings.TrimSpace(raw), home)
	if f == "" {
		f = "."
	}
	if filepath.IsAbs(f) {
		return filepath.Clean(f)
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return filepath.Join(wd, f)
	}
	return filepath.Clean(f)
}

func resolveFactoryConfig(explicitSocket, configPath, home string) (socket, dataDir string, badCfg *onboard.Result) {
	explicitCfg := strings.TrimSpace(configPath)
	cfgPath := explicitCfg
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	} else if _, err := os.Stat(cfgPath); err != nil {
		r := onboard.Result{
			Name:   "config",
			Status: onboard.Fail,
			Detail: err.Error(),
			Fix:    "pass an existing --config PATH",
		}
		return "", "", &r
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		r := onboard.Result{
			Name:   "config",
			Status: onboard.Fail,
			Detail: err.Error(),
			Fix:    "fix " + cfgPath + " or pass --config PATH",
		}
		return "", "", &r
	}
	if explicitSock := strings.TrimSpace(explicitSocket); explicitSock != "" {
		return expandHomePath(explicitSock, home), cfg.DataDir, nil
	}
	return expandHomePath(cfg.Herdr.HCSocketPath(), home), cfg.DataDir, nil
}

func expandHomePath(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
