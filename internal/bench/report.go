package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// Report is a campaign's outcome.
type Report struct {
	Started   time.Time         `json:"started"`
	DurationS int               `json:"duration_s"`
	Setup     map[string]string `json:"setup"`
	Repeat    int               `json:"repeat"`
	Total     eval.CaseStats    `json:"total"`
	Cases     []eval.CaseStats  `json:"cases"`
	// Categories are the per-category numbers.
	Categories []eval.CaseStats `json:"categories"`
	Latency    []LatencyRow     `json:"latency"`
	Outcomes   map[string]int   `json:"latency_outcomes"`
	Voice      VoiceSummary     `json:"voice"`
	Spend      Spend            `json:"spend"`
	// Explain aggregates the explanation scores (explain scenarios).
	Explain *eval.ExplainSummary `json:"explain,omitempty"`
	// NorthStar is the north-star block (internal/northstar): NewReport
	// computes it from the records alone; SetNorthStar adds the events.
	NorthStar *northstar.Block `json:"north_star,omitempty"`
	Runs      []RunSummary     `json:"runs"`
}

// RunSummary is a run's line in the report (the full record is under
// runs/ in the campaign's output directory).
type RunSummary struct {
	Scenario  string         `json:"scenario"`
	Category  string         `json:"category"`
	Repeat    int            `json:"repeat"`
	Status    eval.RunStatus `json:"status"`
	Primary   string         `json:"primary,omitempty"`
	Failures  []eval.Failure `json:"failures,omitempty"`
	EndReason string         `json:"end_reason"`
	Turns     int            `json:"turns"`
	DurationS float64        `json:"duration_s"`
	CostUSD   float64        `json:"cost_usd_estimate"`
	Failovers int            `json:"failovers,omitempty"`
	MeanWER   float64        `json:"mean_wer"`
	// Transcript and Excerpt are kept for failed runs only.
	Transcript []string `json:"transcript,omitempty"`
	Excerpt    []string `json:"excerpt,omitempty"`
}

// LatencyRow is one turn.latency segment over every run.
type LatencyRow struct {
	Segment string  `json:"segment"`
	N       int     `json:"n"`
	P50     float64 `json:"p50_ms"`
	P90     float64 `json:"p90_ms"`
	Max     float64 `json:"max_ms"`
}

// VoiceSummary totals the voice-layer counters.
type VoiceSummary struct {
	Lines        int                `json:"user_lines"`
	NotHeard     int                `json:"lines_not_heard"`
	WERByLang    map[string]float64 `json:"mean_wer_by_lang"`
	WERByNoise   map[string]float64 `json:"mean_wer_by_condition"`
	Flushes      int                `json:"flushes"`
	LocalFlushes int                `json:"local_flushes"`
	Unheard      int                `json:"push_unheard"`
	Requeued     int                `json:"push_requeued"`
	Deferred     int                `json:"push_deferred"`
	Held         int                `json:"push_held"`
	Interrupted  int                `json:"interrupted"`
	Stalled      int                `json:"stalled"`
	Failovers    int                `json:"failovers"`
	// TTSSources counts the user lines by what rendered them (gemini
	// model or say).
	TTSSources map[string]int `json:"tts_sources"`
	// MarkupSpoken counts bot utterances whose heard text carries a
	// bracketed marker (e.g. "[system event, …]") read aloud.
	MarkupSpoken int `json:"markup_spoken"`
	// EmptyBot counts bot utterances with audio but no transcript.
	EmptyBot int `json:"bot_audio_without_transcript"`
}

// latencySegments are the turn.latency fields, in pipeline order.
var latencySegments = []string{"voice_ms", "relay_ms", "queue_ms", "brain_ms", "model_ms", "tool_ms", "delivery_ms", "push_queue_ms", "first_audio_ms", "total_ms"}

