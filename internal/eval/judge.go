package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// The LLM judge (after pipecat's evals/judge.py): one criterion at a
// time, a verdict of yes, no or wait, and a rationale only where someone
// will read it — a no, or a verdict the judge is unsure of. The
// rationale is a second, separate call so the verdict prompt stays short
// and the common case (a confident yes) costs one cheap call.

// Answer is a judge's verdict on one criterion.
type Answer string

// The three verdicts. Wait means the conversation is not finished and
// the criterion cannot be decided yet (the reply so far is a holding
// line, the worker has not reported back): the harness may continue it.
// A final judgement (JudgeInput.Final) has no wait: undecided is no.
const (
	AnswerYes  Answer = "yes"
	AnswerNo   Answer = "no"
	AnswerWait Answer = "wait"
)

// Verdict is the judge's answer on one criterion.
type Verdict struct {
	Criterion  string  `json:"criterion"`
	Answer     Answer  `json:"answer"`
	Confidence float64 `json:"confidence"`
	// Rationale is set only for a no or a low-confidence verdict.
	Rationale string `json:"rationale,omitempty"`
	// Err is set when the judge could not answer (the verdict is then
	// not a judgement and the run counts as errored).
	Err string `json:"error,omitempty"`
}

// Passed reports whether v is a yes.
func (v Verdict) Passed() bool { return v.Err == "" && v.Answer == AnswerYes }

// JudgeInput is what the judge sees for one criterion.
type JudgeInput struct {
	// Criterion is a yes/no question about the conversation, phrased so
	// that yes is the good outcome.
	Criterion string
	// Transcript is the conversation as the CEO experienced it: what
	// they said and what they heard, oldest first.
	Transcript string
	// Facts is the ground truth the judge may check claims against (the
	// workers' screens, what the harness actually sent to them).
	Facts string
	// Final says the conversation is over: wait is not allowed.
	Final bool
}

// Judge decides criteria.
type Judge interface {
	Judge(ctx context.Context, in JudgeInput) (Verdict, error)
}

// LLMJudge is a Judge over a text model.
type LLMJudge struct {
	Model   brain.Model
	ModelID string
	// ExplainBelow is the confidence under which a yes also gets a
	// rationale (default 0.7).
	ExplainBelow float64
}

const judgeSystem = `You are a strict, fair evaluator of a voice assistant called sheepdog. Sheepdog supervises coding agents (workers such as Codex or Claude Code running in terminal panes) for a CEO who talks to it by voice, away from the screen. The CEO hears every sheepdog line; they never see the screens.

You get the conversation, ground-truth facts (what the workers' screens really showed and every action the system really sent to a worker), and ONE criterion phrased as a question where "yes" is the good outcome. Decide only that criterion; ignore everything else.

Verdicts:
- "yes": the criterion is clearly met.
- "no": the criterion is not met, or a claim needed for it contradicts the facts.
- "wait": the conversation is not finished and the criterion cannot be decided yet (for example sheepdog said it is checking and the answer has not arrived). Only use wait when the conversation could still continue.

Judge what the CEO actually heard, not what sheepdog might have meant. A spoken line is for the ear: short, plain words, no file paths, IDs, version numbers or markup unless the CEO asked for that detail.

Answer with JSON only: {"verdict": "yes"|"no"|"wait", "confidence": <number between 0 and 1>}`

const judgeFinalNote = `The conversation is OVER: nothing more will be said. Do not answer "wait"; if the criterion was not met by the end, the verdict is "no".`

const explainSystem = `You are explaining an evaluator's verdict on a voice assistant conversation. In at most two sentences, say concretely why the criterion got this verdict: quote the line(s) that decided it and name the fact it contradicts, if any. No preamble.`

var judgeSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"verdict":    {Type: genai.TypeString, Enum: []string{"yes", "no", "wait"}},
		"confidence": {Type: genai.TypeNumber},
	},
	Required: []string{"verdict", "confidence"},
}

// Judge implements Judge.
func (j *LLMJudge) Judge(ctx context.Context, in JudgeInput) (Verdict, error) {
	v := Verdict{Criterion: in.Criterion}
	if j == nil || j.Model == nil {
		return v, errNoModel
	}
	sys := judgeSystem
	if in.Final {
		sys += "\n\n" + judgeFinalNote
	}
	user := judgePrompt(in)
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(sys, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    judgeSchema,
		Temperature:       genai.Ptr[float32](0),
	}
	resp, err := j.Model.Generate(ctx, j.ModelID, []*genai.Content{genai.NewContentFromText(user, genai.RoleUser)}, cfg)
	if err != nil {
		return v, fmt.Errorf("judge: %w", err)
	}
	ans, conf, err := parseVerdict(textOf(resp))
	if err != nil {
		return v, fmt.Errorf("judge: %w", err)
	}
	if in.Final && ans == AnswerWait {
		ans = AnswerNo // undecided at the end is a no
	}
	v.Answer, v.Confidence = ans, conf
	below := j.ExplainBelow
	if below == 0 {
		below = 0.7
	}
	if ans == AnswerNo || conf < below {
		v.Rationale = j.explain(ctx, user, v)
	}
	return v, nil
}

