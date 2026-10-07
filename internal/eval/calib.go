package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
)

// Judge calibration (Evals first, step 1): transcript segments the user
// labels by hand, the judge's verdicts on the same segments, and their
// agreement per rubric item. The judge sees what the user sees: the
// item's context as facts and its turns as the conversation (not the full
// worker screens a live run gives it).

// CalibFile is docs/eval/calibration/<date>/items.json.
type CalibFile struct {
	Version int          `json:"version"`
	Rubric  []RubricItem `json:"rubric"`
	Items   []CalibItem  `json:"items"`
}

// CalibItem is one transcript segment to label.
type CalibItem struct {
	ID string `json:"id"`
	// Source is the file and time range the segment comes from.
	Source string `json:"source"`
	Lang   string `json:"lang"`
	// Context is what the labeller (and the judge) needs to know: the
	// world facts and what the orchestrator knew.
	Context   string      `json:"context"`
	Turns     []CalibTurn `json:"turns"`
	RubricIDs []string    `json:"rubric_ids"`
}

// CalibTurn is one line of the segment.
type CalibTurn struct {
	// Who is user, voice (the speech the user heard), orchestrator (the
	// supervisor's text; what the user heard when the segment has no
	// voice turns), action (sent to a worker, with its outcome) or event
	// (a worker change, an announcement trigger, a reconnect).
	Who  string `json:"who"`
	T    string `json:"t"`
	Text string `json:"text"`
}

// CalibVerdict is one judge verdict in judge.json.
type CalibVerdict struct {
	ItemID   string `json:"item_id"`
	RubricID string `json:"rubric_id"`
	Verdict  string `json:"verdict"` // pass | fail | na
	Evidence string `json:"evidence"`
	Error    string `json:"error,omitempty"`
}

// CalibLabel is one user label in labels.json.
type CalibLabel struct {
	ItemID   string `json:"item_id"`
	RubricID string `json:"rubric_id"`
	Label    string `json:"label"` // pass | fail | na
	Note     string `json:"note,omitempty"`
}

// calibQuestions rephrase the items whose production question quotes one
// user line, for a whole segment.
var calibQuestions = map[string]string{
	"answered":              "By the end of the segment, did the CEO get what they asked for in it: the question answered, or the requested action carried out and confirmed, or, if that was impossible, a plain statement of that?",
	"explain_depth_more":    "Each time the CEO asked for more (\"then?\", \"more detail\"), did Sheepdog's answer add what was asked for?",
	"explain_depth_why":     "Each time the CEO asked why, did Sheepdog's answer give the reason?",
	"explain_depth_example": "Each time the CEO asked for an example, did Sheepdog's answer give one?",
	"explain_padding":       "Each time the CEO asked a simple question, was Sheepdog's answer the direct answer at the right size?",
}

// CalibRubric is the rubric the calibration set is labelled on: the
// default items and the explanation items (without the per-scenario key
// point item), with the production pass/fail definitions.
func CalibRubric() []RubricItem {
	var out []RubricItem
	for _, r := range append(DefaultRubric(), ExplainRubric()...) {
		if r.ID == explainPointItem.ID {
			continue
		}
		if q, ok := calibQuestions[r.ID]; ok {
			r.Question = q
		}
		out = append(out, r)
	}
	return out
}

var calibWho = map[string]bool{"user": true, "voice": true, "orchestrator": true, "action": true, "event": true}

// LoadCalibFile reads and checks an items file.
func LoadCalibFile(path string) (CalibFile, error) {
	var f CalibFile
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, f.validate()
}

func (f CalibFile) validate() error {
	if f.Version != 1 {
		return fmt.Errorf("version %d: want 1", f.Version)
	}
	known := map[string]bool{}
	for _, r := range f.Rubric {
		if r.ID == "" || known[r.ID] {
			return fmt.Errorf("rubric id %q empty or repeated", r.ID)
		}
		known[r.ID] = true
	}
	seen := map[string]bool{}
	for _, it := range f.Items {
		if it.ID == "" || seen[it.ID] {
			return fmt.Errorf("item id %q empty or repeated", it.ID)
		}
		seen[it.ID] = true
		if it.Source == "" || it.Context == "" || len(it.Turns) == 0 || len(it.RubricIDs) == 0 {
			return fmt.Errorf("item %s: source, context, turns and rubric_ids are required", it.ID)
		}
		if it.Lang != "en" && it.Lang != "zh" && it.Lang != "mixed" {
			return fmt.Errorf("item %s: lang %q: want en, zh or mixed", it.ID, it.Lang)
		}
		for i, t := range it.Turns {
			if !calibWho[t.Who] || strings.TrimSpace(t.Text) == "" {
				return fmt.Errorf("item %s turn %d: who %q or empty text", it.ID, i, t.Who)
			}
		}
		for _, id := range it.RubricIDs {
			if !known[id] {
				return fmt.Errorf("item %s: rubric id %q is not in the rubric", it.ID, id)
			}
		}
	}
	return nil
}

