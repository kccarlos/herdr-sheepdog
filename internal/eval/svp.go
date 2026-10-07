package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// recorder is a logging.Logger that captures the brain.* events grading
// needs: which tools were called, whether each completed or errored, and
// whether the rail fired.
type recorder struct {
	calls   []string
	args    []map[string]any // parallel to calls
	results map[string]bool  // tool name -> completed without error (at least once)
	rail    int
	// meter, when set, gets the orchestrator's tokens from its brain.step
	// events under model.
	meter *Meter
	model string
}

func newRecorder() *recorder { return &recorder{results: map[string]bool{}} }

func (r *recorder) Log(source, typ, _ string, payload any) {
	if source != "brain" {
		return
	}
	m, _ := payload.(map[string]any)
	switch typ {
	case "brain.tool_call":
		if name, _ := m["name"].(string); name != "" {
			args, _ := m["args"].(map[string]any)
			r.calls = append(r.calls, name)
			r.args = append(r.args, args)
		}
	case "brain.tool_result":
		name, _ := m["name"].(string)
		errStr, _ := m["error"].(string)
		if name != "" && errStr == "" {
			r.results[name] = true
		}
	case "brain.rail_blocked":
		r.rail++
	case "brain.step":
		if r.meter != nil {
			r.meter.Add("brain", r.model, stepUsage(m))
		}
	}
}