// NewReport builds the report.
func NewReport(started time.Time, setup map[string]string, repeat int, runs []Run, spend Spend, scs []Scenario) Report {
	rep := Report{Started: started, DurationS: int(time.Since(started).Seconds()), Setup: setup, Repeat: repeat, Spend: spend, Outcomes: map[string]int{}}
	recs := make([]eval.RunRecord, len(runs))
	cat := map[string][]eval.RunRecord{}
	for i, r := range runs {
		recs[i] = r.Record()
		c := r.Record()
		c.Case = r.Category
		cat[r.Category] = append(cat[r.Category], c)
	}
	rep.Cases, rep.Total = eval.Aggregate(recs)
	for _, c := range Categories {
		if len(cat[c]) == 0 {
			continue
		}
		_, t := eval.Aggregate(cat[c])
		t.Case = c
		rep.Categories = append(rep.Categories, t)
	}
	// Latency.
	vals := map[string][]float64{}
	for _, r := range runs {
		for _, l := range r.Latency {
			if o, _ := l["outcome"].(string); o != "" {
				rep.Outcomes[o]++
			}
			for _, s := range latencySegments {
				if v, ok := l[s].(float64); ok {
					vals[s] = append(vals[s], v)
				}
			}
		}
	}
	for _, s := range latencySegments {
		v := vals[s]
		if len(v) == 0 {
			continue
		}
		sort.Float64s(v)
		rep.Latency = append(rep.Latency, LatencyRow{Segment: strings.TrimSuffix(s, "_ms"), N: len(v), P50: pct(v, 0.5), P90: pct(v, 0.9), Max: v[len(v)-1]})
	}
	// Voice.
	cond := map[string]string{}
	for _, s := range scs {
		cond[s.ID] = conditionOf(s)
	}
	v := VoiceSummary{WERByLang: map[string]float64{}, WERByNoise: map[string]float64{}, TTSSources: map[string]int{}}
	nLang, nCond := map[string]int{}, map[string]int{}
	for _, r := range runs {
		for _, b := range r.Bot {
			switch {
			case strings.Contains(b.Text, "[system") || strings.Contains(b.Text, "[System"):
				v.MarkupSpoken++
			case strings.TrimSpace(b.Text) == "" && b.AudioMS > 0:
				v.EmptyBot++
			}
		}
		for _, l := range r.User {
			v.Lines++
			v.TTSSources[orStr(l.Source, "?")]++
			if l.Heard == "" {
				v.NotHeard++
			}
			lang := "en"
			if hasHanText(l.Said) {
				lang = "zh"
			}
			v.WERByLang[lang] += l.WER
			nLang[lang]++
			c := cond[r.Scenario]
			v.WERByNoise[c] += l.WER
			nCond[c]++
		}
		x := r.Voice
		v.Flushes += x.Flushes
		v.LocalFlushes += x.LocalFlushes
		v.Unheard += x.Unheard
		v.Requeued += x.Requeued
		v.Deferred += x.Deferred
		v.Held += x.Held
		v.Interrupted += x.Interrupted
		v.Stalled += x.Stalled
		v.Failovers += len(r.Failovers)
	}
	for k := range v.WERByLang {
		v.WERByLang[k] /= float64(nLang[k])
	}
	for k := range v.WERByNoise {
		v.WERByNoise[k] /= float64(nCond[k])
	}
	rep.Voice = v
	var ex []*eval.ExplainScore
	for _, r := range runs {
		ex = append(ex, r.Explain)
	}
	rep.Explain = eval.SummarizeExplain(ex)
	rep.NorthStar = northstar.Summarize("2", NorthStarRuns(runs, scs, ""))
	for _, r := range runs {
		s := RunSummary{Scenario: r.Scenario, Category: r.Category, Repeat: r.Repeat, Status: r.Status, Primary: string(eval.Primary(r.Failures)), Failures: r.Failures, EndReason: r.EndReason, Turns: r.Turns, DurationS: float64(r.DurationMS) / 1000, CostUSD: r.CostUSD, Failovers: len(r.Failovers), MeanWER: r.Voice.MeanWER}
		if r.Status == eval.RunFail || r.Status == eval.RunError {
			for _, l := range r.Transcript {
				s.Transcript = append(s.Transcript, fmt.Sprintf("[t+%ds] %s: %s", l.AtMS/1000, l.Who, l.Text))
			}
			for _, u := range r.User {
				if u.Heard != u.Said {
					s.Transcript = append(s.Transcript, fmt.Sprintf("(voice model heard %q as %q)", u.Said, u.Heard))
				}
			}
			s.Excerpt = r.Excerpt
		}
		rep.Runs = append(rep.Runs, s)
	}
	return rep
}