// Transcript renders the item's turns for the judge. With voice turns
// the voice is what the user heard and the orchestrator's text is marked
// as not heard; without them (tier 1) the orchestrator's text is what
// the user heard.
func (it CalibItem) Transcript() string {
	hasVoice := false
	for _, t := range it.Turns {
		if t.Who == "voice" {
			hasVoice = true
		}
	}
	var b strings.Builder
	for _, t := range it.Turns {
		var who string
		switch t.Who {
		case "user":
			who = "CEO"
		case "voice":
			who = "Sheepdog (voice, heard by the CEO)"
		case "orchestrator":
			who = "Sheepdog"
			if hasVoice {
				who = "Orchestrator (text to the voice model, not heard by the CEO)"
			}
		case "action":
			who = "ACTION (sent to a worker)"
		case "event":
			who = "EVENT"
		}
		_, _ = fmt.Fprintf(&b, "[%s] %s: %s\n", t.T, who, t.Text)
	}
	return b.String()
}

// JudgeCalib runs the judge on every (item, rubric id) pair, one call
// each, with at most parallel calls in flight (they are independent).
// A judge yes is pass and a no is fail; the judge never says na.
func JudgeCalib(ctx context.Context, j Judge, f CalibFile, parallel int, progress func(done, total int)) []CalibVerdict {
	rubric := map[string]RubricItem{}
	for _, r := range f.Rubric {
		rubric[r.ID] = r
	}
	type job struct {
		item CalibItem
		rid  string
	}
	var jobs []job
	for _, it := range f.Items {
		for _, id := range it.RubricIDs {
			jobs = append(jobs, job{it, id})
		}
	}
	if parallel < 1 {
		parallel = 1
	}
	out := make([]CalibVerdict, len(jobs))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for i, jb := range jobs {
		wg.Add(1)
		go func(i int, jb job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cv := CalibVerdict{ItemID: jb.item.ID, RubricID: jb.rid}
			v, err := j.Judge(ctx, JudgeInput{Criterion: rubric[jb.rid].Criterion(), Transcript: jb.item.Transcript(), Facts: jb.item.Context, Final: true})
			switch {
			case err != nil:
				cv.Error = firstLine(err.Error())
			case v.Answer == AnswerYes:
				cv.Verdict = "pass"
			default:
				cv.Verdict = "fail"
			}
			cv.Evidence = v.Evidence
			out[i] = cv
			if progress != nil {
				mu.Lock()
				done++
				progress(done, len(jobs))
				mu.Unlock()
			}
		}(i, jb)
	}
	wg.Wait()
	return out
}

// RubricAgreement is the judge–user agreement on one rubric item.
type RubricAgreement struct {
	RubricID string `json:"rubric_id"`
	// N is the pairs where both said pass or fail; Agree how many match.
	N     int `json:"n"`
	Agree int `json:"agree"`
	// Kappa is Cohen's kappa over those pairs (nil when undefined: no
	// pairs, or both raters gave one label only and agree on it).
	Kappa *float64 `json:"kappa,omitempty"`
	// CEONA counts labels of na where the judge gave a verdict: the item
	// was asked of a segment where the user thinks it does not apply.
	CEONA int `json:"ceo_na"`
	// Missing counts labels with no judge verdict (or the reverse).
	Missing int `json:"missing"`
	// Lenient counts judge passes on a fail label, Strict judge fails on
	// a pass label (the failure directions).
	Lenient int `json:"judge_lenient"`
	Strict  int `json:"judge_strict"`
}

// ItemAgreement is the agreement on one segment.
type ItemAgreement struct {
	ItemID string `json:"item_id"`
	N      int    `json:"n"`
	Agree  int    `json:"agree"`
}

// Disagreement is one pair where the user and the judge differ.
type Disagreement struct {
	ItemID   string `json:"item_id"`
	RubricID string `json:"rubric_id"`
	Label    string `json:"label"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence,omitempty"`
	Note     string `json:"note,omitempty"`
}

// CalibAgreement is the agreement report.
type CalibAgreement struct {
	Overall       RubricAgreement   `json:"overall"`
	PerRubric     []RubricAgreement `json:"per_rubric"`
	PerItem       []ItemAgreement   `json:"per_item"`
	Disagreements []Disagreement    `json:"disagreements"`
}

