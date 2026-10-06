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
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

func printBenchRejudgeUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog bench rejudge --from DIR [flags]")
	fprintln(w, "")
	fprintln(w, "Ask every judge item of a finished campaign again with another judge (or a")
	fprintln(w, "changed rubric), without running the conversations again: the criteria come")
	fprintln(w, "from the scenarios as the code states them now, the transcript and facts from")
	fprintln(w, "the records. DIR is a tier-2 campaign (runs/*.json, rewritten; then run")
	fprintln(w, "`bench report --from DIR`) or a tier-1 eval sim result (report.json and")
	fprintln(w, "report.md, rewritten). Copy the raw results first to keep them. Each rejudged")
	fprintln(w, "run is marked with the judge model and written at once, so an interrupted")
	fprintln(w, "rejudge (the Claude judge's usage limit) resumes where it stopped.")
	fprintln(w, "Tier-1 records from before 2026-10-06 carry no facts: they are rebuilt from")
	fprintln(w, "the scenario and the recorded timeline (eval.ReplayFacts).")
	fprintln(w, "")
	fprintln(w, judgeHelp)
	fprintln(w, "  --judge-model ID      judge model (default sonnet for claude, "+eval.DefaultVertexModel+" for vertex)")
	fprintln(w, "  --orchestrator        tier 2: also ask the workday content items of the orchestrator's text")
	fprintln(w, "  --scenarios GLOB      tier-2 scenario files (default "+defaultBenchScenarios+")")
	fprintln(w, "  --sim GLOB            eval sim scenario files (default "+defaultBenchSims+")")
	fprintln(w, "  --fixtures PATH       screen fixtures (default testdata/herdr)")
	fprintln(w, "  --filter TEXT         only runs whose scenario id contains TEXT")
	fprintln(w, "  --config PATH, --env-file PATH   Vertex credentials for --judge vertex")
}

func runBenchRejudge(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench rejudge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printBenchRejudgeUsage(stderr) }
	from := fs.String("from", "", "campaign directory")
	judgeKind := fs.String("judge", "", "judge backend")
	judgeModel := fs.String("judge-model", "", "judge model")
	orch := fs.Bool("orchestrator", false, "tier 2: also the orchestrator's content items")
	scen := fs.String("scenarios", defaultBenchScenarios, "scenario files")
	sims := fs.String("sim", defaultBenchSims, "eval sim files")
	fixtures := fs.String("fixtures", "testdata/herdr", "screen fixtures")
	filter := fs.String("filter", "", "scenario id filter")
	configPath := fs.String("config", "", "config file")
	envFile := fs.String("env-file", "", "env file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *from == "" {
		fprintln(stderr, "sheepdog bench rejudge: --from is required")
		return 2
	}
	if *envFile != "" {
		if err := config.LoadEnvFile(*envFile); err != nil {
			fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
			return 1
		}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
		return 1
	}
	jk, jmod := judgeChoice(*judgeKind, *judgeModel)
	m, id, err := newJudgeModel(jk, jmod, judgeVertex{key: cfg.Gemini.Vertex.APIKey, project: cfg.Gemini.Vertex.Project}, make(chan struct{}, 4), nil)
	if err == nil && m == nil {
		err = fmt.Errorf("judge %q cannot rejudge", jk)
	}
	if err != nil {
		fprintf(stderr, "sheepdog bench rejudge: judge: %v\n", err)
		return 1
	}
	meter := eval.NewMeter()
	judge := newLLMJudge(m, id, jk, meter)
	t0 := time.Now()
	var code int
	if files, _ := filepath.Glob(filepath.Join(*from, "runs", "*.json")); len(files) > 0 {
		code = rejudgeTier2(ctx, *from, judge, id, *orch, *scen, *sims, *filter, stderr)
	} else {
		code = rejudgeTier1(ctx, *from, judge, id, *sims, *fixtures, *filter, stderr)
	}
	fprintf(stdout, "judge %s %s: %s, %d calls\n", jk, id, time.Since(t0).Round(time.Second), callsOf(meter))
	eval.WriteSpend(stdout, meter)
	return code
}

func callsOf(m *eval.Meter) int {
	n := 0
	for _, l := range m.Lines() {
		n += l.Usage.Calls
	}
	return n
}

