package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/genai"
)

// Segment-level judging (handover 2026-10-04, "Evals first": one judge
// call per transcript segment scoring all items, each with a quoted piece
// of transcript as evidence). A workday session is 13–18 agenda beats
// over 15–30 minutes; one verdict over the whole session fails it for one
// slip anywhere. A segment is one agenda beat — the CEO's request (or a
// scripted silence) with every line until the next beat: follow-ups,
// sheepdog's answers and the announcements that land in it — plus the
// announcements before the first beat. Each segment gets one call that
// scores the items that apply to it; the run-level items whose question
// spans the call (news, said vs done, end state, safety) stay run-level.

// SegmentCriterion is one rubric item asked in a segment call.
type SegmentCriterion struct {
	ID        string
	Criterion string
}

// SegmentInput is what the judge sees for one segment.
type SegmentInput struct {
	Criteria []SegmentCriterion
	// Earlier is the conversation before the segment: context for
	// references ("the earlier one"), never scored.
	Earlier string
	// Segment is the part of the conversation to score.
	Segment string
	// Facts is the ground truth (the whole call's).
	Facts string
}

// SegmentJudge scores several criteria about one segment in one call.
type SegmentJudge interface {
	JudgeSegment(ctx context.Context, in SegmentInput) ([]Verdict, error)
}

const segmentSystem = `You are a strict, fair evaluator of a voice assistant called sheepdog. Sheepdog supervises coding agents (workers such as Codex or Claude Code running in terminal panes) for a CEO who talks to it by voice, away from the screen. The CEO hears every sheepdog line; they never see the screens.

You get one SEGMENT of a conversation (usually one request of the CEO and everything said until their next request, or announcements sheepdog made on its own), sometimes the EARLIER conversation for context, ground-truth facts (what the workers' screens really showed and every action the system really sent to a worker, with times), and SEVERAL criteria, each with an id and phrased as a question where "yes" is the good outcome.

Score only the SEGMENT. The earlier conversation is there so you can resolve references ("the earlier one", "what did we discuss first") and know what was already said; never charge a flaw in an earlier line to this segment. Judge each sheepdog line against the facts as they stood at the time it was spoken (facts carry times; a worker screen is a scrolling terminal, lower lines are newer, and later facts supersede earlier ones). Only actions sent during the segment's time span belong to the segment.

Decide each criterion on its own, as if it were the only one asked. Charge each flaw only to the criterion that names it (an action nobody asked for belongs to the action criterion, a wrong or invented fact to the fact criteria, an unclear word to the listening criterion), and do not fail a criterion for a flaw it does not name. Most criteria say when they pass and when they fail, and fail in both directions (too little and too much, doing less and doing more than asked): apply the criterion's own definition, not a general preference for short or long answers.

The segment is OVER: nothing more will be said in it. If a criterion was not met by the end of the segment, the verdict is "no".

Judge what the CEO actually heard, not what sheepdog might have meant.

Evidence, for every criterion: quote, word for word, the shortest piece of the SEGMENT that decided the verdict (one line or part of one, with its speaker); quote the FACTS instead when the deciding point is there (an action that was or was not sent, a screen line). When the verdict rests on something absent, quote the line where it should have been, or write "none:" and say in a few words what is missing.

Answer with JSON only: {"verdicts": [{"id": "<criterion id>", "verdict": "yes"|"no", "confidence": <number between 0 and 1>, "evidence": "<quote>"}, ...]}, one entry per criterion id, every id exactly once.`

var segmentSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"verdicts": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"id":         {Type: genai.TypeString},
					"verdict":    {Type: genai.TypeString, Enum: []string{"yes", "no"}},
					"confidence": {Type: genai.TypeNumber},
					"evidence":   {Type: genai.TypeString},
				},
				Required: []string{"id", "verdict", "confidence", "evidence"},
			},
		},
	},
	Required: []string{"verdicts"},
}

