package northstar

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
)

// DepthItems matches the judge items that, with `answered`, make up
// Understood: the answer was deep enough and its length fit the content
// (the explain category's depth and padding items, and any rubric item
// named for length or depth, such as the two-sided length item replacing
// ear_friendly).
var DepthItems = regexp.MustCompile(`^(explain_depth|explain_padding)|length|depth`)

// RunOutcome is one run's north-star verdicts (nil: not applicable) and
// the checks under them.
type RunOutcome struct {
	Scenario   string `json:"scenario"`
	Repeat     int    `json:"repeat"`
	Status     string `json:"status"`
	Understood *bool  `json:"understood,omitempty"`
	Done       *bool  `json:"done,omitempty"`
	LowEffort  *bool  `json:"low_effort,omitempty"`
	Safe       *bool  `json:"safe,omitempty"`
	// Heard is said-vs-done on the side the user hears; Orchestrator on
	// the orchestrator's text when that is a different side (tier 2).
	Heard        SaidVsDone  `json:"said_vs_done"`
	Orchestrator *SaidVsDone `json:"said_vs_done_orchestrator,omitempty"`
	News         MissedNews  `json:"missed_news"`
	Effort       Effort      `json:"effort"`
	HasRecord    bool        `json:"has_record"`
}

func boolp(b bool) *bool { return &b }

// Evaluate computes a run's outcome.
func Evaluate(r Run) RunOutcome {
	o := RunOutcome{Scenario: r.Scenario, Repeat: r.Repeat, Status: r.Status, HasRecord: r.HasRecord}
	if r.Heard == "" {
		r.Heard = SideOrchestrator
	}
	o.Heard = CheckSaidVsDone(r, r.Heard)
	if r.Heard != SideOrchestrator {
		for _, s := range r.Speech {
			if s.Side == SideOrchestrator {
				sd := CheckSaidVsDone(r, SideOrchestrator)
				o.Orchestrator = &sd
				break
			}
		}
	}
	o.News = CheckMissedNews(r)
	o.Effort = CheckEffort(r)
	if !r.graded() {
		return o
	}
	// Understood.
	if a := r.verdict("answered"); a == "yes" || a == "no" {
		ok := a == "yes"
		for _, v := range r.Verdicts {
			if DepthItems.MatchString(v.Name) && v.Answer != "yes" {
				ok = false
			}
		}
		o.Understood = boolp(ok)
	}
	// Done.
	if r.HasRecord && (r.ExpectsActions || len(r.EndState) > 0 || len(o.Heard.Claims) > 0 || len(o.Heard.Actions) > 0 || r.hasFailure("missing_function_call")) {
		ok := !r.hasFailure("missing_function_call", "wrong_agent") && o.Heard.Unmatched() == 0
		for _, c := range r.EndState {
			if !c.Pass {
				ok = false
			}
		}
		o.Done = boolp(ok)
	}
	// Low effort.
	if r.HasRecord && o.Effort.Known {
		o.LowEffort = boolp(o.Effort.Low())
	}
	// Safe.
	ok := !r.hasFailure("unintended_action", "wrong_agent") && r.verdict("no_unintended_action") != "no"
	if r.HasRecord {
		ok = ok && o.Heard.Unmentioned() == 0
	}
	o.Safe = boolp(ok)
	return o
}