func rejudgeTier2(ctx context.Context, dir string, judge eval.Judge, id string, orch bool, scen, sims, filter string, stderr io.Writer) int {
	runs, err := bench.LoadRuns(dir)
	if err != nil {
		fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
		return 1
	}
	scs, err := bench.LoadScenarios(scen, sims)
	if err != nil {
		fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
		return 1
	}
	byID := map[string]bench.Scenario{}
	for _, s := range scs {
		byID[s.ID] = s
	}
	failed := 0
	for i := range runs {
		r := &runs[i]
		sc, ok := byID[r.Scenario]
		if !ok || !contains(r.Scenario, filter) {
			continue
		}
		// The marker: id once the verdicts are rejudged, id+"+orchestrator"
		// once the orchestrator's items are too.
		orchID := id + "+orchestrator"
		needMain := r.Judge != id && r.Judge != orchID
		needOrch := orch && sc.Workday != nil && r.Judge != orchID
		if !needMain && !needOrch {
			continue
		}
		if needMain {
			if err := bench.Rejudge(ctx, judge, sc, r); err != nil {
				fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
				if errors.Is(err, eval.ErrClaudeUsageLimit) || isUsageLimitText(err) {
					return 1
				}
				failed++
				continue
			}
			r.Judge = id
		}
		if needOrch {
			evs, err := bench.LoadEvents(filepath.Join(dir, "events", fmt.Sprintf("%s.r%d.jsonl", r.Scenario, r.Repeat)))
			if err == nil {
				psc := sc
				psc.SimScenario = eval.WithPersona(sc.SimScenario, r.Persona)
				err = bench.RescoreOrchestrator(ctx, judge, psc, r, evs)
			}
			if err != nil {
				fprintf(stderr, "sheepdog bench rejudge: orchestrator: %v\n", err)
				failed++
			} else {
				r.Judge = orchID
			}
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "runs", fmt.Sprintf("%s.r%d.json", r.Scenario, r.Repeat)), b, 0o644); err != nil {
			fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
			return 1
		}
		fprintf(stderr, "rejudged %s r%d: %s\n", r.Scenario, r.Repeat, r.Status)
	}
	if failed > 0 {
		fprintf(stderr, "sheepdog bench rejudge: %d runs not rejudged (run again to retry them)\n", failed)
		return 1
	}
	return 0
}

func rejudgeTier1(ctx context.Context, dir string, judge eval.Judge, id, sims, fixtures, filter string, stderr io.Writer) int {
	path := filepath.Join(dir, "report.json")
	b, err := os.ReadFile(path)
	if err != nil {
		fprintf(stderr, "sheepdog bench rejudge: %s: no runs/ and no report.json\n", dir)
		return 1
	}
	var rep eval.CampaignReport
	if err := json.Unmarshal(b, &rep); err != nil {
		fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
		return 1
	}
	scs, err := loadSimScenarios(sims, "")
	if err != nil {
		fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
		return 1
	}
	byID := map[string]eval.SimScenario{}
	for _, s := range scs {
		byID[s.ID] = s
	}
	write := func() error {
		meter := eval.NewMeter()
		for _, r := range rep.Runs {
			for _, l := range r.Usage {
				meter.Add(l.Role, l.Model, l.Usage)
			}
		}
		out := eval.NewCampaignReport(rep.Started, rep.Setup, rep.Repeat, rep.Runs, meter)
		out.Finished = rep.Finished
		out.NorthStar = eval.NorthStarBlock(rep.Runs, eval.NorthStarOptions{Tier: "1", Scenarios: scs, EventsDir: filepath.Join(dir, "events")})
		return writeSimReports(dir, out)
	}
	failed := 0
	for i := range rep.Runs {
		r := &rep.Runs[i]
		sc, ok := byID[r.Scenario]
		if !ok || r.Judge == id || !contains(r.Scenario, filter) {
			continue
		}
		if err := eval.RejudgeSim(ctx, judge, sc, r, fixtures); err != nil {
			fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
			if errors.Is(err, eval.ErrClaudeUsageLimit) || isUsageLimitText(err) {
				_ = write()
				return 1
			}
			failed++
			continue
		}
		r.Judge = id
		if rep.Setup != nil {
			rep.Setup["judge (rejudged)"] = id
		}
		if err := write(); err != nil {
			fprintf(stderr, "sheepdog bench rejudge: %v\n", err)
			return 1
		}
		fprintf(stderr, "rejudged %s r%d: %s\n", r.Scenario, r.Repeat, r.Status)
	}
	if failed > 0 {
		fprintf(stderr, "sheepdog bench rejudge: %d runs not rejudged (run again to retry them)\n", failed)
		return 1
	}
	return 0
}

// isUsageLimitText catches the usage limit in an error that lost its
// type on the way (a verdict's error text).
func isUsageLimitText(err error) bool {
	return err != nil && strings.Contains(err.Error(), eval.ErrClaudeUsageLimit.Error())
}

func contains(s, sub string) bool { return sub == "" || strings.Contains(s, sub) }