// Agreement compares the user's labels with the judge's verdicts. Pairs
// where either side says na are left out of agreement and kappa (an na
// from the user is counted per item as CEONA; the judge never says na).
func Agreement(labels []CalibLabel, verdicts []CalibVerdict) CalibAgreement {
	key := func(i, r string) string { return i + "\x00" + r }
	jv := map[string]CalibVerdict{}
	for _, v := range verdicts {
		jv[key(v.ItemID, v.RubricID)] = v
	}
	type pair struct{ a, b string }
	perRubric := map[string][]pair{}
	perItem := map[string]*ItemAgreement{}
	rep := CalibAgreement{}
	stats := map[string]*RubricAgreement{}
	get := func(id string) *RubricAgreement {
		if stats[id] == nil {
			stats[id] = &RubricAgreement{RubricID: id}
		}
		return stats[id]
	}
	var all []pair
	labelled := map[string]bool{}
	for _, l := range labels {
		k := key(l.ItemID, l.RubricID)
		labelled[k] = true
		s := get(l.RubricID)
		v, ok := jv[k]
		if !ok || v.Verdict == "" {
			s.Missing++
			continue
		}
		if l.Label == "na" || v.Verdict == "na" {
			if l.Label == "na" {
				s.CEONA++
			}
			continue
		}
		p := pair{l.Label, v.Verdict}
		perRubric[l.RubricID] = append(perRubric[l.RubricID], p)
		all = append(all, p)
		ia := perItem[l.ItemID]
		if ia == nil {
			ia = &ItemAgreement{ItemID: l.ItemID}
			perItem[l.ItemID] = ia
		}
		ia.N++
		if p.a == p.b {
			ia.Agree++
		} else {
			rep.Disagreements = append(rep.Disagreements, Disagreement{ItemID: l.ItemID, RubricID: l.RubricID, Label: l.Label, Verdict: v.Verdict, Evidence: v.Evidence, Note: l.Note})
		}
	}
	for _, v := range verdicts {
		if !labelled[key(v.ItemID, v.RubricID)] {
			get(v.RubricID).Missing++
		}
	}
	fill := func(s *RubricAgreement, ps []pair) {
		s.N = len(ps)
		var a, b []string
		for _, p := range ps {
			switch {
			case p.a == p.b:
				s.Agree++
			case p.b == "pass":
				s.Lenient++
			default:
				s.Strict++
			}
			a, b = append(a, p.a), append(b, p.b)
		}
		s.Kappa = CohenKappa(a, b)
	}
	ids := make([]string, 0, len(stats))
	for id := range stats {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := stats[id]
		fill(s, perRubric[id])
		rep.PerRubric = append(rep.PerRubric, *s)
		rep.Overall.CEONA += s.CEONA
		rep.Overall.Missing += s.Missing
	}
	rep.Overall.RubricID = "all"
	fill(&rep.Overall, all)
	for _, ia := range perItem {
		rep.PerItem = append(rep.PerItem, *ia)
	}
	sort.Slice(rep.PerItem, func(i, j int) bool { return rep.PerItem[i].ItemID < rep.PerItem[j].ItemID })
	return rep
}

// CohenKappa is Cohen's kappa of two raters' labels (same length); nil
// when there are no pairs or the chance agreement is 1 (both raters used
// one and the same label throughout).
func CohenKappa(a, b []string) *float64 {
	n := len(a)
	if n == 0 || n != len(b) {
		return nil
	}
	ca, cb := map[string]int{}, map[string]int{}
	agree := 0
	for i := range a {
		ca[a[i]]++
		cb[b[i]]++
		if a[i] == b[i] {
			agree++
		}
	}
	po := float64(agree) / float64(n)
	pe := 0.0
	for k, x := range ca {
		pe += float64(x) / float64(n) * float64(cb[k]) / float64(n)
	}
	if math.Abs(1-pe) < 1e-12 {
		return nil
	}
	k := (po - pe) / (1 - pe)
	return &k
}

// WriteAgreement prints the report as Markdown.
func WriteAgreement(w io.Writer, rep CalibAgreement) {
	kappa := func(k *float64) string {
		if k == nil {
			return "–"
		}
		return fmt.Sprintf("%.2f", *k)
	}
	pct := func(a, n int) string {
		if n == 0 {
			return "–"
		}
		return fmt.Sprintf("%d/%d (%.0f %%)", a, n, 100*float64(a)/float64(n))
	}
	_, _ = fmt.Fprintf(w, "# Judge–CEO agreement\n\n")
	_, _ = fmt.Fprintf(w, "Overall: agreement %s, Cohen's kappa %s; CEO n/a %d, unpaired %d.\n\n", pct(rep.Overall.Agree, rep.Overall.N), kappa(rep.Overall.Kappa), rep.Overall.CEONA, rep.Overall.Missing)
	_, _ = fmt.Fprintf(w, "Judge too lenient (passes a fail label) %d, too strict (fails a pass label) %d.\n\n", rep.Overall.Lenient, rep.Overall.Strict)
	_, _ = fmt.Fprintf(w, "| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |\n|---|---|---|---|---|---|---|\n")
	for _, s := range rep.PerRubric {
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %d | %d | %d | %d |\n", s.RubricID, pct(s.Agree, s.N), kappa(s.Kappa), s.Lenient, s.Strict, s.CEONA, s.Missing)
	}
	_, _ = fmt.Fprintf(w, "\n| Item | Agreement |\n|---|---|\n")
	for _, s := range rep.PerItem {
		_, _ = fmt.Fprintf(w, "| %s | %s |\n", s.ItemID, pct(s.Agree, s.N))
	}
	if len(rep.Disagreements) > 0 {
		_, _ = fmt.Fprintf(w, "\n## Disagreements\n\n| Item | Rubric item | CEO | Judge | Judge's evidence | CEO's note |\n|---|---|---|---|---|---|\n")
		for _, d := range rep.Disagreements {
			_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", d.ItemID, d.RubricID, d.Label, d.Verdict, cell(d.Evidence), cell(d.Note))
		}
	}
}

func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}