func segmentPrompt(in SegmentInput) string {
	var b strings.Builder
	if e := strings.TrimSpace(in.Earlier); e != "" {
		b.WriteString("EARLIER CONVERSATION (context only, not scored):\n")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	b.WriteString("SEGMENT TO SCORE:\n")
	b.WriteString(strings.TrimSpace(in.Segment))
	b.WriteString("\n\n")
	if f := strings.TrimSpace(in.Facts); f != "" {
		b.WriteString("FACTS (ground truth, the CEO did not see these):\n")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	b.WriteString("CRITERIA:\n")
	for _, c := range in.Criteria {
		fmt.Fprintf(&b, "- id %q: %s\n", c.ID, strings.TrimSpace(c.Criterion))
	}
	return b.String()
}

// JudgeSegment implements SegmentJudge: one call, one verdict per
// criterion, in the criteria's order. A criterion missing from the answer
// is an error (never a silent pass).
func (j *LLMJudge) JudgeSegment(ctx context.Context, in SegmentInput) ([]Verdict, error) {
	if j == nil || j.Model == nil {
		return nil, errNoModel
	}
	if len(in.Criteria) == 0 {
		return nil, nil
	}
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(segmentSystem, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    segmentSchema,
		Temperature:       genai.Ptr[float32](0),
	}
	resp, err := j.Model.Generate(ctx, j.ModelID, []*genai.Content{genai.NewContentFromText(segmentPrompt(in), genai.RoleUser)}, cfg)
	if err != nil {
		return nil, fmt.Errorf("judge: %w", err)
	}
	return parseSegmentVerdicts(textOf(resp), in.Criteria)
}

func parseSegmentVerdicts(text string, criteria []SegmentCriterion) ([]Verdict, error) {
	var out struct {
		Verdicts []struct {
			ID         string   `json:"id"`
			Verdict    string   `json:"verdict"`
			Confidence *float64 `json:"confidence"`
			Evidence   string   `json:"evidence"`
		} `json:"verdicts"`
	}
	m := reJSONObject.FindString(text)
	if m == "" || json.Unmarshal([]byte(m), &out) != nil {
		return nil, fmt.Errorf("judge: unparseable segment verdicts %q", firstLine(text))
	}
	byID := map[string]Verdict{}
	for _, v := range out.Verdicts {
		ans := Answer(strings.ToLower(strings.TrimSpace(v.Verdict)))
		if ans == AnswerWait {
			ans = AnswerNo // the segment is over: undecided is no
		}
		if ans != AnswerYes && ans != AnswerNo {
			continue
		}
		conf := 0.5
		if v.Confidence != nil {
			conf = clamp01(*v.Confidence)
		}
		byID[strings.TrimSpace(v.ID)] = Verdict{Answer: ans, Confidence: conf, Evidence: strings.TrimSpace(v.Evidence)}
	}
	vs := make([]Verdict, len(criteria))
	var missing []string
	for i, c := range criteria {
		v, ok := byID[c.ID]
		if !ok {
			missing = append(missing, c.ID)
			continue
		}
		v.Criterion = c.Criterion
		if v.Answer == AnswerNo {
			v.Rationale = v.Evidence
		}
		vs[i] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("judge: no verdict for %s", strings.Join(missing, ", "))
	}
	return vs, nil
}

// JudgeSegment implements SegmentJudge for the fake: each criterion is
// decided by the rules, as Judge decides it (and recorded as an input).
func (f *FakeJudge) JudgeSegment(ctx context.Context, in SegmentInput) ([]Verdict, error) {
	out := make([]Verdict, len(in.Criteria))
	for i, c := range in.Criteria {
		v, err := f.Judge(ctx, JudgeInput{Criterion: c.Criterion, Transcript: in.Segment, Facts: in.Facts, Final: true})
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Segment kinds.
const (
	// SegmentRequest has at least one CEO line.
	SegmentRequest = "request"
	// SegmentAnnouncement has only sheepdog's own lines (before the first
	// beat, or in a scripted silence).
	SegmentAnnouncement = "announcement"
)

// Segment is one part of a conversation: lines [Start, End) of it.
type Segment struct {
	Beat       int
	Kind       string
	Start, End int
}

// SplitSegments cuts a conversation into segments at each line that
// carries an agenda beat (a CEO line or a scripted silence); the lines
// before the first beat are a segment of their own (beat 0). A
// conversation without beats is cut at each CEO line that is not an
// answer to sheepdog's question. A segment where sheepdog said nothing
// and the CEO said nothing is dropped.
func SplitSegments(convo []SimLine) []Segment {
	hasBeats := false
	for _, l := range convo {
		if l.Beat > 0 {
			hasBeats = true
			break
		}
	}
	starts := func(l SimLine) bool {
		if hasBeats {
			return l.Beat > 0
		}
		return l.Who == "ceo" && l.Tag != "answer"
	}
	var out []Segment
	cur := Segment{Start: 0}
	flush := func(end int) {
		cur.End = end
		if cur.End <= cur.Start {
			return
		}
		ceo, heard := false, false
		for _, l := range convo[cur.Start:cur.End] {
			switch l.Who {
			case "ceo":
				ceo = true
			case "sheepdog", "push":
				if strings.TrimSpace(l.Text) != "" {
					heard = true
				}
			}
		}
		switch {
		case ceo:
			cur.Kind = SegmentRequest
		case heard:
			cur.Kind = SegmentAnnouncement
		default:
			return
		}
		out = append(out, cur)
	}
	n := 0
	for i, l := range convo {
		if !starts(l) {
			continue
		}
		flush(i)
		n++
		beat := l.Beat
		if !hasBeats {
			beat = n
		}
		cur = Segment{Beat: beat, Start: i}
	}
	flush(len(convo))
	return out
}

// segmentHeard reports whether sheepdog said anything in the segment.
func segmentHeard(lines []SimLine) bool {
	for _, l := range lines {
		if (l.Who == "sheepdog" || l.Who == "push") && strings.TrimSpace(l.Text) != "" {
			return true
		}
	}
	return false
}

// segmentAnnouncementItems are the default items asked of a segment of
// sheepdog's own announcements (no request: answered and right_agent do
// not apply).
var segmentAnnouncementItems = map[string]bool{"no_unintended_action": true, "no_hallucination": true, "facts_current": true, "length_fits": true, "ear_friendly": true, "language": true}

// segmentNoReplyItems are asked of a request segment sheepdog said
// nothing in (the length and listening items have nothing to judge).
var segmentNoReplyItems = map[string]bool{"answered": true, "no_unintended_action": true, "right_agent": true}

// SegmentCriteria lists the items a segment of scenario s is scored on:
// the default items that apply to its kind (answered asked of the
// segment, with the calibration set's wording), the scenario's own rubric
// items on request segments, and the workday clarify and coherence items
// of its beat.
func SegmentCriteria(s SimScenario, seg Segment, heard bool) []SegmentCriterion {
	skip := map[string]bool{}
	for _, k := range s.Expect.Skip {
		skip[k] = true
	}
	var out []SegmentCriterion
	for _, k := range defaultRubricOrder {
		if skip[k] {
			continue
		}
		if seg.Kind == SegmentAnnouncement && !segmentAnnouncementItems[k] {
			continue
		}
		if !heard && !segmentNoReplyItems[k] {
			continue
		}
		r := defaultRubric[k]
		if q, ok := calibQuestions[k]; ok {
			r.Question = q
		}
		out = append(out, SegmentCriterion{ID: k, Criterion: r.Criterion()})
	}
	if seg.Kind != SegmentRequest || s.Workday == nil {
		return out
	}
	names, criteria := workdayRubric(s)
	byName := map[string]string{}
	for i, n := range names {
		byName[n] = criteria[i]
	}
	for i, c := range s.Workday.Truth.Clarify {
		if c.Beat == seg.Beat {
			n := fmt.Sprintf("%s_%d", WorkdayClarifyItem, i+1)
			out = append(out, SegmentCriterion{ID: n, Criterion: byName[n]})
		}
	}
	for i, p := range s.Workday.Truth.Coherence {
		if p.Beat == seg.Beat {
			n := fmt.Sprintf("%s_%d", WorkdayCoherenceItem, i+1)
			out = append(out, SegmentCriterion{ID: n, Criterion: byName[n]})
		}
	}
	return out
}

// SegmentResult is one segment's verdicts in a run record.
type SegmentResult struct {
	Index   int    `json:"index"`
	Beat    int    `json:"beat"`
	Kind    string `json:"kind"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	// Verdicts are the segment's items (names as in the run's verdicts).
	Verdicts []NamedVerdict `json:"verdicts"`
}

// ErrNotSegmentJudge: the judge cannot score segments.
var ErrNotSegmentJudge = errors.New("judge cannot score segments")

// JudgeSegments scores every segment of a recorded conversation, one call
// each, at most parallel in flight. The first judge error is returned
// (the results are then incomplete and must not be kept).
func JudgeSegments(ctx context.Context, j Judge, s SimScenario, convo []SimLine, facts string, parallel int) ([]SegmentResult, error) {
	sj, ok := j.(SegmentJudge)
	if !ok {
		return nil, ErrNotSegmentJudge
	}
	segs := SplitSegments(convo)
	out := make([]SegmentResult, len(segs))
	errs := make([]error, len(segs))
	if parallel < 1 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, seg := range segs {
		lines := convo[seg.Start:seg.End]
		crit := SegmentCriteria(s, seg, segmentHeard(lines))
		res := SegmentResult{Index: i, Beat: seg.Beat, Kind: seg.Kind, StartMS: lines[0].AtMS, EndMS: lines[len(lines)-1].AtMS}
		out[i] = res
		if len(crit) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int, in SegmentInput) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			vs, err := sj.JudgeSegment(ctx, in)
			if err != nil {
				errs[i] = fmt.Errorf("segment %d (beat %d): %w", i, out[i].Beat, err)
				return
			}
			for k, v := range vs {
				out[i].Verdicts = append(out[i].Verdicts, NamedVerdict{Name: in.Criteria[k].ID, Verdict: v})
			}
		}(i, SegmentInput{Criteria: crit, Earlier: renderSim(convo[:seg.Start]), Segment: renderSim(lines), Facts: facts})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// JudgeCalibSegments runs the segment judge on the calibration set: one
// call per item scoring all its rubric ids (the items are segment-sized),
// with at most parallel calls in flight. The output has the same shape as
// JudgeCalib's, so agreement is computed the same way.
func JudgeCalibSegments(ctx context.Context, j SegmentJudge, f CalibFile, parallel int, progress func(done, total int)) []CalibVerdict {
	rubric := map[string]RubricItem{}
	for _, r := range f.Rubric {
		rubric[r.ID] = r
	}
	if parallel < 1 {
		parallel = 1
	}
	outs := make([][]CalibVerdict, len(f.Items))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for i, it := range f.Items {
		wg.Add(1)
		go func(i int, it CalibItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			crit := make([]SegmentCriterion, len(it.RubricIDs))
			for k, id := range it.RubricIDs {
				crit[k] = SegmentCriterion{ID: id, Criterion: rubric[id].Criterion()}
			}
			vs, err := j.JudgeSegment(ctx, SegmentInput{Criteria: crit, Segment: it.Transcript(), Facts: it.Context})
			cvs := make([]CalibVerdict, len(crit))
			for k, c := range crit {
				cv := CalibVerdict{ItemID: it.ID, RubricID: c.ID}
				switch {
				case err != nil:
					cv.Error = firstLine(err.Error())
				case vs[k].Answer == AnswerYes:
					cv.Verdict = "pass"
				default:
					cv.Verdict = "fail"
				}
				if err == nil {
					cv.Evidence = vs[k].Evidence
				}
				cvs[k] = cv
			}
			outs[i] = cvs
			if progress != nil {
				mu.Lock()
				done++
				progress(done, len(f.Items))
				mu.Unlock()
			}
		}(i, it)
	}
	wg.Wait()
	var out []CalibVerdict
	for _, o := range outs {
		out = append(out, o...)
	}
	return out
}
