package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/tier3"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func printBenchTier3Usage(w io.Writer) {
	fprintln(w, "usage: sheepdog bench run --tier 3 [flags]")
	fprintln(w, "")
	fprintln(w, "Tier 3: the real daemon driving real coding agents in a real herdr. Every run")
	fprintln(w, "gets its own named herdr session (never the default one), fresh clones of the")
	fprintln(w, "bench repo, real Claude Code (Haiku) or Codex workers, and a scratch daemon")
	fprintln(w, "(--voice=off --heard-echo) bound to that session's socket. A simulated CEO")
	fprintln(w, "talks to it in text; the judge grades the call against the real screens and")
	fprintln(w, "what was sent to the workers. Everything is torn down after each run. Costs")
	fprintln(w, "money and Claude subscription usage.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --tier N              3 (the only tier this command runs)")
	fprintln(w, "  --files GLOB          scenario files (default testdata/bench/tier3/*.json)")
	fprintln(w, "  --filter SUBSTR       only scenarios whose id contains SUBSTR")
	fprintln(w, "  --repeat N            runs per scenario (default 1)")
	fprintln(w, "  --parallel N          runs at once (default 1, at most the port count)")
	fprintln(w, "  --ports LIST          web ports for the scratch daemons (default 7793,7794,7795)")
	fprintln(w, "  --root DIR            scratch directory for the runs (required)")
	fprintln(w, "  --bench-repo PATH     toy repo cloned per workspace (default ~/Documents/sheepdog-bench)")
	fprintln(w, "  --env-file PATH       the daemon's env file with the brain keys (default ./.env)")
	fprintln(w, "  --brain-model ID      the daemon's brain.model (default gemini-3.8-flash)")
	fprintln(w, "  --claude-model ID     Claude worker model (default haiku)")
	fprintln(w, "  --codex-model ID      Codex worker model (default gpt-6-luna); effort --codex-effort (low)")
	fprintln(w, "  --simuser KIND        vertex (default), ollama or fake (the scenario script)")
	fprintln(w, "  --judge KIND          vertex (default), ollama or fake")
	fprintln(w, "  --judge-model ID      judge and simulated-user model (default "+eval.DefaultJudgeModel+")")
	fprintln(w, "  --out DIR             write report.json and report.md under DIR")
	fprintln(w, "  --events DIR          write each run's full daemon event log under DIR")
	fprintln(w, "  --keep                keep each run's scratch directory")
}

// wantsTier3 reports whether bench run's flags ask for tier 3 (its flag
// set is its own, so the choice is made before either set parses).
func wantsTier3(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--":
			return false
		case a == "--tier=3" || a == "-tier=3":
			return true
		case (a == "--tier" || a == "-tier") && i+1 < len(args) && args[i+1] == "3":
			return true
		}
	}
	return false
}

