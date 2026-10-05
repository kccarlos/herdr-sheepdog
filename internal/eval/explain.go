package eval

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Explanation evals (handover 2026-10-04, "Next" step 1). A scenario
// with an explain block has a fixed script whose turns are labelled
// (explain, then, more, why, example, simple) and lists the key points
// of substance its world holds (reasons, numbers, examples,
// consequences, options). After the call, the judge model scores two
// sides separately so a report shows where detail is lost:
//
//   - the orchestrator: the slow brain's text for each question (the
//     spoken and detail of every to_supervisor result, and its pushes);
//   - the voice: what the voice model actually said (tier 2 only; at
//     tier 1 there is no voice model and the orchestrator's text is
//     what the CEO gets).
//
// Items: coverage (which key points each side stated, one yes/no call
// per point and answer), ear comprehension (could a listener who cannot
// see the screen restate what it is, why it matters and what can be
// done: three calls per side), follow-up depth (on then/more/why/example
// the answer adds content or hands off; rephrasing only fails) and no
// padding on the simple controls. Depth and padding failures are
// judge_no failures of the run; coverage, comprehension and words per
// answer are metrics.

// Explain turn kinds.
const (
	ExplainKindExplain = "explain"
	ExplainKindThen    = "then"
	ExplainKindMore    = "more"
	ExplainKindWhy     = "why"
	ExplainKindExample = "example"
	ExplainKindSimple  = "simple"
)

// ExplainSpec is a scenario's explain block.
type ExplainSpec struct {
	// Turns labels each script line with what it asks for.
	Turns []string `json:"turns"`
	// KeyPoints are the substance the world holds that a full
	// explanation carries (required when any turn is not simple).
	KeyPoints []string `json:"key_points,omitempty"`
}

func (e *ExplainSpec) validate(s SimScenario) error {
	if !s.Fixed {
		return fmt.Errorf("needs a fixed script (each turn's kind must be known)")
	}
	if len(e.Turns) != len(s.Script) {
		return fmt.Errorf("%d turns for %d script lines", len(e.Turns), len(s.Script))
	}
	if e.substantive() && len(e.KeyPoints) == 0 {
		return fmt.Errorf("key_points are required when a turn is not simple")
	}
	for _, k := range e.Turns {
		switch k {
		case ExplainKindExplain, ExplainKindThen, ExplainKindMore, ExplainKindWhy, ExplainKindExample, ExplainKindSimple:
		default:
			return fmt.Errorf("turn kind %q: want explain, then, more, why, example or simple", k)
		}
	}
	return nil
}

func (e *ExplainSpec) substantive() bool {
	for _, k := range e.Turns {
		if k != ExplainKindSimple {
			return true
		}
	}
	return false
}

// Kind is the kind of the CEO's turn-th line (1-based); "" past the
// script.
func (e *ExplainSpec) Kind(turn int) string {
	if e == nil || turn < 1 || turn > len(e.Turns) {
		return ""
	}
	return e.Turns[turn-1]
}

func isFollowUp(kind string) bool {
	switch kind {
	case ExplainKindThen, ExplainKindMore, ExplainKindWhy, ExplainKindExample:
		return true
	}
	return false
}

// ExplainExchange is one CEO question and what came back before the
// next one.
type ExplainExchange struct {
	Turn     int    `json:"turn"`
	AtMS     int64  `json:"at_ms"`
	Kind     string `json:"kind"`
	Question string `json:"question"`
	// Orchestrator is the orchestrator's text for this question, in
	// order: each to_supervisor result's spoken and detail (and
	// options), and its pushes.
	Orchestrator string `json:"orchestrator,omitempty"`
	// Spoken is the spoken part of the orchestrator's text alone (its
	// detail is background for the voice, not a line to say): what the
	// orchestrator's padding is judged on.
	Spoken string `json:"orchestrator_spoken,omitempty"`
	// Consulted: the voice asked the orchestrator about this question
	// (always at tier 1).
	Consulted bool `json:"consulted"`
	// Voice is what the voice model said (tier 2).
	Voice string `json:"voice,omitempty"`
}

