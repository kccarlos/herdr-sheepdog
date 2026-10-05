package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Repeat runs and their statistics (after pipecat's evals suite): a live
// model is not deterministic, so one run says little. A case's pass rate
// counts only the runs that were graded: errored runs (the harness
// failed) and runs that never started are reported but left out, and
// neither is ever a pass.

// RunRecord is one run of one case, whatever kind of case it is.
type RunRecord struct {
	Case       string    `json:"case"`
	Repeat     int       `json:"repeat"`
	Status     RunStatus `json:"status"`
	Failures   []Failure `json:"failures,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	CostUSD    float64   `json:"cost_usd_estimate"`
}

// Record is a simulated run's RunRecord.
func (r SimRun) Record() RunRecord {
	return RunRecord{Case: r.Scenario, Repeat: r.Repeat, Status: r.Status, Failures: r.Failures, DurationMS: r.DurationMS, CostUSD: r.CostUSD}
}

// Record is an SVP or assistant result's RunRecord (repeat is the run's
// index).
func (r Result) Record(repeat int) RunRecord {
	return RunRecord{Case: r.ID, Repeat: repeat, Status: r.RunStatus(), Failures: r.Failures, DurationMS: r.Duration.Milliseconds()}
}

// CaseStats is one case's numbers over its runs.
type CaseStats struct {
	Case    string `json:"case"`
	Runs    int    `json:"runs"`
	Pass    int    `json:"pass"`
	Fail    int    `json:"fail"`
	Errored int    `json:"errored"`
	NotRun  int    `json:"not_run"`
	// PassRate is Pass / (Pass + Fail); nil when no run was graded.
	PassRate *float64 `json:"pass_rate"`
	// Primary counts each failed run once, by its most severe label;
	// Labels counts every label of every run.
	Primary        map[FailureType]int `json:"primary,omitempty"`
	Labels         map[FailureType]int `json:"labels,omitempty"`
	MeanDurationMS int64               `json:"mean_duration_ms"`
	CostUSD        float64             `json:"cost_usd_estimate"`
}

// Aggregate computes per-case statistics, in first-seen case order, and
// the totals over every run (Case "TOTAL").
func Aggregate(recs []RunRecord) (perCase []CaseStats, total CaseStats) {
	idx := map[string]int{}
	total = CaseStats{Case: "TOTAL", Primary: map[FailureType]int{}, Labels: map[FailureType]int{}}
	var durs = map[string]int64{}
	var totalDur int64
	for _, r := range recs {
		i, ok := idx[r.Case]
		if !ok {
			i = len(perCase)
			idx[r.Case] = i
			perCase = append(perCase, CaseStats{Case: r.Case, Primary: map[FailureType]int{}, Labels: map[FailureType]int{}})
		}
		for _, cs := range []*CaseStats{&perCase[i], &total} {
			cs.Runs++
			cs.CostUSD += r.CostUSD
			switch r.Status {
			case RunPass:
				cs.Pass++
			case RunFail:
				cs.Fail++
			case RunError:
				cs.Errored++
			default:
				cs.NotRun++
			}
			if p := Primary(r.Failures); p != "" {
				cs.Primary[p]++
			}
			seen := map[FailureType]bool{}
			for _, f := range r.Failures {
				if !seen[f.Type] {
					seen[f.Type] = true
					cs.Labels[f.Type]++
				}
			}
		}
		durs[r.Case] += r.DurationMS
		totalDur += r.DurationMS
	}
	finish := func(cs *CaseStats, dur int64) {
		if graded := cs.Pass + cs.Fail; graded > 0 {
			rate := float64(cs.Pass) / float64(graded)
			cs.PassRate = &rate
		}
		if cs.Runs > 0 {
			cs.MeanDurationMS = dur / int64(cs.Runs)
		}
	}
	for i := range perCase {
		finish(&perCase[i], durs[perCase[i].Case])
	}
	finish(&total, totalDur)
	return perCase, total
}

// Campaign runs every scenario repeat times with at most parallel runs at
// once. Once the estimated spend reaches budget (when positive), or ctx
// ends, the remaining runs are recorded as not_run rather than started.
// done, when set, is called after each run (progress).
func Campaign(ctx context.Context, scenarios []SimScenario, o SimOptions, repeat, parallel int, budget float64, done func(SimRun)) ([]SimRun, *Meter) {
	repeat, parallel = max(repeat, 1), max(parallel, 1)
	type job struct {
		sc SimScenario
		r  int
		i  int
	}
	var jobs []job
	// Repeat-major order: every scenario's first run before any second,
	// so a budget stop leaves the coverage even.
	for r := 1; r <= repeat; r++ {
		for _, sc := range scenarios {
			jobs = append(jobs, job{sc: sc, r: r, i: len(jobs)})
		}
	}
	out := make([]SimRun, len(jobs))
	meter := NewMeter()
	var mu sync.Mutex
	queue := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				mu.Lock()
				spent := meter.Total()
				mu.Unlock()
				var run SimRun
				switch {
				case ctx.Err() != nil:
					run = notRun(j.sc.ID, j.r, "cancelled")
				case budget > 0 && spent >= budget:
					run = notRun(j.sc.ID, j.r, fmt.Sprintf("spend budget $%.2f reached ($%.2f)", budget, spent))
				default:
					run = RunSim(ctx, j.sc, o, j.r)
				}
				mu.Lock()
				for _, l := range run.Usage {
					meter.Add(l.Role, l.Model, l.Usage)
				}
				out[j.i] = run
				mu.Unlock()
				if done != nil {
					done(run)
				}
			}
		}()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()
	return out, meter
}

func notRun(id string, repeat int, why string) SimRun {
	return SimRun{Scenario: id, Repeat: repeat, Status: RunNotRun, EndReason: "not_run", Failures: []Failure{{FailNotRun, why}}}
}

// CampaignReport is the JSON report of a campaign.
type CampaignReport struct {
	Started  time.Time         `json:"started"`
	Finished time.Time         `json:"finished"`
	Setup    map[string]string `json:"setup"`
	Repeat   int               `json:"repeat"`
	Stats    []CaseStats       `json:"stats"`
	Total    CaseStats         `json:"total"`
	Spend    []MeterLine       `json:"spend"`
	SpendUSD float64           `json:"spend_usd_estimate"`
	// Explain aggregates the explanation scores (explain scenarios).
	Explain *ExplainSummary `json:"explain,omitempty"`
	Runs    []SimRun        `json:"runs"`
}

// NewCampaignReport assembles the report of runs.
func NewCampaignReport(started time.Time, setup map[string]string, repeat int, runs []SimRun, meter *Meter) CampaignReport {
	recs := make([]RunRecord, len(runs))
	for i, r := range runs {
		recs[i] = r.Record()
	}
	stats, total := Aggregate(recs)
	var ex []*ExplainScore
	for _, r := range runs {
		ex = append(ex, r.Explain)
	}
	return CampaignReport{Started: started, Finished: time.Now(), Setup: setup, Repeat: repeat, Stats: stats, Total: total, Spend: meter.Lines(), SpendUSD: meter.Total(), Explain: SummarizeExplain(ex), Runs: runs}
}

// WriteJSON writes the report to path.
func (c CampaignReport) WriteJSON(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// WriteMarkdown writes the human report: setup, per-scenario pass rates,
// the failure taxonomy, spend, and the failed runs with excerpts (at most
// maxFailures of them, the most severe first).
func (c CampaignReport) WriteMarkdown(w io.Writer, maxFailures int) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("# Simulated-user campaign\n\n")
	p("Started %s, finished %s (%s). Repeat %d.\n\n", c.Started.UTC().Format(time.RFC3339), c.Finished.UTC().Format(time.RFC3339), c.Finished.Sub(c.Started).Round(time.Second), c.Repeat)
	if len(c.Setup) > 0 {
		keys := make([]string, 0, len(c.Setup))
		for k := range c.Setup {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p("- %s: %s\n", k, c.Setup[k])
		}
		p("\n")
	}
	p("## Pass rate per scenario\n\nPass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.\n\n")
	p("| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range append(append([]CaseStats(nil), c.Stats...), c.Total) {
		name := s.Case
		if name == "TOTAL" {
			name = "**TOTAL**"
		}
		p("| %s | %d | %d | %d | %d | %d | %s | %s | %.1f | %.3f |\n", name, s.Runs, s.Pass, s.Fail, s.Errored, s.NotRun, rateString(s.PassRate), countsString(s.Primary), float64(s.MeanDurationMS)/1000, s.CostUSD)
	}
	p("\n%s", c.failoverSection())
	p("\n## Failure taxonomy\n\n| Label | Runs (primary) | Runs (any) |\n|---|---|---|\n")
	for _, t := range FailureOrder {
		if c.Total.Primary[t] == 0 && c.Total.Labels[t] == 0 {
			continue
		}
		p("| %s | %d | %d |\n", t, c.Total.Primary[t], c.Total.Labels[t])
	}
	verdicts := map[string][2]int{}
	var names []string
	for _, r := range c.Runs {
		for _, v := range r.Verdicts {
			if _, ok := verdicts[v.Name]; !ok {
				names = append(names, v.Name)
			}
			n := verdicts[v.Name]
			n[1]++
			if v.Passed() {
				n[0]++
			}
			verdicts[v.Name] = n
		}
	}
	if len(names) > 0 {
		p("\n## Judge items\n\n| Item | Yes | Asked | Yes rate |\n|---|---|---|---|\n")
		for _, n := range names {
			v := verdicts[n]
			p("| %s | %d | %d | %.0f%% |\n", n, v[0], v[1], 100*float64(v[0])/float64(v[1]))
		}
	}
	c.Explain.WriteMarkdown(w)
	p("\n## Spend (estimate at list prices)\n\n| Role | Model | Calls | In | Out | Reasoning | $ |\n|---|---|---|---|---|---|---|\n")
	for _, l := range c.Spend {
		p("| %s | %s | %d | %d | %d | %d | %.3f |\n", l.Role, l.Model, l.Usage.Calls, l.Usage.In, l.Usage.Out, l.Usage.Reasoning, l.CostUSD)
	}
	p("| **total** | | | | | | **%.3f** |\n", c.SpendUSD)

	var failed []SimRun
	for _, r := range c.Runs {
		if r.Status == RunFail || r.Status == RunError {
			failed = append(failed, r)
		}
	}
	rank := map[FailureType]int{}
	for i, t := range FailureOrder {
		rank[t] = i
	}
	sort.SliceStable(failed, func(i, j int) bool { return rank[Primary(failed[i].Failures)] < rank[Primary(failed[j].Failures)] })
	if len(failed) > maxFailures {
		failed = failed[:maxFailures]
	}
	if len(failed) > 0 {
		p("\n## Failed runs\n")
	}
	for _, r := range failed {
		p("\n### %s r%d — %s (%s, %d turns, %.1f s)\n\n", r.Scenario, r.Repeat, Primary(r.Failures), r.EndReason, r.Turns, float64(r.DurationMS)/1000)
		for _, f := range r.Failures {
			p("- **%s**: %s\n", f.Type, mdEscape(firstLine(f.Detail)))
		}
		p("\n```text\n%s\n```\n", renderSim(r.Transcript))
	}
}

// failoverSection is the markdown table of the brain's failovers: per
// scenario, the runs that failed over, their failover count and which
// provider answered how many runs.
func (c CampaignReport) failoverSection() string {
	type agg struct {
		runs, failedOver, failovers int
		answered                    map[string]int
	}
	var order []string
	by := map[string]*agg{}
	total := &agg{answered: map[string]int{}}
	for _, r := range c.Runs {
		if r.Status == RunNotRun {
			continue
		}
		a := by[r.Scenario]
		if a == nil {
			a = &agg{answered: map[string]int{}}
			by[r.Scenario] = a
			order = append(order, r.Scenario)
		}
		for _, x := range []*agg{a, total} {
			x.runs++
			x.failovers += r.Failovers
			if r.Failovers > 0 {
				x.failedOver++
			}
			if r.AnsweredBy != "" {
				x.answered[r.AnsweredBy]++
			}
		}
	}
	var b strings.Builder
	b.WriteString("## Brain failover\n\nFailovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.\n\n| Scenario | Runs | Runs with failover | Failovers | Answered by |\n|---|---|---|---|---|\n")
	row := func(name string, a *agg) {
		var parts []string
		for k, v := range a.answered {
			parts = append(parts, fmt.Sprintf("%s ×%d", k, v))
		}
		sort.Strings(parts)
		ans := "–"
		if len(parts) > 0 {
			ans = strings.Join(parts, ", ")
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", name, a.runs, a.failedOver, a.failovers, ans)
	}
	for _, id := range order {
		row(id, by[id])
	}
	row("**TOTAL**", total)
	return b.String()
}

func rateString(r *float64) string {
	if r == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100**r)
}

func countsString(m map[FailureType]int) string {
	var parts []string
	for _, t := range FailureOrder {
		if m[t] > 0 {
			parts = append(parts, fmt.Sprintf("%s ×%d", t, m[t]))
		}
	}
	if len(parts) == 0 {
		return "–"
	}
	return strings.Join(parts, ", ")
}

func mdEscape(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

// WriteStatsTable prints per-case statistics for repeated SVP or
// assistant runs.
func WriteStatsTable(w io.Writer, title string, recs []RunRecord) {
	stats, total := Aggregate(recs)
	idw := 5
	for _, s := range stats {
		idw = max(idw, len(s.Case))
	}
	_, _ = fmt.Fprintf(w, "== %s ==\n", title)
	for _, s := range append(stats, total) {
		_, _ = fmt.Fprintf(w, "  %-*s  runs %2d  pass %2d  fail %2d  err %2d  not_run %2d  rate %5s  %s\n", idw, s.Case, s.Runs, s.Pass, s.Fail, s.Errored, s.NotRun, rateString(s.PassRate), countsString(s.Primary))
	}
}
