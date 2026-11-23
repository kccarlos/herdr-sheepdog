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
	fprintln(w, "usage: sheepdog factory <start|stop|status|log|plan|task|recap|report|ask|answer|doctor> [flags] [folder]")
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
	fprintln(w, "  report review <pass|changes> --code <code> --head <sha> --self-check-misses <n> --deletable \"<text>\" [--class \"<text>\"] [--not-run \"<text>\"] [--findings <path>]  (reviewer only) submit a review verdict")
	fprintln(w, "  report ask \"<question>\" --option \"<choice>\" --option \"<choice>\" --recommend \"<choice>\"  (worker/reviewer) ask the coordinator a decision question")
	fprintln(w, "  ask --kind <kind> [--task <task>] \"<question>\" --option \"<choice>\" --option \"<choice>\" --recommend \"<choice>\"  (coordinator) open an ask (gated kinds go to the owner)")
	fprintln(w, "  ask --kind override --task <task> [--task <task> ...]  (coordinator) open an engine-filled override card for the owner")
	fprintln(w, "  answer <ask> \"<option>\" [--why \"<why>\"]  (coordinator) answer an open decision ask")
	fprintln(w, "  doctor                               check unattended factory prerequisites:")
	fprintln(w, "           herdr-core (hc), timed project-volume probe inside herdr-core")
	fprintln(w, "           (macOS consent prompts and external-disk stalls), at least one")
	fprintln(w, "           logged-in agent CLI (claude, codex or agy) plus a canary start")
	fprintln(w, "           inside herdr-core, gh login and matching origin fetch and push URLs, and")
	fprintln(w, "           codexbar. Exit 1 on FAIL.")
	fprintln(w, "flags: --goal TEXT, --max-running N, --proof OUTCOME=CMD, --artifact OUTCOME=PATH, --touched TEXT, --skipped TEXT, --reject WHY, --nothing-to-change, --code CODE, --head SHA, --self-check-misses N, --deletable TEXT, --class TEXT, --not-run TEXT, --findings PATH, --why TEXT, --child JSON, --kind KIND, --task ID, --option TEXT, --recommend TEXT, --folder PATH, --config PATH, --hc-socket PATH, --no-canary")
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
	case "ask":
		return runFactoryAsk(ctx, args[1:], false, stdout, stderr)
	case "answer":
		return runFactoryAnswer(ctx, args[1:], stdout, stderr)
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

func factoryFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	return fs
}