// ExplainSide is one side's scores.
type ExplainSide struct {
	// Coverage is the share of the scenario's key points the side
	// stated anywhere in the call.
	Coverage *float64 `json:"coverage,omitempty"`
	// OfOrchestrator (voice side): of the key points the orchestrator
	// gave the voice, the share the voice said.
	OfOrchestrator *float64 `json:"of_orchestrator,omitempty"`
	// Points lists, per exchange, the key points (0-based) the side
	// stated in that answer.
	Points [][]int `json:"points,omitempty"`
	// Comprehension is the share of what / why / what-can-be-done a
	// listener could restate from this side's lines.
	Comprehension *float64 `json:"comprehension,omitempty"`
	DepthAsked    int      `json:"depth_asked"`
	DepthPassed   int      `json:"depth_passed"`
	PaddingAsked  int      `json:"padding_asked"`
	PaddingPassed int      `json:"padding_passed"`
	// Words is each answer's length (Latin words plus Han characters),
	// per exchange (-1: no text on this side).
	Words []int `json:"words"`
	// Verdicts are the depth, padding and comprehension verdicts (the
	// per-point coverage verdicts are summarised in Points).
	Verdicts []NamedVerdict `json:"verdicts,omitempty"`
	// Errors counts judge calls that failed.
	Errors int `json:"errors,omitempty"`
}

// ExplainScore is a run's explanation scores.
type ExplainScore struct {
	Language     string            `json:"language"`
	Exchanges    []ExplainExchange `json:"exchanges"`
	Orchestrator ExplainSide       `json:"orchestrator"`
	// Voice is nil at tier 1 (no voice model).
	Voice *ExplainSide `json:"voice,omitempty"`
	// NotConsulted counts the questions the voice answered without
	// asking the orchestrator.
	NotConsulted int `json:"not_consulted"`
	// FollowUpsNotConsulted counts the follow-ups among them.
	FollowUpsNotConsulted int `json:"follow_ups_not_consulted"`
}

// Failures are the run's explanation failures: a follow-up that only
// rephrased, a simple answer padded.
func (s *ExplainScore) Failures() []Failure {
	if s == nil {
		return nil
	}
	var fs []Failure
	add := func(side string, sd *ExplainSide) {
		if sd == nil {
			return
		}
		for _, v := range sd.Verdicts {
			if strings.HasPrefix(v.Name, "explain_comprehension") {
				continue
			}
			switch {
			case v.Err != "":
				fs = append(fs, Failure{FailError, "judge " + v.Name + " (" + side + "): " + v.Err})
			case !v.Passed():
				fs = append(fs, Failure{FailJudgeNo, v.Name + " (" + side + "): " + orDefault(firstLine(v.Rationale), "no rationale")})
			}
		}
	}
	add("orchestrator", &s.Orchestrator)
	add("voice", s.Voice)
	return fs
}