// runBenchTier3 implements `sheepdog bench run --tier 3`; runBenchRun
// hands it the run's flags (wantsTier3).
func runBenchTier3(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printBenchTier3Usage(stderr) }
	home, _ := os.UserHomeDir()
	tier := fs.Int("tier", 0, "bench tier")
	files := fs.String("files", "testdata/bench/tier3/*.json", "scenario files")
	filter := fs.String("filter", "", "scenario id substring")
	repeat := fs.Int("repeat", 1, "runs per scenario")
	parallel := fs.Int("parallel", 1, "runs at once")
	ports := fs.String("ports", "7793,7794,7795", "web ports")
	root := fs.String("root", "", "scratch directory")
	repo := fs.String("bench-repo", filepath.Join(home, "Documents", "sheepdog-bench"), "bench repo")
	envFile := fs.String("env-file", ".env", "daemon env file")
	brainModel := fs.String("brain-model", "", "brain model")
	claudeModel := fs.String("claude-model", "haiku", "Claude worker model")
	codexModel := fs.String("codex-model", "gpt-6-luna", "Codex worker model")
	codexEffort := fs.String("codex-effort", "low", "Codex reasoning effort")
	simuser := fs.String("simuser", eval.BackendVertex, "simulated user backend")
	judgeKind := fs.String("judge", eval.BackendVertex, "judge backend")
	judgeModel := fs.String("judge-model", "", "judge model")
	out := fs.String("out", "", "report directory")
	events := fs.String("events", "", "event log directory")
	keep := fs.Bool("keep", false, "keep scratch directories")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *tier != 3 {
		fprintln(stderr, "sheepdog bench run: only --tier 3 is implemented here")
		return 2
	}
	if *root == "" {
		fprintln(stderr, "sheepdog bench run: --root (a scratch directory) is required")
		return 2
	}
	scs, err := tier3.LoadGlob(*files, *filter)
	if err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	pool := make(chan int, 8)
	n := 0
	for _, p := range strings.Split(*ports, ",") {
		var port int
		if _, err := fmt.Sscan(strings.TrimSpace(p), &port); err != nil || port <= 0 {
			fprintf(stderr, "sheepdog bench run: bad port %q\n", p)
			return 2
		}
		if port == 7777 {
			fprintln(stderr, "sheepdog bench run: 7777 is the user's daemon port")
			return 2
		}
		pool <- port
		n++
	}
	if *parallel > n {
		*parallel = n
	}
	env, err := filepath.Abs(*envFile)
	if err != nil {
		return 1
	}
	if err := config.LoadEnvFile(env); err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return 1
	}
	o := tier3.Options{
		Root: absRoot, BenchRepo: *repo, EnvFile: env, BrainModel: *brainModel,
		ClaudeModel: *claudeModel, CodexModel: *codexModel, CodexEffort: *codexEffort,
		Ports: pool, EventsDir: *events, Keep: *keep, Log: stderr,
	}
	project, key := os.Getenv("VERTEX_PROJECT"), os.Getenv("VERTEX_API_KEY")
	sem := make(chan struct{}, 4)
	backend := func(kind string) (brain.Model, string, error) {
		m, id, err := eval.NewBackend(eval.BackendOptions{Kind: kind, Model: *judgeModel, VertexAPIKey: key, VertexProject: project}, logging.NopLogger{})
		if err != nil || m == nil {
			return m, id, err
		}
		return eval.Patient(m, sem, 8, altFor(kind, id)), id, nil
	}
	if *simuser != "fake" {
		um, uid, err := backend(*simuser)
		if err != nil {
			fprintf(stderr, "sheepdog bench run: simulated user: %v\n", err)
			return 1
		}
		o.NewSimUser = func(m *eval.Meter) eval.SimUser {
			return &eval.LLMSimUser{Model: eval.Metered(um, m, "simuser"), ModelID: uid}
		}
	}
	if *judgeKind != "fake" {
		jm, jid, err := backend(*judgeKind)
		if err != nil {
			fprintf(stderr, "sheepdog bench run: judge: %v\n", err)
			return 1
		}
		o.NewJudge = func(m *eval.Meter) eval.Judge {
			return &eval.LLMJudge{Model: eval.Metered(jm, m, "judge"), ModelID: jid}
		}
	}

	started := time.Now()
	total := len(scs) * max(*repeat, 1)
	var mu sync.Mutex
	done := 0
	recs, meter := tier3.Campaign(ctx, scs, o, *repeat, *parallel, func(r tier3.Record) {
		mu.Lock()
		defer mu.Unlock()
		done++
		fprintf(stderr, "[%d/%d] %s r%d: %s (%s, %s) %ds ≈ $%.4f\n", done, total, r.Scenario, r.Repeat, r.Status, r.EndReason, eval.Primary(r.Failures), r.DurationMS/1000, r.CostUSD)
	})
	runs := make([]eval.SimRun, len(recs))
	for i, r := range recs {
		runs[i] = r.SimRun
	}
	eval.WriteSimTable(stdout, runs)
	if *out != "" {
		setup := map[string]string{
			"mode":           "tier 3 (real herdr session, real workers, scratch daemon --voice=off --heard-echo)",
			"brain":          "gemini (vertex) " + orStr(*brainModel, "gemini-3.8-flash"),
			"workers":        "claude " + *claudeModel + "; codex " + *codexModel + " (" + *codexEffort + ")",
			"simulated user": *simuser + " " + orStr(*judgeModel, eval.DefaultJudgeModel),
			"judge":          *judgeKind + " " + orStr(*judgeModel, eval.DefaultJudgeModel),
			"parallel":       fmt.Sprint(*parallel),
			"scenarios":      *files + filterNote(*filter),
		}
		rep := eval.NewCampaignReport(started, setup, *repeat, runs, meter)
		if err := writeSimReports(*out, rep); err != nil {
			fprintf(stderr, "sheepdog bench run: %v\n", err)
			return 1
		}
		b, _ := json.MarshalIndent(recs, "", "  ")
		if err := os.WriteFile(filepath.Join(*out, "tier3.json"), b, 0o644); err != nil {
			fprintf(stderr, "sheepdog bench run: %v\n", err)
			return 1
		}
		fprintf(stderr, "reports: %s\n", *out)
	}
	return 0
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