// stepUsage reads a brain.step event's token counts.
func stepUsage(m map[string]any) Usage {
	return Usage{Calls: 1, In: toInt(m["prompt_tokens"]), Out: toInt(m["completion_tokens"]), Reasoning: toInt(m["reasoning_tokens"])}
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func (r *recorder) calledAtAll(name string) bool {
	for _, c := range r.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (r *recorder) calledSuccessfully(name string) bool { return r.results[name] }

// readWith reports whether a read_screen call asked for source (the
// default source is screen) and read_screen completed at least once.
func (r *recorder) readWith(source string) bool {
	for i, name := range r.calls {
		if name != brain.ToolReadScreen || i >= len(r.args) {
			continue
		}
		got, _ := r.args[i]["source"].(string)
		if got == "" {
			got = brain.SourceScreen
		}
		if strings.EqualFold(got, source) {
			return r.calledSuccessfully(brain.ToolReadScreen)
		}
	}
	return false
}

func (r *recorder) names() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range r.calls {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// RunSVPOffline runs cases through the orchestrator with a scripted fake model:
// no network, so this is a check of the harness (the rail, the provenance
// wrapping, roster/transcript plumbing), not of model judgement.
func RunSVPOffline(ctx context.Context, cases []SVPCase, fixtureDir string) []Result {
	out := make([]Result, 0, len(cases))
	for _, c := range cases {
		out = append(out, runSVPCase(ctx, c, fixtureDir, svpRun{offline: true}))
	}
	return out
}

// RunSVPLive runs cases through the orchestrator with a real model. Cases after
// a rate-limited call are reported as skipped rather than retried (the
// Gemini free tier needs a cool-down, not a hot loop).
func RunSVPLive(ctx context.Context, cases []SVPCase, fixtureDir string, model brain.Model, modelName string) []Result {
	return RunSVPLiveWith(ctx, cases, fixtureDir, SVPLive{Model: model, ModelName: modelName})
}

// SVPLive configures a live orchestrator run.
type SVPLive struct {
	Model     brain.Model
	ModelName string
	// Provider is the orchestrator provider id of Model (gemini, vertex,
	// deepseek); empty is the orchestrator's default. DeepSeek needs it so the
	// loop replays its reasoning content.
	Provider string
	// Judge, when set, decides the wording checks (spoken_contains_any_of,
	// first_sentence_contains_any_of, spoken_no_jargon) and the case's
	// own judge criteria; nil keeps the code checks (substring and word
	// lists) and skips the criteria.
	Judge Judge
	// Meter, when set, counts the orchestrator's tokens.
	Meter *Meter
}

// RunSVPLiveWith is RunSVPLive with a provider and an optional judge.
func RunSVPLiveWith(ctx context.Context, cases []SVPCase, fixtureDir string, live SVPLive) []Result {
	out := make([]Result, 0, len(cases))
	for i, c := range cases {
		r := runSVPCase(ctx, c, fixtureDir, svpRun{model: live.Model, modelName: live.ModelName, provider: live.Provider, judge: live.Judge, meter: live.Meter})
		out = append(out, r)
		if r.Err != nil && errors.Is(r.Err, brain.ErrAllKeysRateLimited) {
			for _, rest := range cases[i+1:] {
				out = append(out, Result{ID: rest.ID, Status: StatusSkipped, Detail: "rate limited (429); stopped rather than hot-retry"})
			}
			break
		}
	}
	return out
}

// svpRun is how one case runs: offline with the scripted model, or live
// with a model and, optionally, a judge.
type svpRun struct {
	model     brain.Model
	modelName string
	provider  string
	offline   bool
	judge     Judge
	meter     *Meter
}

func runSVPCase(ctx context.Context, c SVPCase, fixtureDir string, run svpRun) Result {
	offline, model, modelName := run.offline, run.model, run.modelName
	t0 := time.Now()
	host, alias, err := buildHost(c, fixtureDir)
	if err != nil {
		return Result{ID: c.ID, Status: StatusFail, Detail: "fixture: " + err.Error(), Duration: time.Since(t0), Failures: []Failure{{FailError, "fixture: " + err.Error()}}}
	}
	rec := newRecorder()
	rec.meter, rec.model = run.meter, modelName
	m := model
	// MaxSteps 3 caps a live case at up to 3 model calls (docs/roles.md
	// D9-adjacent cost discipline: the Gemini free tier's quota is
	// limited, keep eval runs small). The utterance budget is left to the
	// orchestrator's default (brain.DefaultUtteranceTimeout), so a live case gets
	// the same budget the daemon does.
	cfg := brain.Config{MaxSteps: 3, ThinkingLevel: "low"}
	if modelName != "" {
		cfg.Model = modelName
	}
	cfg.Provider = run.provider
	if offline {
		m = scriptFor(c, alias)
	}
	b, err := brain.New(cfg, m, rec)
	if err != nil {
		return Result{ID: c.ID, Status: StatusFail, Detail: "brain.New: " + err.Error(), Duration: time.Since(t0), Failures: []Failure{{FailError, "brain.New: " + err.Error()}}}
	}
	defer func() { _ = b.Close() }()
	rec.model = b.Model()
	b.SetHost(host)
	seedTranscript(b, c.Transcript)

	req := brain.Request{
		Kind: brain.Kind(c.Request.Kind), Question: askedOf(c), Agent: c.Request.Agent, Briefing: c.Request.Briefing,
		Event: c.Request.Event, Template: c.Request.Template, Owed: c.Request.Owed, Screen: c.Request.Screen,
	}
	if req.Kind == "" {
		req.Kind = brain.KindUtterance
	}
	reply, err := b.Turn(ctx, req)
	b.Quiesce()
	dur := time.Since(t0)
	got := map[string]any{"tools_called": strings.Join(rec.names(), ","), "spoken": reply.Spoken}
	base := baselineOf(c)

	if err != nil && errors.Is(err, brain.ErrAllKeysRateLimited) {
		// brain.fail wraps a rate limit in ErrFallback (the supervisor
		// tolerates it and answers for itself); RunSVPLive
		// needs to see it unwrapped to stop rather than hot-retry the
		// remaining cases against the same exhausted key pool.
		return Result{ID: c.ID, Status: StatusFail, Detail: firstLine(err.Error()), Err: err, Duration: dur, Got: got, Failures: classifySVP(nil, err)}
	}
	if err != nil && !errors.Is(err, brain.ErrFallback) {
		return Result{ID: c.ID, Status: StatusFail, Detail: firstLine(err.Error()), Err: err, Duration: dur, Got: got, Failures: classifySVP(nil, err)}
	}
	if err != nil {
		// A clean fallback (no reply) still lets the harness checks below
		// run against whatever the recorder captured before it happened.
		got["fallback"] = firstLine(err.Error())
	}
	judge := run.judge
	if offline {
		judge = nil // a scripted reply has nothing to judge
	}
	reasons, skipped := gradeSVPWith(c.Expect, rec, reply, offline, judge != nil)
	var verdicts []Verdict
	if judge != nil {
		var why []string
		verdicts, why = judgeSVP(ctx, judge, c, req, reply, rec, host)
		reasons = append(reasons, why...)
	}
	if fb, ok := got["fallback"]; ok && !offline {
		// Live, a turn that fell back gave the user the daemon's code
		// sentence, not an answer: say why first (a timeout reads very
		// differently from a wrong answer in the failure taxonomy).
		reasons = append([]string{fmt.Sprintf("%s: %v", reasonFallback, fb)}, reasons...)
	}
	reasons = append(reasons, requestReasons(c, m)...)
	reasons = append(reasons, stopWordReasons(c)...)
	reasons = append(reasons, heardReasons(c, req.Question)...)
	reasons = append(reasons, base.reasons...)
	r := Result{ID: c.ID, Duration: dur, Got: got, SkippedChecks: skipped, Pending: c.PendingCheck, Baseline: base.summary, Verdicts: verdicts, Failures: classifySVP(reasons, nil)}
	switch {
	case offline && len(reasons) == 0:
		r.Status, r.Detail = StatusPass, "harness check (fake model)"
		if fb, ok := got["fallback"]; ok {
			// A turn that fell back never reached the executor: say so,
			// rather than let a vacuous must_not_call read as checked.
			r.Detail += fmt.Sprintf(", but the turn fell back: %v", fb)
		}
	case offline:
		r.Status, r.Detail = StatusFail, "harness check (fake model): "+strings.Join(reasons, "; ")
	case len(reasons) > 0:
		r.Status, r.Detail = StatusFail, strings.Join(reasons, "; ")
	default:
		r.Status = StatusPass
	}
	return r
}

// requestReasons checks Expect.RequestContains against the first call
// the scripted model received (offline; live models keep no record of
// it, and the request text does not depend on the model).
func requestReasons(c SVPCase, m brain.Model) []string {
	fake, ok := m.(*braintest.Model)
	if len(c.Expect.RequestContains) == 0 || !ok {
		return nil
	}
	calls := fake.Calls()
	if len(calls) == 0 {
		return []string{"request_contains: the model was never called"}
	}
	text := calls[0].Text()
	var reasons []string
	for _, want := range c.Expect.RequestContains {
		if !strings.Contains(text, want) {
			reasons = append(reasons, fmt.Sprintf("the request lacks %q", want))
		}
	}
	return reasons
}

// stopWordReasons checks Expect.NotStopWord against the daemon's own
// bare-stop list: words on it never reach the orchestrator.
func stopWordReasons(c SVPCase) []string {
	if !c.Expect.NotStopWord || !supervisor.BareStopWords[supervisor.BareWord(c.Request.Question)] {
		return nil
	}
	return []string{fmt.Sprintf("%q is a bare stop word: the daemon would run the stop procedure (Ctrl-C a recent instruction) before any brain turn", c.Request.Question)}
}

// askedOf is the question the daemon puts in front of the orchestrator for c:
// what the user was heard saying (source.heard), when the case
// records it, else the relay — the same choice the supervisor makes
// (supervisor.UtteranceOf).
func askedOf(c SVPCase) string {
	if c.Source == nil {
		return c.Request.Question
	}
	q, _ := supervisor.UtteranceOf(c.Source.Heard, c.Request.Question)
	return q
}

// heardReasons checks Expect.QuestionIsHeard: the orchestrator was asked asked,
// which must be the user's recorded words.
func heardReasons(c SVPCase, asked string) []string {
	if !c.Expect.QuestionIsHeard || c.Source == nil || c.Source.Heard == "" || asked == c.Source.Heard {
		return nil
	}
	return []string{fmt.Sprintf("the brain was asked %q, not what the shepherd said (%q)", asked, c.Source.Heard)}
}

// baseline is a case's grade against the logged behaviour (Observed).
type baseline struct {
	summary string   // for the table: "fails as logged: …" / "passes as logged"
	reasons []string // failures of the case itself (BaselineFails violated)
}

// baselineOf grades c's expectations, every check including the live-only
// ones, against what the logged system did (c.Observed). This is how a
// regression case proves offline, with no model, that it catches the
// failure it was taken from (docs/iteration-2.md rule 2: "fails before").
func baselineOf(c SVPCase) baseline {
	if c.Observed == nil {
		return baseline{}
	}
	rec := newRecorder()
	for _, oc := range c.Observed.ToolCalls {
		rec.calls = append(rec.calls, oc.Name)
		rec.args = append(rec.args, oc.Args)
		rec.results[oc.Name] = true
	}
	reply := brain.Reply{Spoken: c.Observed.Spoken, Urgency: c.Observed.Urgency}
	if c.Observed.NeedsUser != nil {
		reply.NeedsUser = *c.Observed.NeedsUser
	}
	fails, _ := gradeSVP(c.Expect, rec, reply, false)
	// The logged orchestrator was asked the relay itself.
	fails = append(fails, heardReasons(c, c.Request.Question)...)
	return judgeBaseline(fails, c.BaselineFails)
}

// judgeBaseline turns the baseline's failure reasons into a summary and,
// when the case pins the outcome (BaselineFails), a failure of the case.
func judgeBaseline(fails []string, want *bool) baseline {
	var b baseline
	if len(fails) > 0 {
		b.summary = "fails as logged: " + strings.Join(fails, "; ")
	} else {
		b.summary = "passes as logged"
	}
	switch {
	case want == nil:
	case *want && len(fails) == 0:
		b.reasons = append(b.reasons, "baseline: the logged behaviour passes this case's expectations, so the case does not catch the failure it was taken from")
	case !*want && len(fails) > 0:
		b.reasons = append(b.reasons, "baseline: the logged behaviour was expected to pass but "+b.summary)
	}
	return b
}

// checkSVPExpect grades a case's recorded tool calls and reply against
// its Expect (see gradeSVP).
func checkSVPExpect(exp SVPExpect, rec *recorder, reply brain.Reply, offline bool) []string {
	reasons, _ := gradeSVP(exp, rec, reply, offline)
	return reasons
}

// liveOnlyChecks are the checks about how the reply is phrased or judged:
// the offline fake model cannot phrase or judge, so offline they are
// skipped and listed in the report, never passed silently
// (docs/iteration-2.md WP0b).
var liveOnlyChecks = []string{
	"spoken_contains_any_of", "spoken_max_sentences", "spoken_max_words", "spoken_no_jargon",
	"first_sentence_contains_any_of", "needs_user", "urgency_not", "ask_empty", "reply_language",
}

// gradeSVP grades a case's recorded tool calls and reply against its
// Expect, returning a failure reason per mismatch and, offline, the
// live-only checks the case sets that were skipped.
func gradeSVP(exp SVPExpect, rec *recorder, reply brain.Reply, offline bool) (reasons, skipped []string) {
	return gradeSVPWith(exp, rec, reply, offline, false)
}

// gradeSVPWith is gradeSVP; judged leaves the wording checks
// (judgeSVP) to the judge instead of the code's substring and word
// lists.
func gradeSVPWith(exp SVPExpect, rec *recorder, reply brain.Reply, offline, judged bool) (reasons, skipped []string) {
	if offline {
		for _, name := range liveOnlyChecks {
			if exp.sets(name) {
				skipped = append(skipped, name)
			}
		}
	}
	if len(exp.ToolsUsedAnyOf) > 0 {
		ok := false
		for _, want := range exp.ToolsUsedAnyOf {
			if rec.calledAtAll(want) {
				ok = true
				break
			}
		}
		if !ok {
			reasons = append(reasons, "none of ["+strings.Join(exp.ToolsUsedAnyOf, ", ")+"] were called (called: "+strings.Join(rec.names(), ",")+")")
		}
	}
	for _, bad := range exp.MustNotCall {
		if rec.calledSuccessfully(bad) {
			reasons = append(reasons, bad+" completed successfully but must not (a rail-blocked attempt would be fine)")
		}
	}
	if exp.RailBlocked && rec.rail == 0 {
		reasons = append(reasons, "expected the rail to block a call, but brain.rail_blocked never fired")
	}
	if exp.ReadSource != "" && !rec.readWith(exp.ReadSource) {
		reasons = append(reasons, fmt.Sprintf("read_screen with source %s was not called, or did not complete (called: %s)", exp.ReadSource, strings.Join(rec.names(), ",")))
	}
	if exp.NoTool && len(rec.calls) > 0 {
		reasons = append(reasons, "answered with tool calls ("+strings.Join(rec.names(), ",")+"), but the roster and ledger in the prompt are enough")
	}
	if !offline && !judged && len(exp.SpokenContainsAnyOf) > 0 {
		lower := strings.ToLower(reply.Spoken)
		found := false
		for _, want := range exp.SpokenContainsAnyOf {
			if strings.Contains(lower, strings.ToLower(want)) {
				found = true
				break
			}
		}
		if !found {
			reasons = append(reasons, "spoken "+quoteOrEmpty(reply.Spoken)+" contains none of: "+strings.Join(exp.SpokenContainsAnyOf, ", "))
		}
	}
	if !offline && exp.SpokenMaxSentences > 0 && tooManySentences(reply.Spoken, exp.SpokenMaxSentences) {
		reasons = append(reasons, fmt.Sprintf("spoken %s has more than %d sentences; the rest belongs in detail", quoteOrEmpty(reply.Spoken), exp.SpokenMaxSentences))
	}
	for i, name := range rec.calls {
		if name != brain.ToolPrompt || i >= len(rec.args) {
			continue
		}
		text, _ := rec.args[i]["text"].(string)
		for _, bad := range exp.PromptMustNotContain {
			if strings.Contains(text, bad) {
				reasons = append(reasons, fmt.Sprintf("a prompt to a worker contains %q: %s", bad, firstLine(text)))
			}
		}
	}
	if offline {
		return reasons, skipped
	}
	if exp.SpokenMaxWords > 0 {
		if n := wordCount(reply.Spoken); n > exp.SpokenMaxWords {
			reasons = append(reasons, fmt.Sprintf("spoken has %d words, more than %d", n, exp.SpokenMaxWords))
		}
	}
	if exp.SpokenNoJargon && !judged {
		if found := jargonIn(reply.Spoken); len(found) > 0 {
			reasons = append(reasons, "spoken uses jargon: "+strings.Join(found, ", "))
		}
	}
	if len(exp.FirstSentenceContainsAnyOf) > 0 && !judged {
		first := prose.Spoken(reply.Spoken, 1)
		if !containsAnyFold(first, exp.FirstSentenceContainsAnyOf) {
			reasons = append(reasons, "first sentence "+quoteOrEmpty(first)+" contains none of: "+strings.Join(exp.FirstSentenceContainsAnyOf, ", "))
		}
	}
	if exp.NeedsUser != nil && reply.NeedsUser != *exp.NeedsUser {
		reasons = append(reasons, fmt.Sprintf("needs_user=%v, want %v", reply.NeedsUser, *exp.NeedsUser))
	}
	if exp.AskEmpty && strings.TrimSpace(reply.Ask) != "" {
		reasons = append(reasons, "ask "+quoteOrEmpty(reply.Ask)+" on a line with no open decision; ask is only for a decision the shepherd has to take")
	}
	if why := languageReasons(exp.ReplyLanguage, reply); why != "" {
		reasons = append(reasons, why)
	}
	for _, bad := range exp.UrgencyNot {
		u := reply.Urgency
		if u == "" && strings.TrimSpace(reply.Spoken) == "" {
			u = brain.UrgencySkip // an empty line is a skip (brain.Reply.Urgency)
		}
		if u == bad {
			reasons = append(reasons, "urgency="+u+", which this case rules out")
		}
	}
	return reasons, skipped
}

// sets reports whether the check called name is set on exp (for the
// report's list of checks skipped offline).
func (exp SVPExpect) sets(name string) bool {
	switch name {
	case "spoken_contains_any_of":
		return len(exp.SpokenContainsAnyOf) > 0
	case "spoken_max_sentences":
		return exp.SpokenMaxSentences > 0
	case "spoken_max_words":
		return exp.SpokenMaxWords > 0
	case "spoken_no_jargon":
		return exp.SpokenNoJargon
	case "first_sentence_contains_any_of":
		return len(exp.FirstSentenceContainsAnyOf) > 0
	case "needs_user":
		return exp.NeedsUser != nil
	case "urgency_not":
		return len(exp.UrgencyNot) > 0
	case "ask_empty":
		return exp.AskEmpty
	case "reply_language":
		return exp.ReplyLanguage != ""
	}
	return false
}

// languageReasons grades Expect.ReplyLanguage: "en" wants spoken and ask
// free of CJK characters, "zh" wants a CJK character in spoken.
func languageReasons(lang string, reply brain.Reply) string {
	hasCJK := func(s string) bool {
		for _, r := range s {
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
				return true
			}
		}
		return false
	}
	switch lang {
	case "en":
		if hasCJK(reply.Spoken) || hasCJK(reply.Ask) {
			return "reply is not in English though the shepherd last spoke English: " + quoteOrEmpty(reply.Spoken+" "+reply.Ask)
		}
	case "zh":
		if !hasCJK(reply.Spoken) {
			return "reply is not in Chinese though the shepherd last spoke Chinese: " + quoteOrEmpty(reply.Spoken)
		}
	}
	return ""
}

func containsAnyFold(s string, subs []string) bool {
	lower := strings.ToLower(s)
	for _, want := range subs {
		if strings.Contains(lower, strings.ToLower(want)) {
			return true
		}
	}
	return false
}

// wordCount counts whitespace-separated words, the way the scorecard
// counts a spoken line's length.
func wordCount(s string) int { return len(strings.Fields(s)) }

// jargonTerms is a small stand-in list of words the user's ear should
// not have to parse (docs/iteration-2.md WP1: no PR numbers, ms,
// versions, unfamiliar terms).
//
// TODO(WP0a): switch to internal/earmetrics once it lands; this list is a
// local copy so the two packages are not written against each other.
var jargonTerms = []string{
	"daemon", "fast path", "fast paths", "push queue", "chattiness ceiling", "context window",
	"compact", "compaction", "tokens", "pruning", "send_keys", "rail", "turn loop", "event turn",
	"system prompt", "fallback", "timers", "ci", "refactor", "api", "json", "ms",
}

var (
	reJargonTech = regexp.MustCompile(`(?i)\b(?:pr\s*#?\d+|#\d+|v?\d+\.\d+(?:\.\d+)?|\d+\s*ms)\b`)
	jargonRes    = func() []*regexp.Regexp {
		out := make([]*regexp.Regexp, 0, len(jargonTerms))
		for _, t := range jargonTerms {
			out = append(out, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(t)+`\b`))
		}
		return out
	}()
)

// jargonIn lists the jargon terms and technical tokens (PR numbers,
// versions, milliseconds) s uses.
func jargonIn(s string) []string {
	var found []string
	for i, re := range jargonRes {
		if re.MatchString(s) {
			found = append(found, jargonTerms[i])
		}
	}
	found = append(found, reJargonTech.FindAllString(s, -1)...)
	return found
}

// tooManySentences reports whether s has more than n sentences, counted
// the way the spoken-line scrubber cuts them.
func tooManySentences(s string, n int) bool {
	return prose.Spoken(s, n) != prose.Spoken(s, 0)
}

// buildHost sets up an in-memory braintest.Host from the case: the
// primary agent (Request.Agent, or "auth") gets ScreenFixture and
// Pending; Agents adds any further roster rows (ambiguity cases).
//
// A RosterOnly case (the extracted and most regression cases) has no
// synthetic primary: Agents is the logged roster, and the alias the
// offline script targets is Request.Agent, else the agent with a pending
// prompt, else the first one.
func buildHost(c SVPCase, fixtureDir string) (*braintest.Host, string, error) {
	h := braintest.NewHost()
	alias := c.Request.Agent
	if c.RosterOnly {
		if len(c.Agents) == 0 {
			return nil, "", errors.New("roster_only case with no agents")
		}
		if alias == "" {
			alias = targetOf(c.Agents)
		}
	} else {
		if alias == "" {
			alias = "auth"
		}
		text, err := readFixture(c.ScreenFixture, fixtureDir)
		if err != nil {
			return nil, "", err
		}
		const primaryPane = "w1:p1"
		h.SetAgent(brain.AgentInfo{PaneID: primaryPane, Alias: alias, Kind: "codex", State: "blocked"})
		h.SetScreen(primaryPane, text)
		if c.Pending != nil {
			h.SetPending(primaryPane, toPendingPrompt(c.Pending))
		}
	}
	for _, n := range c.Notes {
		h.AddNote(n)
	}
	for i, a := range c.Agents {
		pane := a.PaneID
		if pane == "" {
			pane = fmt.Sprintf("w%d:p1", i+2)
		}
		h.SetAgent(brain.AgentInfo{PaneID: pane, Alias: a.Alias, Kind: orDefault(a.Kind, "codex"), State: orDefault(a.State, "idle")})
		if a.Screen != "" {
			if scr, err := readFixture(a.Screen, fixtureDir); err == nil {
				h.SetScreen(pane, scr)
			} else {
				h.SetScreen(pane, a.Screen) // inline text
			}
		}
		if a.Pending != nil {
			h.SetPending(pane, toPendingPrompt(a.Pending))
		}
		if a.Session != "" {
			read, err := readSessionFixture(a.Session, fixtureDir, orDefault(a.Kind, "codex"))
			if err != nil {
				return nil, "", fmt.Errorf("session %s: %w", a.Session, err)
			}
			h.SetSession(pane, read)
		}
	}
	return h, alias, nil
}

// targetOf is the agent a roster_only case's offline script addresses
// when the request names none: the one with an open prompt, else the one
// with a session fixture, else the first.
func targetOf(agents []SVPAgent) string {
	for _, a := range agents {
		if a.Pending != nil {
			return a.Alias
		}
	}
	for _, a := range agents {
		if a.Session != "" {
			return a.Alias
		}
	}
	return agents[0].Alias
}

// readSessionFixture reads a session transcript fixture the way the
// daemon's Host does (workerlog's Tail): name is a file under the
// workerlog directory next to fixtureDir (testdata/herdr ->
// testdata/workerlog), or a path.
func readSessionFixture(name, fixtureDir, kind string) (brain.SessionRead, error) {
	path := name
	if !strings.ContainsAny(name, "/\\") {
		path = filepath.Join(filepath.Dir(filepath.Clean(fixtureDir)), "workerlog", name)
	}
	d, err := workerlog.New(workerlog.Options{}).Tail(workerlog.Session{Agent: kind, Kind: "path", Value: path})
	if err != nil {
		return brain.SessionRead{}, err
	}
	return brain.SessionRead{LastAssistant: d.LastAssistant, Instruction: d.Instruction(), Files: d.Files, Tools: d.Tools}, nil
}

func toPendingPrompt(p *SVPPending) *brain.PendingPrompt {
	if p == nil {
		return nil
	}
	opts := make([]brain.Option, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, brain.Option{Label: o.Label, Keys: o.Keys})
	}
	return &brain.PendingPrompt{ID: "eval-pending", Question: p.Question, Options: opts, YesKeys: p.YesKeys, NoKeys: p.NoKeys}
}