// Criteria, each stated in both directions (RubricItem). %s in a
// question is quoted text (the key point, or the CEO's question).
var (
	explainPointItem = RubricItem{
		ID:       "explain_point",
		Question: "Does Sheepdog's answer to the CEO's question state this point, in any language or wording? POINT: %q",
		PassWhen: "the point's substance is there, with its number, reason or example; a translation or paraphrase counts.",
		FailWhen: "the point is missing, only hinted at, or its number, reason or example is left out; OR it is stated wrongly (a changed number, a different cause or example).",
	}
	explainWhatItem = RubricItem{
		ID:       "explain_comprehension_what",
		Question: "Imagine a listener who cannot see any screen and heard only Sheepdog's lines above. Could they restate, in their own words, WHAT the matter is: concretely what the worker found or did, not just its topic?",
		PassWhen: "the lines say concretely what was found or done, in words a listener can retell.",
		FailWhen: "only the topic is named, so there is nothing concrete to retell; OR the substance is there but buried in terms, identifiers or side details so that a listener would lose it.",
	}
	explainWhyItem = RubricItem{
		ID:       "explain_comprehension_why",
		Question: "Imagine a listener who cannot see any screen and heard only Sheepdog's lines above. Could they restate, in their own words, WHY it matters: the cause, and the consequence or risk?",
		PassWhen: "the cause and the consequence or risk are both said in words a listener can retell.",
		FailWhen: "the cause or the consequence is missing, or only asserted ('it is important'); OR they are there but buried in terms, identifiers or side details so that a listener would lose them.",
	}
	explainActionItem = RubricItem{
		ID:       "explain_comprehension_action",
		Question: "Imagine a listener who cannot see any screen and heard only Sheepdog's lines above. Could they restate, in their own words, WHAT CAN BE DONE about it: the options or the recommendation, and roughly what each would take?",
		PassWhen: "the options or the recommendation are said, with roughly what each would take.",
		FailWhen: "no option or recommendation is given, or only 'should I fix it?' with nothing on what fixing means; OR the options are read out as a long list of labels or commands a listener could not keep apart.",
	}
	explainMoreItem = RubricItem{
		ID:       "explain_depth_more",
		Question: "The CEO's last question (%q) asks for more. Did Sheepdog's answer to it add what was asked for?",
		PassWhen: "it adds substantive content not already said in its earlier answers (a new reason, example, number, consequence or step), or says plainly that this is everything it has and offers to find out more, or hands the question to a worker or goes and checks.",
		FailWhen: "it says the same things again in other words with nothing new; OR it repeats the whole earlier answer around a small addition, so the CEO has to sit through it again to find the new part.",
	}
	explainWhyQItem = RubricItem{
		ID:       "explain_depth_why",
		Question: "The CEO's last question (%q) asks why. Did Sheepdog's answer to it give the reason?",
		PassWhen: "it gives a concrete reason (a cause or mechanism, not a restatement of the problem), or says it does not know and offers to find out, or hands the question to a worker.",
		FailWhen: "it repeats the earlier answer in other words without a reason; OR it offers several guessed reasons as if they were fact, or buries the reason in a walk-through of unrelated detail.",
	}
	explainExampleQItem = RubricItem{
		ID:       "explain_depth_example",
		Question: "The CEO's last question (%q) asks for an example. Did Sheepdog's answer to it give one?",
		PassWhen: "it gives a concrete example (a specific case, input, number or event), or says it has none and offers to find out, or hands the question to a worker.",
		FailWhen: "a general restatement without a specific case; OR code, a pattern or a long list of cases read out instead of one case a listener can picture.",
	}
	explainPaddingItem = RubricItem{
		ID:       "explain_padding",
		Question: "The CEO's last question (%q) is a simple one. Was Sheepdog's answer to it the direct answer at the right size?",
		PassWhen: "the direct answer, with at most one short extra sentence; a short offer at the end is fine.",
		FailWhen: "too long: it restates the question, adds unasked-for background, details from the screen the CEO did not ask for, or a list of options; OR too thin: no direct answer (only an offer or 'let me check' when the answer was at hand), or the one fact asked for left out.",
	}
)

// ExplainRubric is the explanation items. A run names a depth verdict
// explain_depth_t<turn>_<kind> (then and more both take explain_depth_more)
// and a padding verdict explain_padding_t<turn>.
func ExplainRubric() []RubricItem {
	return []RubricItem{explainPointItem, explainWhatItem, explainWhyItem, explainActionItem, explainMoreItem, explainWhyQItem, explainExampleQItem, explainPaddingItem}
}

var (
	explainPointCriterion = explainPointItem.Criterion()
	explainWhat           = explainWhatItem.Criterion()
	explainWhy            = explainWhyItem.Criterion()
	explainAction         = explainActionItem.Criterion()
	explainMore           = explainMoreItem.Criterion()
	explainWhyQ           = explainWhyQItem.Criterion()
	explainExampleQ       = explainExampleQItem.Criterion()
	explainPadding        = explainPaddingItem.Criterion()
)