func runFactoryCmd(ctx context.Context, sub string, args []string, stdout, stderr io.Writer) int {
	fs := factoryFlags("factory "+sub, stderr)
	configPath, goal, maxRunning := fs.String("config", "", "config file"), fs.String("goal", "", "project goal"), fs.Int("max-running", 0, "maximum concurrent tasks")
	if err := fs.Parse(reorderFactoryArgs(fs, args)); err != nil {
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
	fs := factoryFlags("factory status", stderr)
	configPath := fs.String("config", "", "config file")
	if err := fs.Parse(reorderFactoryArgs(fs, args)); err != nil {
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
		queryFolder := folder
		if queryFolder != "" {
			queryFolder = canonicalFolder(queryFolder)
		}
		var resp struct{ Tasks []factory.TaskStatus }
		if getJSON(ctx, daemon.LocalURL(cfg.Web.Bind, cfg.Web.Port), "/api/factory/status?"+url.Values{"folder": {queryFolder}, "data_dir": {cfg.DataDir}}.Encode(), &resp) == nil {
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
	fs := factoryFlags("factory log", stderr)
	configPath, n := fs.String("config", "", "config file"), fs.Int("n", 0, "last N lines")
	if err := fs.Parse(reorderFactoryArgs(fs, args)); err != nil {
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
	if err != nil {
		fprintf(stderr, "sheepdog factory plan %s: %v\n", args[0], err)
		return 1
	}
	return sendFactoryReport(ctx, "factory plan "+args[0], "plan "+args[0], json.RawMessage(raw), "plan "+map[string]string{"apply": "applied", "amend": "amended"}[args[0]], stdout, stderr)
}

// runFactoryTask sends `task <action> <task> --why "<why>"` with the run's token.
func runFactoryTask(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a0, a1, why, exit := parseTwoArgWhy("factory task", args, stderr)
	if exit >= 0 {
		return exit
	}
	return sendFactoryReport(ctx, "factory task "+a0, "task", map[string]string{"action": a0, "task": a1, "why": why}, "task "+a1+": "+a0, stdout, stderr)
}

func parseTwoArgWhy(name string, args []string, stderr io.Writer) (string, string, string, int) {
	fs := factoryFlags(name, stderr)
	why := fs.String("why", "", "reason")
	if err := fs.Parse(reorderFactoryArgs(fs, args)); err != nil {
		return "", "", "", flagExit(err)
	}
	if fs.NArg() != 2 {
		printFactoryUsage(stderr)
		return "", "", "", 2
	}
	return fs.Arg(0), fs.Arg(1), *why, -1
}

// runFactoryRecap sends `recap decide <task> <choice> --why "<why>" [--child '<json>' ...]` with the run's token.
func runFactoryRecap(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "decide" {
		printFactoryUsage(stderr)
		return 2
	}
	fs := factoryFlags("factory recap decide", stderr)
	why := fs.String("why", "", "reason for the recap decision")
	var children []json.RawMessage
	fs.Func("child", "child task JSON (repeatable, for split)", func(v string) error {
		if !json.Valid([]byte(v)) {
			return errors.New("--child is not valid JSON")
		}
		children = append(children, json.RawMessage(v))
		return nil
	})
	if err := fs.Parse(reorderFactoryArgs(fs, args[1:])); err != nil {
		return flagExit(err)
	}
	if fs.NArg() != 2 {
		printFactoryUsage(stderr)
		return 2
	}
	return sendFactoryReport(ctx, "factory recap decide", "recap decide", map[string]any{"task": fs.Arg(0), "choice": fs.Arg(1), "why": *why, "children": children}, "recap "+fs.Arg(0)+": "+fs.Arg(1), stdout, stderr)
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
	if len(args) >= 1 && args[0] == "ask" {
		return runFactoryAsk(ctx, args[1:], true, stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "review" {
		return runFactoryReportReview(ctx, args[1:], stdout, stderr)
	}
	if len(args) < 1 || args[0] != "ready" {
		printFactoryUsage(stderr)
		return 2
	}
	fs := factoryFlags("factory report ready", stderr)
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
		msg := "ready " + head
		if reject != nil {
			msg = "rejected " + head
		} else if *nothing {
			msg = "nothing to change at " + head
		}
		return sendFactoryReport(ctx, "factory report ready", "ready", body, msg, stdout, stderr)
	}
	fprintf(stderr, "sheepdog factory report ready: %v\n", err)
	return 1
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

func runFactoryReportReview(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := factoryFlags("factory report review", stderr)
	code := fs.String("code", "", "round one-time code")
	head := fs.String("head", "", "reviewed commit SHA")
	missesRaw := fs.String("self-check-misses", "", "findings the worker's own checks should have caught")
	deletable := fs.String("deletable", "", "lines, options or abstractions that could go with no loss")
	class := fs.String("class", "", "finding class when second of its kind")
	notRun := fs.String("not-run", "", "checks that could not be run and why")
	findingsPath := fs.String("findings", "", "path to findings file outside the worktree")
	if err := fs.Parse(reorderFactoryArgs(fs, args)); err != nil {
		return flagExit(err)
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if fs.NArg() != 1 {
		printFactoryUsage(stderr)
		return 2
	}
	verdict := strings.TrimSpace(fs.Arg(0))
	if verdict != flow.VerdictPass && verdict != flow.VerdictChanges {
		printFactoryUsage(stderr)
		return 2
	}
	if !seen["code"] || strings.TrimSpace(*code) == "" || !seen["head"] || strings.TrimSpace(*head) == "" ||
		!seen["self-check-misses"] || !seen["deletable"] || strings.TrimSpace(*deletable) == "" {
		printFactoryUsage(stderr)
		return 2
	}
	misses, err := strconv.Atoi(strings.TrimSpace(*missesRaw))
	if err != nil || misses < 0 {
		printFactoryUsage(stderr)
		return 2
	}
	findings := ""
	if seen["findings"] {
		fp := strings.TrimSpace(*findingsPath)
		if fp == "" || fp == "-" {
			printFactoryUsage(stderr)
			return 2
		}
		f, err := os.Open(fp)
		if err != nil {
			fprintf(stderr, "sheepdog factory report review: %v\n", err)
			return 1
		}
		raw, rerr := io.ReadAll(io.LimitReader(f, int64(proofOutputLimit)+1))
		cerr := f.Close()
		if err := cmp.Or(rerr, cerr); err != nil {
			fprintf(stderr, "sheepdog factory report review: %v\n", err)
			return 1
		}
		if len(raw) > proofOutputLimit {
			fprintf(stderr, "sheepdog factory report review: findings file %s is over %d bytes\n", fp, proofOutputLimit)
			return 1
		}
		findings = string(raw)
	}
	body := map[string]any{
		"verdict":           verdict,
		"code":              *code,
		"head":              *head,
		"self_check_misses": misses,
		"deletable":         *deletable,
		"class":             *class,
		"not_run":           *notRun,
		"findings":          findings,
	}
	return sendFactoryReport(ctx, "factory report review", "review", body, "review "+verdict, stdout, stderr)
}

func sendFactoryReport(ctx context.Context, cmd, verb string, body any, okMsg string, stdout, stderr io.Writer) int {
	raw, _ := json.Marshal(body)
	if err := postFactoryReport(ctx, verb, raw); err != nil {
		fprintf(stderr, "sheepdog %s: %v\n", cmd, err)
		return 1
	}
	fprintln(stdout, okMsg)
	return 0
}

func runFactoryAsk(ctx context.Context, args []string, isReport bool, stdout, stderr io.Writer) int {
	name := map[bool]string{false: "factory ask", true: "factory report ask"}[isReport]
	fs := factoryFlags(name, stderr)
	kind, rec := fs.String("kind", "", "ask kind"), fs.String("recommend", "", "recommended option")
	var tasks, opts []string
	fs.Func("task", "task id", func(v string) error { tasks = append(tasks, v); return nil })
	fs.Func("option", "choice", func(v string) error { opts = append(opts, v); return nil })
	if err := fs.Parse(reorderFactoryArgs(fs, args)); err != nil {
		return flagExit(err)
	}
	if strings.TrimSpace(*kind) == flow.KindOverride {
		if fs.NArg() != 0 {
			printFactoryUsage(stderr)
			return 2
		}
		return sendFactoryReport(ctx, name, "ask", map[string]any{"kind": flow.KindOverride, "tasks": tasks}, "ask recorded", stdout, stderr)
	}
	if fs.NArg() != 1 || len(tasks) > 1 {
		printFactoryUsage(stderr)
		return 2
	}
	task := ""
	if len(tasks) == 1 {
		task = tasks[0]
	}
	if isReport && *kind == "" {
		*kind = flow.KindDecision
	}
	return sendFactoryReport(ctx, name, "ask", map[string]any{"kind": *kind, "task": task, "question": fs.Arg(0), "options": opts, "recommend": *rec}, "ask recorded", stdout, stderr)
}

func runFactoryAnswer(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a0, a1, why, exit := parseTwoArgWhy("factory answer", args, stderr)
	if exit >= 0 {
		return exit
	}
	return sendFactoryReport(ctx, "factory answer", "answer", map[string]string{"ask": a0, "option": a1, "why": why}, "answered "+a0, stdout, stderr)
}

func postFactoryReport(ctx context.Context, verb string, args json.RawMessage) error {
	// In a run's terminal the engine set SHEEPDOG_WEB_PORT: no config file needed.
	url := ""
	if port, err := strconv.Atoi(os.Getenv("SHEEPDOG_WEB_PORT")); err == nil && port > 0 {
		url = daemon.LocalURL("", port)
	} else if cfg, err := config.Load(""); err != nil {
		return err
	} else {
		url = daemon.LocalURL(cfg.Web.Bind, cfg.Web.Port)
	}
	return postJSON(ctx, url, "/api/factory/report", map[string]any{"token": os.Getenv(runTokenEnv), "verb": verb, "args": args})
}

func runFactoryDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := factoryFlags("factory doctor", stderr)
	folderFlag := fs.String("folder", "", "project folder")
	configPath := fs.String("config", "", "config file")
	hcSocket := fs.String("hc-socket", "", "herdr-core socket")
	noCanary := fs.Bool("no-canary", false, "skip agent CLI canary start")
	if err := fs.Parse(reorderFactoryArgs(fs, args)); err != nil {
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

// reorderFactoryArgs allows flags to follow positional arguments (e.g.
// `sheepdog factory doctor ~/repo --no-canary`) despite Go's flag.FlagSet
// stopping at the first positional argument. Value flags are derived from fs
// so adding a flag to a subcommand never requires updating a separate list.
func reorderFactoryArgs(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			flags = append(flags, "--")
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if flagTakesValue(fs, a) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func flagTakesValue(fs *flag.FlagSet, arg string) bool {
	if fs == nil || strings.Contains(arg, "=") {
		return false
	}
	name := strings.TrimPrefix(arg, "-")
	name = strings.TrimPrefix(name, "-")
	if name == "" || strings.HasPrefix(name, "-") {
		return false
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
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

// canonicalFolder resolves symlinks on path in the owner's login session,
// retaining any missing suffix below an existing directory.
func canonicalFolder(path string) string {
	for cur, tail := filepath.Clean(path), ""; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			if tail == "" {
				return real
			}
			return filepath.Join(real, tail)
		} else if _, lerr := os.Lstat(cur); lerr == nil || !os.IsNotExist(lerr) {
			return path
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
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