// conditionOf names a scenario's acoustic condition.
func conditionOf(s Scenario) string {
	var parts []string
	if s.Audio.Noise != nil && s.Audio.Noise.Kind != "" {
		parts = append(parts, fmt.Sprintf("%s %gdB", s.Audio.Noise.Kind, s.Audio.Noise.SNRDB))
	}
	if s.Audio.Echo != nil {
		parts = append(parts, "echo")
	}
	if len(parts) == 0 {
		return "clean"
	}
	return strings.Join(parts, "+")
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

// WriteJSON saves the report.
func (rep Report) WriteJSON(path string) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func rate(r *float64) string {
	if r == nil {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", *r*100)
}

func labels(m map[eval.FailureType]int) string {
	var parts []string
	for _, t := range eval.FailureOrder {
		if m[t] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", t, m[t]))
		}
	}
	return strings.Join(parts, ", ")
}

// WriteMarkdown renders the report; maxFailures bounds the failure list.
func (rep Report) WriteMarkdown(w io.Writer, maxFailures int) {
	_, _ = fmt.Fprintf(w, "# Voice bench (tier 2), %s\n\n", rep.Started.UTC().Format("2006-01-02 15:04Z"))
	keys := make([]string, 0, len(rep.Setup))
	for k := range rep.Setup {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "- %s: %s\n", k, rep.Setup[k])
	}
	t := rep.Total
	_, _ = fmt.Fprintln(w)
	rep.NorthStar.WriteMarkdown(w)
	_, _ = fmt.Fprintf(w, "**%d runs (%d scenarios × %d): pass %d, fail %d, errored %d, not run %d — pass rate %s** (errored and not-run runs excluded). %d min wall. Estimated spend $%.2f (%s).\n\n", t.Runs, len(rep.Cases), rep.Repeat, t.Pass, t.Fail, t.Errored, t.NotRun, rate(t.PassRate), rep.DurationS/60, rep.Spend.TotalUSD, rep.Spend.Summary())
	_, _ = fmt.Fprintf(w, "## By category\n\n| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |\n|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Categories {
		_, _ = fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %s | %s |\n", c.Case, c.Runs, c.Pass, c.Fail, c.Errored, rate(c.PassRate), labels(c.Primary))
	}
	_, _ = fmt.Fprintf(w, "\n## By scenario\n\n| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		_, _ = fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %s | %s | %.0f | %.3f |\n", c.Case, c.Runs, c.Pass, c.Fail, c.Errored, rate(c.PassRate), labels(c.Primary), float64(c.MeanDurationMS)/1000, c.CostUSD)
	}
	_, _ = fmt.Fprintf(w, "\n## Failure taxonomy\n\nPrimary label per failed or errored run: %s. Every label of every run: %s.\n", orStr(labels(t.Primary), "none"), orStr(labels(t.Labels), "none"))
	_, _ = fmt.Fprintf(w, "\n## Latency (turn.latency, ms)\n\n| Segment | N | p50 | p90 | max |\n|---|---|---|---|---|\n")
	for _, l := range rep.Latency {
		_, _ = fmt.Fprintf(w, "| %s | %d | %.0f | %.0f | %.0f |\n", l.Segment, l.N, l.P50, l.P90, l.Max)
	}
	if len(rep.Outcomes) > 0 {
		var o []string
		for k, v := range rep.Outcomes {
			o = append(o, fmt.Sprintf("%s %d", k, v))
		}
		sort.Strings(o)
		_, _ = fmt.Fprintf(w, "\nOutcomes: %s.\n", strings.Join(o, ", "))
	}
	v := rep.Voice
	_, _ = fmt.Fprintf(w, "\n## Voice layer\n\n- user lines: %d, of which the voice model heard nothing of %d.\n", v.Lines, v.NotHeard)
	_, _ = fmt.Fprintf(w, "- Mean WER of what the voice model heard: %s; by condition: %s.\n", fmtMap(v.WERByLang), fmtMap(v.WERByNoise))
	_, _ = fmt.Fprintf(w, "- Daemon flushes %d, phone barge-in cuts %d, interrupted %d, stalled %d.\n", v.Flushes, v.LocalFlushes, v.Interrupted, v.Stalled)
	_, _ = fmt.Fprintf(w, "- Pushes: unheard %d, re-queued %d, deferred %d, held (no listener) %d. Orchestrator failovers %d.\n", v.Unheard, v.Requeued, v.Deferred, v.Held, v.Failovers)
	_, _ = fmt.Fprintf(w, "- Bot utterances that read a bracketed marker aloud (\"[system event, …]\"): %d; with audio but no transcript: %d.\n", v.MarkupSpoken, v.EmptyBot)
	_, _ = fmt.Fprintf(w, "- user lines rendered by: %s.\n", fmtCounts(v.TTSSources))
	rep.Explain.WriteMarkdown(w)
	_, _ = fmt.Fprintf(w, "\n## Failed runs\n\n")
	n := 0
	for _, r := range rep.Runs {
		if r.Status != eval.RunFail && r.Status != eval.RunError {
			continue
		}
		if n >= maxFailures {
			_, _ = fmt.Fprintf(w, "(%d more in report.json)\n", countFailed(rep.Runs)-n)
			break
		}
		n++
		_, _ = fmt.Fprintf(w, "### %s r%d — %s (%s, %d turns, %.0f s)\n\n", r.Scenario, r.Repeat, r.Primary, r.EndReason, r.Turns, r.DurationS)
		for _, f := range r.Failures {
			_, _ = fmt.Fprintf(w, "- %s: %s\n", f.Type, f.Detail)
		}
		if len(r.Transcript) > 0 {
			_, _ = fmt.Fprintf(w, "\n```text\n%s\n```\n", strings.Join(r.Transcript, "\n"))
		}
		_, _ = fmt.Fprintln(w)
	}
	if n == 0 {
		_, _ = fmt.Fprintln(w, "None.")
	}
}

func countFailed(rs []RunSummary) int {
	n := 0
	for _, r := range rs {
		if r.Status == eval.RunFail || r.Status == eval.RunError {
			n++
		}
	}
	return n
}

func fmtMap(m map[string]float64) string {
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s %.3f", k, v))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "–"
	}
	return strings.Join(parts, ", ")
}

func fmtCounts(m map[string]int) string {
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s %d", k, v))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "–"
	}
	return strings.Join(parts, ", ")
}

// LoadRuns reads the run records a campaign wrote under dir/runs.
func LoadRuns(dir string) ([]Run, error) {
	files, err := filepath.Glob(filepath.Join(dir, "runs", "*.json"))
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var r Run
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		runs = append(runs, r)
	}
	sortRuns(runs)
	return runs, nil
}

// SetNorthStar recomputes the north-star block with each run's event log
// from eventsDir (<id>.r<n>.jsonl).
func (rep *Report) SetNorthStar(runs []Run, scs []Scenario, eventsDir string) {
	rep.NorthStar = northstar.Summarize("2", NorthStarRuns(runs, scs, eventsDir))
}