// ScoreExplain scores a finished call's exchanges (voice: the voice side
// is scored too). The judge is asked each item as a final yes/no.
func ScoreExplain(ctx context.Context, j Judge, spec *ExplainSpec, lang string, ex []ExplainExchange, voice bool) *ExplainScore {
	if spec == nil {
		return nil
	}
	sc := &ExplainScore{Language: orDefault(lang, "en"), Exchanges: ex}
	for _, e := range ex {
		if !e.Consulted {
			sc.NotConsulted++
			if isFollowUp(e.Kind) {
				sc.FollowUpsNotConsulted++
			}
		}
	}
	type side struct {
		name string
		text func(ExplainExchange) string
		out  *ExplainSide
	}
	sides := []side{{"orchestrator", func(e ExplainExchange) string { return e.Orchestrator }, &sc.Orchestrator}}
	if voice {
		sc.Voice = &ExplainSide{}
		sides = append(sides, side{"voice", func(e ExplainExchange) string { return e.Voice }, sc.Voice})
	}

	// Every judge call, then the scores.
	type job struct {
		in            JudgeInput
		side          *ExplainSide
		name          string
		point, answer int // coverage: key point and exchange; -1 otherwise
	}
	var jobs []job
	for _, sd := range sides {
		sd.out.Points = make([][]int, len(ex))
		for i, e := range ex {
			t := strings.TrimSpace(sd.text(e))
			if t == "" {
				sd.out.Words = append(sd.out.Words, -1)
			} else {
				sd.out.Words = append(sd.out.Words, CountWords(t))
			}
			if t != "" && e.Kind != ExplainKindSimple {
				one := renderExchanges(ex[i:i+1], sd.text)
				for p, kp := range spec.KeyPoints {
					jobs = append(jobs, job{in: JudgeInput{Criterion: fmt.Sprintf(explainPointCriterion, kp), Transcript: one, Final: true}, side: sd.out, point: p, answer: i})
				}
			}
			var crit string
			switch e.Kind {
			case ExplainKindThen, ExplainKindMore:
				crit = explainMore
			case ExplainKindWhy:
				crit = explainWhyQ
			case ExplainKindExample:
				crit = explainExampleQ
			case ExplainKindSimple:
				crit = explainPadding
			}
			// The orchestrator is judged only on the questions it was
			// asked; the voice on every one.
			if crit == "" || (sd.name == "orchestrator" && !e.Consulted) {
				continue
			}
			name := fmt.Sprintf("explain_depth_t%d_%s", e.Turn, e.Kind)
			if e.Kind == ExplainKindSimple {
				name = fmt.Sprintf("explain_padding_t%d", e.Turn)
			}
			text := sd.text
			if e.Kind == ExplainKindSimple && sd.name == "orchestrator" {
				text = func(e ExplainExchange) string { return orDefault(e.Spoken, e.Orchestrator) }
			}
			jobs = append(jobs, job{in: JudgeInput{Criterion: fmt.Sprintf(crit, e.Question), Transcript: renderExchanges(ex[:i+1], text), Final: true}, side: sd.out, name: name, point: -1})
		}
		if spec.substantive() {
			// Only the answers this side gave: a trailing question it was
			// not asked must not read as silence.
			var answered []ExplainExchange
			for _, e := range ex {
				if strings.TrimSpace(sd.text(e)) != "" {
					answered = append(answered, e)
				}
			}
			all := renderExchanges(answered, sd.text)
			for k, c := range map[string]string{"what": explainWhat, "why": explainWhy, "action": explainAction} {
				jobs = append(jobs, job{in: JudgeInput{Criterion: c, Transcript: all, Final: true}, side: sd.out, name: "explain_comprehension_" + k, point: -1})
			}
		}
	}
	verdicts := make([]Verdict, len(jobs))
	var wg sync.WaitGroup
	for i, jb := range jobs {
		wg.Add(1)
		go func(i int, in JudgeInput) {
			defer wg.Done()
			v, err := j.Judge(ctx, in)
			if err != nil {
				v = Verdict{Criterion: in.Criterion, Err: firstLine(err.Error())}
			}
			verdicts[i] = v
		}(i, jb.in)
	}
	wg.Wait()

	compYes, compN := map[*ExplainSide]int{}, map[*ExplainSide]int{}
	for i, jb := range jobs {
		v := verdicts[i]
		if v.Err != "" {
			jb.side.Errors++
		}
		if jb.point >= 0 {
			if v.Passed() {
				jb.side.Points[jb.answer] = append(jb.side.Points[jb.answer], jb.point)
			}
			continue
		}
		jb.side.Verdicts = append(jb.side.Verdicts, NamedVerdict{Name: jb.name, Verdict: v})
		switch {
		case strings.HasPrefix(jb.name, "explain_comprehension"):
			if v.Err == "" {
				compN[jb.side]++
				if v.Passed() {
					compYes[jb.side]++
				}
			}
		case strings.HasPrefix(jb.name, "explain_padding"):
			jb.side.PaddingAsked++
			if v.Passed() {
				jb.side.PaddingPassed++
			}
		default:
			jb.side.DepthAsked++
			if v.Passed() {
				jb.side.DepthPassed++
			}
		}
	}
	for _, sd := range sides {
		o := sd.out
		sort.SliceStable(o.Verdicts, func(a, b int) bool { return o.Verdicts[a].Name < o.Verdicts[b].Name })
		for i := range o.Points {
			sort.Ints(o.Points[i])
		}
		if n := compN[o]; n > 0 {
			o.Comprehension = ptr(float64(compYes[o]) / float64(n))
		}
		if k := len(spec.KeyPoints); k > 0 && spec.substantive() {
			o.Coverage = ptr(float64(len(union(o.Points))) / float64(k))
		}
	}
	if sc.Voice != nil && spec.substantive() {
		given := union(sc.Orchestrator.Points)
		said := map[int]bool{}
		for _, p := range union(sc.Voice.Points) {
			said[p] = true
		}
		if len(given) > 0 {
			n := 0
			for _, p := range given {
				if said[p] {
					n++
				}
			}
			sc.Voice.OfOrchestrator = ptr(float64(n) / float64(len(given)))
		}
	}
	return sc
}