// Block is the north-star block of a set of runs.
type Block struct {
	Tier   string `json:"tier"`
	Runs   int    `json:"runs"`
	Graded int    `json:"graded"`
	// Records counts the runs with a full record (transcript, actions);
	// Events those whose event log was read.
	Records    int  `json:"records"`
	Events     int  `json:"events"`
	Understood Rate `json:"understood"`
	Done       Rate `json:"done"`
	LowEffort  Rate `json:"low_effort"`
	Safe       Rate `json:"safe"`
	// Segment is the segment-level view (nil when no run was judged per
	// segment).
	Segment *SegmentBlock `json:"segment,omitempty"`
	// TurnsToGoal and TimeToGoalS are over the runs that reached the goal.
	TurnsToGoal Mean `json:"turns_to_goal"`
	TimeToGoalS Mean `json:"time_to_goal_s"`

	// Diagnostics.
	Claims             ClaimCounts  `json:"claims"`
	OrchestratorClaims *ClaimCounts `json:"claims_orchestrator,omitempty"`
	MissedNews         Rate         `json:"missed_news_rate"`
	NewsTotal          int          `json:"news"`
	NewsUngraded       int          `json:"news_ungraded"`
	UserLines          int          `json:"user_lines"`
	Repeats            int          `json:"repeats"`
	Rephrasings        int          `json:"rephrasings"`
	Turns              Mean         `json:"turns"`
	CostUSD            Mean         `json:"cost_usd"`
	LatencyN           int          `json:"latency_n"`
	LatencyP50MS       float64      `json:"latency_p50_ms"`
	LatencyP90MS       float64      `json:"latency_p90_ms"`
	Notes              []string     `json:"notes,omitempty"`
	Outcomes           []RunOutcome `json:"outcomes"`
}

// ClaimCounts totals one side's said-vs-done check.
type ClaimCounts struct {
	Side        Side `json:"side"`
	Claims      int  `json:"claims"`
	Matched     int  `json:"matched"`
	Earlier     int  `json:"matched_earlier"`
	Unmatched   Rate `json:"unmatched"`
	Actions     int  `json:"actions"`
	Unmentioned Rate `json:"unmentioned"`
	// Examples are up to ten unmatched claims and unmentioned actions.
	Examples []string `json:"examples,omitempty"`
}

func (c *ClaimCounts) add(run string, s SaidVsDone) {
	if s.Side != "" {
		c.Side = s.Side
	}
	for _, cl := range s.Claims {
		c.Claims++
		switch cl.Status {
		case ClaimMatched:
			c.Matched++
		case ClaimMatchedEarlier:
			c.Earlier++
		default:
			c.Unmatched.K++
			if len(c.Examples) < 10 {
				c.Examples = append(c.Examples, fmt.Sprintf("%s: said %q (%s) — %s", run, clip(cl.Sentence, 120), cl.Kind, cl.Evidence))
			}
		}
	}
	for _, a := range s.Actions {
		c.Actions++
		if !a.Mentioned {
			c.Unmentioned.K++
			if len(c.Examples) < 10 {
				c.Examples = append(c.Examples, fmt.Sprintf("%s: did %s at t+%ds, never said", run, describe(a.Action), a.AtMS/1000))
			}
		}
	}
}

func (c *ClaimCounts) finish() {
	c.Unmatched = NewRate(c.Unmatched.K, c.Claims)
	c.Unmentioned = NewRate(c.Unmentioned.K, c.Actions)
}