// explain asks for the rationale of v; a failed call leaves it empty
// (the verdict stands).
func (j *LLMJudge) explain(ctx context.Context, user string, v Verdict) string {
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(explainSystem, genai.RoleUser),
		Temperature:       genai.Ptr[float32](0),
	}
	prompt := fmt.Sprintf("%s\n\nVERDICT: %s (confidence %.2f)\nWhy?", user, v.Answer, v.Confidence)
	resp, err := j.Model.Generate(ctx, j.ModelID, []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}, cfg)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(textOf(resp))
}

func judgePrompt(in JudgeInput) string {
	var b strings.Builder
	b.WriteString("CONVERSATION:\n")
	b.WriteString(strings.TrimSpace(in.Transcript))
	b.WriteString("\n\n")
	if f := strings.TrimSpace(in.Facts); f != "" {
		b.WriteString("FACTS (ground truth, the CEO did not see these):\n")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	b.WriteString("CRITERION: ")
	b.WriteString(strings.TrimSpace(in.Criterion))
	return b.String()
}

var (
	reJSONObject = regexp.MustCompile(`(?s)\{.*\}`)
	reBareAnswer = regexp.MustCompile(`(?i)\b(yes|no|wait)\b`)
)

// parseVerdict reads the judge's JSON, tolerating a fenced block or prose
// around it (Ollama ignores the response schema), and falls back to the
// first bare yes/no/wait word with a low confidence.
func parseVerdict(text string) (Answer, float64, error) {
	var out struct {
		Verdict    string   `json:"verdict"`
		Confidence *float64 `json:"confidence"`
	}
	if m := reJSONObject.FindString(text); m != "" && json.Unmarshal([]byte(m), &out) == nil {
		ans := Answer(strings.ToLower(strings.TrimSpace(out.Verdict)))
		if ans == AnswerYes || ans == AnswerNo || ans == AnswerWait {
			conf := 0.5
			if out.Confidence != nil {
				conf = clamp01(*out.Confidence)
			}
			return ans, conf, nil
		}
	}
	if m := reBareAnswer.FindString(text); m != "" {
		return Answer(strings.ToLower(m)), 0.5, nil
	}
	return "", 0, fmt.Errorf("unparseable verdict %q", firstLine(text))
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// FakeJudge is a scripted Judge for offline runs and tests: the first
// rule whose substring the criterion contains (case-insensitive) decides;
// otherwise Default (yes when empty). It records every input.
type FakeJudge struct {
	Rules   []FakeRule
	Default Answer

	mu     sync.Mutex
	inputs []JudgeInput
}

// FakeRule maps a criterion substring to a verdict.
type FakeRule struct {
	Contains string
	Answer   Answer
	// Fn, when set, decides from the input instead (Answer ignored).
	Fn func(JudgeInput) Answer
}

// Judge implements Judge.
func (f *FakeJudge) Judge(_ context.Context, in JudgeInput) (Verdict, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()
	ans := f.Default
	if ans == "" {
		ans = AnswerYes
	}
	for _, r := range f.Rules {
		if strings.Contains(strings.ToLower(in.Criterion), strings.ToLower(r.Contains)) {
			ans = r.Answer
			if r.Fn != nil {
				ans = r.Fn(in)
			}
			break
		}
	}
	if in.Final && ans == AnswerWait {
		ans = AnswerNo
	}
	v := Verdict{Criterion: in.Criterion, Answer: ans, Confidence: 1}
	if ans != AnswerYes {
		v.Rationale = "fake judge rule"
	}
	return v, nil
}

// Inputs returns what the fake was asked, in order.
func (f *FakeJudge) Inputs() []JudgeInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]JudgeInput(nil), f.inputs...)
}

// judgeAll decides every criterion, one call each (in parallel when the
// judge is remote: they are independent).
func judgeAll(ctx context.Context, j Judge, criteria []string, transcript, facts string, final bool) []Verdict {
	out := make([]Verdict, len(criteria))
	var wg sync.WaitGroup
	for i, c := range criteria {
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			v, err := j.Judge(ctx, JudgeInput{Criterion: c, Transcript: transcript, Facts: facts, Final: final})
			if err != nil {
				v = Verdict{Criterion: c, Err: firstLine(err.Error())}
			}
			out[i] = v
		}(i, c)
	}
	wg.Wait()
	return out
}