func ptr(f float64) *float64 { return &f }

func union(pp [][]int) []int {
	seen := map[int]bool{}
	var out []int
	for _, ps := range pp {
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Ints(out)
	return out
}

// renderExchanges is a judge transcript of exchanges, with this side's
// text as Sheepdog's answers. On the orchestrator side a question it was
// never asked is left out (except the last one), so its answers read as
// one conversation.
func renderExchanges(ex []ExplainExchange, text func(ExplainExchange) string) string {
	var b strings.Builder
	for i, e := range ex {
		if i < len(ex)-1 && !e.Consulted && strings.TrimSpace(e.Orchestrator) == "" && strings.TrimSpace(text(e)) == "" {
			continue
		}
		b.WriteString("CEO: " + e.Question + "\n")
		b.WriteString("Sheepdog: " + orDefault(strings.TrimSpace(text(e)), "(says nothing)") + "\n")
	}
	return b.String()
}

// CountWords counts Latin words (runs of letters or digits) and Han
// characters, each one word.
func CountWords(s string) int {
	n := 0
	in := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			n++
			in = false
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’':
			if !in {
				n++
				in = true
			}
		default:
			in = false
		}
	}
	return n
}

// JoinOrchestrator is one to_supervisor result as the orchestrator's
// text: spoken, then detail, then the options.
func JoinOrchestrator(spoken, detail string, options []string) string {
	var parts []string
	for _, s := range []string{spoken, detail} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	if len(options) > 0 {
		parts = append(parts, "Options: "+strings.Join(options, "; ")+".")
	}
	return strings.Join(parts, " ")
}

// ExplainSummary aggregates explanation scores over runs.
type ExplainSummary struct {
	Runs                  int                 `json:"runs"`
	Questions             int                 `json:"questions"`
	NotConsulted          int                 `json:"not_consulted"`
	FollowUps             int                 `json:"follow_ups"`
	FollowUpsNotConsulted int                 `json:"follow_ups_not_consulted"`
	Orchestrator          ExplainSideSummary  `json:"orchestrator"`
	Voice                 *ExplainSideSummary `json:"voice,omitempty"`
}

// ExplainSideSummary is one side over runs.
type ExplainSideSummary struct {
	Coverage       *float64 `json:"coverage_mean,omitempty"`
	OfOrchestrator *float64 `json:"of_orchestrator_mean,omitempty"`
	Comprehension  *float64 `json:"comprehension_mean,omitempty"`
	// Words per answer, by the language of the call: substantive answers
	// and the simple controls' answers (Han characters count as words).
	WordsEN       *float64 `json:"words_per_answer_en,omitempty"`
	WordsZH       *float64 `json:"words_per_answer_zh,omitempty"`
	SimpleWordsEN *float64 `json:"simple_words_en,omitempty"`
	SimpleWordsZH *float64 `json:"simple_words_zh,omitempty"`
	DepthAsked    int      `json:"depth_asked"`
	DepthPassed   int      `json:"depth_passed"`
	PaddingAsked  int      `json:"padding_asked"`
	PaddingPassed int      `json:"padding_passed"`
}

type meanAcc struct {
	sum float64
	n   int
}

func (m *meanAcc) add(f float64) { m.sum += f; m.n++ }
func (m *meanAcc) addp(f *float64) {
	if f != nil {
		m.add(*f)
	}
}
func (m meanAcc) mean() *float64 {
	if m.n == 0 {
		return nil
	}
	return ptr(m.sum / float64(m.n))
}