// seedTranscript ingests prior transcript lines: "user: …" and "Dog: …"
// give the matching message kind; "PUSH <alias>: …" a worker push;
// anything else is treated as a user utterance.
func seedTranscript(b *brain.Brain, lines []string) {
	var msgs []brain.Message
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "CEO: "):
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindCEOUtterance, Content: strings.TrimPrefix(line, "CEO: ")})
		case strings.HasPrefix(line, "Dog: "):
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindAssistantSaid, Content: strings.TrimPrefix(line, "Dog: ")})
		case strings.HasPrefix(line, "PUSH "):
			rest := strings.TrimPrefix(line, "PUSH ")
			alias, text, ok := strings.Cut(rest, ": ")
			if !ok {
				alias, text = "", rest
			}
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindPush, Content: text, Agent: alias})
		default:
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindCEOUtterance, Content: line})
		}
	}
	b.Seed(msgs)
}

// scriptFor picks one fake-model step for the offline harness check: the
// rail cases attempt send_keys (to prove the rail refuses it), everything
// else calls the first tool the case expects (or read_screen, absent a
// hint), then answers.
func scriptFor(c SVPCase, alias string) *braintest.Model {
	if c.Expect.NoTool {
		// A status case: the scripted model answers straight off, so the
		// harness check is that nothing was called.
		return braintest.NewModel(braintest.Step{Text: braintest.Reply("Offline scripted answer for "+c.ID+".", "")})
	}
	tool := brain.ToolReadScreen
	switch {
	case c.Expect.ReadSource != "":
		// A detail case: script the read the case requires, so the check
		// is that the Host served it (the session fixture parsed).
		fc := braintest.FC(tool, map[string]any{"agent": alias, "source": c.Expect.ReadSource})
		return braintest.NewModel(
			braintest.Step{Calls: []*genai.FunctionCall{fc}},
			braintest.Step{Text: braintest.Reply("Offline scripted answer for "+c.ID+".", "")},
		)
	case len(c.Expect.ToolsUsedAnyOf) > 0:
		// The case's own expectation takes priority: e.g. a case that
		// wants "prompt" AND asserts must_not_call send_keys should be
		// scripted to try prompt, not send_keys (must_not_call then holds
		// trivially, as it should — nothing ever attempted it).
		tool = c.Expect.ToolsUsedAnyOf[0]
	case c.Expect.RailBlocked || (containsStr(c.Expect.MustNotCall, brain.ToolSendKeys) && !hasPending(c)):
		// No positive expectation: the case exists to test the rail, so
		// script the attempt the rail must refuse. With a prompt open the
		// rail would rightly let a user turn answer it, so there "must not
		// send keys" is the model's judgement (a status question is not
		// an approval): live only, nothing to script.
		tool = brain.ToolSendKeys
	case brain.Kind(c.Request.Kind).IsEvent() && containsStr(c.Expect.MustNotCall, brain.ToolPrompt):
		// An event turn that must not act: script the attempt, so every
		// extracted finish or block proves the executor refuses an action
		// tool on an event (docs/roles.md "The one rail").
		tool = brain.ToolPrompt
	}
	fc := braintest.FC(tool, defaultArgsFor(tool, alias))
	return braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{fc}},
		braintest.Step{Text: braintest.Reply("Offline scripted answer for "+c.ID+".", "")},
	)
}