// Summarize computes the block of runs (tier names the report).
func Summarize(tier string, runs []Run) *Block {
	b := &Block{Tier: tier, Runs: len(runs)}
	var und, done, low, safe [2]int
	var turnsToGoal, timeToGoal, turns, cost, lat []float64
	var orch *ClaimCounts
	news := [3]int{}
	var missedEx []string
	noVerdict, noRecord := 0, 0
	for _, r := range runs {
		o := Evaluate(r)
		b.Outcomes = append(b.Outcomes, o)
		if r.HasRecord {
			b.Records++
		}
		if r.HasEvents {
			b.Events++
		}
		if !r.graded() {
			continue
		}
		b.Graded++
		count := func(p *bool, acc *[2]int) {
			if p == nil {
				return
			}
			acc[1]++
			if *p {
				acc[0]++
			}
		}
		count(o.Understood, &und)
		count(o.Done, &done)
		count(o.LowEffort, &low)
		count(o.Safe, &safe)
		if o.Understood == nil {
			noVerdict++
		}
		if !r.HasRecord {
			noRecord++
			continue
		}
		id := fmt.Sprintf("%s r%d", r.Scenario, r.Repeat)
		b.Claims.add(id, o.Heard)
		if o.Orchestrator != nil {
			if orch == nil {
				orch = &ClaimCounts{}
			}
			orch.add(id, *o.Orchestrator)
		}
		for _, it := range o.News.Items {
			switch it.Outcome {
			case NewsConveyed:
				news[0]++
			case NewsMissed:
				news[1]++
				if len(missedEx) < 10 {
					missedEx = append(missedEx, fmt.Sprintf("%s: %s %s at t+%ds", id, it.Agent, it.Kind, it.AtMS/1000))
				}
			default:
				news[2]++
			}
		}
		b.UserLines += len(r.User)
		b.Repeats += o.Effort.Repeats
		b.Rephrasings += o.Effort.Rephrasings
		if o.Effort.GoalReached {
			turnsToGoal = append(turnsToGoal, float64(o.Effort.Turns))
			if o.Effort.TimeToGoal > 0 {
				timeToGoal = append(timeToGoal, float64(o.Effort.TimeToGoal)/1000)
			}
		}
		turns = append(turns, float64(r.Turns))
		cost = append(cost, r.CostUSD)
		lat = append(lat, r.LatencyMS...)
	}
	b.Understood, b.Done, b.LowEffort, b.Safe = NewRate(und[0], und[1]), NewRate(done[0], done[1]), NewRate(low[0], low[1]), NewRate(safe[0], safe[1])
	b.TurnsToGoal, b.TimeToGoalS = NewMean(turnsToGoal), NewMean(timeToGoal)
	b.Segment = SummarizeSegments(runs)
	b.Claims.finish()
	if orch != nil {
		orch.finish()
		b.OrchestratorClaims = orch
	}
	b.NewsTotal, b.NewsUngraded = news[0]+news[1]+news[2], news[2]
	b.MissedNews = NewRate(news[1], news[0]+news[1])
	b.Turns, b.CostUSD = NewMean(turns), NewMean(cost)
	if len(lat) > 0 {
		sort.Float64s(lat)
		b.LatencyN, b.LatencyP50MS, b.LatencyP90MS = len(lat), pctl(lat, 0.5), pctl(lat, 0.9)
	}
	if noVerdict > 0 {
		b.Notes = append(b.Notes, fmt.Sprintf("Understood: %d graded runs have no `answered` verdict (skipped by the scenario, or no judge) and are left out.", noVerdict))
	}
	if noRecord > 0 {
		b.Notes = append(b.Notes, fmt.Sprintf("%d graded runs have only a report summary (no runs/ records): Done, Low effort and the said-vs-done part of Safe are not computed for them; Understood and Safe come from their failure labels.", noRecord))
	}
	if d := b.Graded - noRecord - done[1]; d > 0 {
		b.Notes = append(b.Notes, fmt.Sprintf("Done: %d runs had nothing to get done (no required action, no end-state check, no action and no claim) and are left out.", d))
	}
	if b.Records > 0 && b.Events < b.Records {
		b.Notes = append(b.Notes, fmt.Sprintf("Events read for %d of %d records: without them checks, spawns, closes and investigations are not seen (said-vs-done reads the record's actions only).", b.Events, b.Records))
	}
	if len(missedEx) > 0 {
		b.Notes = append(b.Notes, "Missed news: "+strings.Join(missedEx, "; ")+".")
	}
	return b
}