// SummarizeExplain aggregates scores (nil entries skipped); nil when
// there are none.
func SummarizeExplain(scores []*ExplainScore) *ExplainSummary {
	var s ExplainSummary
	var accs [2][7]meanAcc
	voice := false
	for _, x := range scores {
		if x == nil {
			continue
		}
		s.Runs++
		s.NotConsulted += x.NotConsulted
		s.FollowUpsNotConsulted += x.FollowUpsNotConsulted
		for _, e := range x.Exchanges {
			s.Questions++
			if isFollowUp(e.Kind) {
				s.FollowUps++
			}
		}
		sides := []*ExplainSide{&x.Orchestrator, x.Voice}
		for k, sd := range sides {
			if sd == nil {
				continue
			}
			if k == 1 {
				voice = true
			}
			a := &accs[k]
			a[0].addp(sd.Coverage)
			a[1].addp(sd.OfOrchestrator)
			a[2].addp(sd.Comprehension)
			for i, w := range sd.Words {
				if w < 0 || i >= len(x.Exchanges) {
					continue
				}
				simple := x.Exchanges[i].Kind == ExplainKindSimple
				switch {
				case x.Language == "zh" && simple:
					a[6].add(float64(w))
				case x.Language == "zh":
					a[4].add(float64(w))
				case simple:
					a[5].add(float64(w))
				default:
					a[3].add(float64(w))
				}
			}
		}
		s.Orchestrator.DepthAsked += x.Orchestrator.DepthAsked
		s.Orchestrator.DepthPassed += x.Orchestrator.DepthPassed
		s.Orchestrator.PaddingAsked += x.Orchestrator.PaddingAsked
		s.Orchestrator.PaddingPassed += x.Orchestrator.PaddingPassed
		if x.Voice != nil {
			if s.Voice == nil {
				s.Voice = &ExplainSideSummary{}
			}
			s.Voice.DepthAsked += x.Voice.DepthAsked
			s.Voice.DepthPassed += x.Voice.DepthPassed
			s.Voice.PaddingAsked += x.Voice.PaddingAsked
			s.Voice.PaddingPassed += x.Voice.PaddingPassed
		}
	}
	if s.Runs == 0 {
		return nil
	}
	fill := func(o *ExplainSideSummary, a [7]meanAcc) {
		o.Coverage, o.OfOrchestrator, o.Comprehension = a[0].mean(), a[1].mean(), a[2].mean()
		o.WordsEN, o.WordsZH, o.SimpleWordsEN, o.SimpleWordsZH = a[3].mean(), a[4].mean(), a[5].mean(), a[6].mean()
	}
	fill(&s.Orchestrator, accs[0])
	if voice && s.Voice != nil {
		fill(s.Voice, accs[1])
	}
	return &s
}

func fmtMean(f *float64, prec int) string {
	if f == nil {
		return "–"
	}
	return fmt.Sprintf("%.*f", prec, *f)
}

func fmtRatio(a, n int) string {
	if n == 0 {
		return "–"
	}
	return fmt.Sprintf("%d/%d", a, n)
}

// Line is the one-line metric of the report.
func (s *ExplainSummary) Line() string {
	if s == nil {
		return ""
	}
	o := s.Orchestrator
	line := fmt.Sprintf("Explain (%d runs): orchestrator coverage %s, comprehension %s, words per answer en %s / zh %s", s.Runs, fmtMean(o.Coverage, 2), fmtMean(o.Comprehension, 2), fmtMean(o.WordsEN, 0), fmtMean(o.WordsZH, 0))
	if v := s.Voice; v != nil {
		line += fmt.Sprintf("; voice coverage %s (%s of the orchestrator's points), comprehension %s, words per answer en %s / zh %s; %d of %d follow-ups answered without the orchestrator", fmtMean(v.Coverage, 2), fmtMean(v.OfOrchestrator, 2), fmtMean(v.Comprehension, 2), fmtMean(v.WordsEN, 0), fmtMean(v.WordsZH, 0), s.FollowUpsNotConsulted, s.FollowUps)
	}
	return line + "."
}

// WriteMarkdown writes the report's explanation section.
func (s *ExplainSummary) WriteMarkdown(w io.Writer) {
	if s == nil {
		return
	}
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("\n## Explanations\n\n%s\n\n", s.Line())
	p("| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |\n|---|---|---|---|---|---|---|---|---|---|\n")
	row := func(name string, o ExplainSideSummary) {
		p("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", name, fmtMean(o.Coverage, 2), fmtMean(o.OfOrchestrator, 2), fmtMean(o.Comprehension, 2), fmtMean(o.WordsEN, 0), fmtMean(o.WordsZH, 0), fmtMean(o.SimpleWordsEN, 0), fmtMean(o.SimpleWordsZH, 0), fmtRatio(o.DepthPassed, o.DepthAsked), fmtRatio(o.PaddingPassed, o.PaddingAsked))
	}
	row("orchestrator", s.Orchestrator)
	if s.Voice != nil {
		row("voice", *s.Voice)
	}
	p("\nCoverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. %d of %d questions (%d of %d follow-ups) were answered without asking the orchestrator.\n", s.NotConsulted, s.Questions, s.FollowUpsNotConsulted, s.FollowUps)
}