func defaultArgsFor(tool, alias string) map[string]any {
	switch tool {
	case brain.ToolReadScreen:
		return map[string]any{"agent": alias}
	case brain.ToolRunCLI:
		return map[string]any{"agent": alias, "question": "why did it fail?"}
	case brain.ToolListAgents:
		return map[string]any{}
	case brain.ToolRecall:
		return map[string]any{"query": ""}
	case brain.ToolPrompt:
		return map[string]any{"agent": alias, "text": "continue"}
	case brain.ToolSendKeys:
		return map[string]any{"agent": alias, "keys": []any{"1"}}
	case brain.ToolInterrupt:
		return map[string]any{"agent": alias}
	case brain.ToolPromise:
		return map[string]any{"agent": alias, "text": "let me know when it's done", "event": "done"}
	case brain.ToolNote:
		return map[string]any{"text": "remember the migrations"}
	case brain.ToolInvestigate:
		return map[string]any{"agent": alias, "question": "why did the failing test fail?"}
	default:
		return map[string]any{"agent": alias}
	}
}

// hasPending reports whether any agent of c has an open approval prompt.
func hasPending(c SVPCase) bool {
	if c.Pending != nil && !c.RosterOnly {
		return true
	}
	for _, a := range c.Agents {
		if a.Pending != nil {
			return true
		}
	}
	return false
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// readFixture loads name, treating it as a bare file name under dir when
// it has no path separator (matches `sheepdog brain-test --fixture`).
func readFixture(name, dir string) (string, error) {
	path := name
	if !strings.ContainsAny(name, "/\\") {
		path = filepath.Join(dir, name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