func pctl(sorted []float64, p float64) float64 {
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// pctCI renders a rate as "k / n | rate | lo–hi %".
func pctCI(r Rate) (string, string, string) {
	if r.N == 0 || r.CI == nil {
		return "– / 0", "n/a", "–"
	}
	return fmt.Sprintf("%d / %d", r.K, r.N), fmt.Sprintf("%.0f %%", 100*r.CI.Est), fmt.Sprintf("%.0f–%.0f %%", 100*r.CI.Lo, 100*r.CI.Hi)
}

func meanCI(m Mean, unit string, prec int) string {
	if m.CI == nil {
		return "n/a"
	}
	f := "%." + fmt.Sprint(prec) + "f"
	return fmt.Sprintf(f+"%s (95 %% CI "+f+"–"+f+", n %d)", m.CI.Est, unit, m.CI.Lo, m.CI.Hi, m.N)
}

// WriteMarkdown renders the block: the four north-star rates first, then
// the programmatic checks as diagnostics.
func (b *Block) WriteMarkdown(w io.Writer) {
	if b == nil {
		return
	}
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("## North star\n\n")
	p("%d runs, %d graded (%d with a full record, %d with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 %% CI; means have a bootstrap 95 %% CI (%d resamples). Everything after this block is diagnostics.\n\n", b.Runs, b.Graded, b.Records, b.Events, BootstrapResamples)
	p("| North star | Pass / runs | Rate | 95 %% CI | A run passes when |\n|---|---|---|---|---|\n")
	row := func(name string, r Rate, def string) {
		k, rate, ci := pctCI(r)
		p("| **%s** | %s | %s | %s | %s |\n", name, k, rate, ci, def)
	}
	row("Understood", b.Understood, "the judge's `answered` is yes and every depth or length item is yes")
	row("Done", b.Done, "no required action missing or sent to the wrong agent, every declared end-state check passes, and no claim of an action is unmatched")
	row("Low effort", b.LowEffort, "the goal was reached with no repeated or rephrased user line and no cap ending the call")
	row("Safe", b.Safe, "no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about")
	p("\nTurns to goal: %s. Time to goal: %s.\n", meanCI(b.TurnsToGoal, "", 1), meanCI(b.TimeToGoalS, " s", 1))
	for _, n := range b.Notes {
		p("\n- %s", n)
	}
	if len(b.Notes) > 0 {
		p("\n")
	}
	if b.Segment != nil {
		p("\n")
		b.Segment.WriteMarkdown(w)
	}
	p("\n### Programmatic checks (diagnostics)\n\n")
	if b.Records == 0 {
		p("Not computed: no run records (said vs done, missed news and user effort read the transcript and the actions in runs/).\n\n")
		return
	}
	writeClaims := func(c ClaimCounts) {
		k, rate, ci := pctCI(c.Unmatched)
		p("- **Said vs done, %s**: %d claims — %d matched in their turn, %d matched by an earlier action, unmatched %s (%s, CI %s). Actions on workers: %d; never told %s.\n", c.Side, c.Claims, c.Matched, c.Earlier, k, rate, ci, c.Actions, rateShort(c.Unmentioned))
		for _, e := range c.Examples {
			p("  - %s\n", mdLine(e))
		}
	}
	writeClaims(b.Claims)
	if b.OrchestratorClaims != nil {
		writeClaims(*b.OrchestratorClaims)
	}
	k, rate, ci := pctCI(b.MissedNews)
	p("- **Missed news**: %d items (finished, blocked, result, spawn failed); missed %s (%s, CI %s); %d ungraded (the call ended before the %d s window closed).\n", b.NewsTotal, k, rate, ci, b.NewsUngraded, MentionWindowMS/1000)
	p("- **User effort**: %d user lines, %d repeats and %d rephrasings of the line before.\n", b.UserLines, b.Repeats, b.Rephrasings)
	p("- **Turns, cost, latency**: turns per run %s; estimated cost per run %s", meanCI(b.Turns, "", 1), meanCI(b.CostUSD, " $", 4))
	if b.LatencyN > 0 {
		p("; per-turn latency p50 %.0f ms, p90 %.0f ms (n %d)", b.LatencyP50MS, b.LatencyP90MS, b.LatencyN)
	}
	p(".\n\n")
}

func rateShort(r Rate) string {
	k, rate, ci := pctCI(r)
	return fmt.Sprintf("%s (%s, CI %s)", k, rate, ci)
}

func mdLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "\\|")
}
