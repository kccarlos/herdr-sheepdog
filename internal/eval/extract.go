package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// ExtractOptions selects a window of the event log for `sheepdog eval
// extract` (docs/dev/iteration-2.md WP0b: "turns a log window into cases").
type ExtractOptions struct {
	// From and To bound the window (inclusive); zero is unbounded.
	From, To time.Time
	// Context is how many prior transcript lines a case carries
	// (default 8).
	Context int
	// Aliases seeds pane → alias (LearnAliases over the previous day
	// file): an agent the supervisor last named yesterday otherwise shows
	// up as its pane id.
	Aliases map[string]string
	// Redact lists further words to replace with "<redacted>" wherever
	// they appear (whole words, case-insensitive): a person's name a
	// worker's screen showed, the machine's hostname (DefaultRedactions).
	// They come from the command line, never from the repository, so the
	// words themselves are never checked in.
	Redact []string
}

// DefaultRedactions are the words every extraction redacts: this
// machine's hostname and its first label (a shell prompt on a worker's
// screen shows it).
func DefaultRedactions() []string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return nil
	}
	out := []string{h}
	if short, _, ok := strings.Cut(h, "."); ok && len(short) >= 6 {
		out = append(out, short)
	}
	return out
}

// redactCases replaces every whole-word, case-insensitive occurrence of
// the words in each case's JSON, so no field is missed.
func redactCases(cases []SVPCase, words []string) []SVPCase {
	var alts []string
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			alts = append(alts, regexp.QuoteMeta(w))
		}
	}
	if len(alts) == 0 {
		return cases
	}
	re := regexp.MustCompile(`(?i)\b(?:` + strings.Join(alts, "|") + `)\b`)
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			continue
		}
		var out SVPCase
		if json.Unmarshal(re.ReplaceAll(b, []byte("<redacted>")), &out) == nil {
			cases[i] = out
		}
	}
	return cases
}

// LearnAliases returns every pane's alias as the supervisor's events name
// it, the latest one winning.
func LearnAliases(events []logEvent) map[string]string {
	out := map[string]string{}
	for _, ev := range events {
		if a := aliasFrom(ev); a != "" {
			out[ev.PaneID] = a
		}
	}
	return out
}

// DefaultExtractContext is the number of prior transcript lines an
// extracted case carries when ExtractOptions.Context is zero.
const DefaultExtractContext = 8

// logEvent is one event line as extraction needs it: the envelope with
// the payload decoded to a map.
type logEvent struct {
	eventlog.Event
	p map[string]any
}

func (e logEvent) str(key string) string {
	s, _ := e.p[key].(string)
	return s
}

// ReadDayFile reads one event day file (`<data_dir>/events/<day>.jsonl`).
// Lines that do not parse are skipped: the log is append-only and a
// crash can leave a torn last line.
func ReadDayFile(path string) ([]logEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []logEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 16<<20)
	for sc.Scan() {
		var ev eventlog.Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		le := logEvent{Event: ev}
		if len(ev.Payload) > 0 {
			_ = json.Unmarshal(ev.Payload, &le.p)
		}
		if le.p == nil {
			le.p = map[string]any{}
		}
		out = append(out, le)
	}
	return out, sc.Err()
}

// ExtractDayFile extracts the cases of one day file (path is
// <dir>/<day>.jsonl), seeding aliases from the previous day's file when
// there is one.
func ExtractDayFile(path string, opt ExtractOptions) ([]SVPCase, error) {
	events, err := ReadDayFile(path)
	if err != nil {
		return nil, err
	}
	day := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if d, err := time.Parse("2006-01-02", day); err == nil && opt.Aliases == nil {
		prev := filepath.Join(filepath.Dir(path), d.AddDate(0, 0, -1).Format("2006-01-02")+".jsonl")
		if pe, err := ReadDayFile(prev); err == nil {
			opt.Aliases = LearnAliases(pe)
		}
	}
	return Extract(events, day, opt), nil
}

// DayFiles lists the day files under dir that the window [from, to]
// touches (UTC days, as the event log names them); zero bounds take
// every file.
func DayFiles(dir string, from, to time.Time) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var out []string
	for _, m := range matches {
		day := strings.TrimSuffix(filepath.Base(m), ".jsonl")
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			continue
		}
		if !from.IsZero() && d.Add(24*time.Hour).Before(from.UTC()) {
			continue
		}
		if !to.IsZero() && d.After(to.UTC()) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Extract turns the events of one day file into cases: one utterance case
// per `voice.tool_call` of to_supervisor, and one announce case per
// worker event (`herdr.transition` to blocked, done, or idle from
// working). Each case carries the transcript, roster, screens and pending
// prompts the log lets us reconstruct at that moment, what the logged
// system did (Observed, the baseline), and its provenance (Source).
//
// What the log does not carry is left out (docs/dev/roles.md "The L0 eval
// corpus" lists it): herdr.read logs only a byte count, so a screen is at
// most the first 1 KB of the orchestrator's own last read_screen result, and an
// agent it never read shows "(screen not in the event log)"; aliases are
// learned from the supervisor's events, which name them beside the pane
// id; the roster sentence (Briefing) is not logged.
func Extract(events []logEvent, day string, opt ExtractOptions) []SVPCase {
	if opt.Context <= 0 {
		opt.Context = DefaultExtractContext
	}
	x := newExtractor(events)
	for pane, a := range opt.Aliases {
		if _, ok := x.aliasAll[pane]; !ok {
			x.aliasAll[pane] = a
		}
	}
	var out []SVPCase
	for i := range events {
		ev := events[i]
		x.apply(i)
		if (!opt.From.IsZero() && ev.TS.Before(opt.From)) || (!opt.To.IsZero() && ev.TS.After(opt.To)) {
			continue
		}
		switch ev.Type {
		case "voice.tool_call":
			if ev.str("name") != "to_supervisor" {
				continue
			}
			if c, ok := x.utteranceCase(i, day, opt.Context); ok {
				out = append(out, c)
			}
		case "herdr.transition":
			to, from := ev.str("to"), ev.str("from")
			if to == "blocked" || to == "done" || (to == "idle" && from == "working") {
				out = append(out, x.announceCase(i, day, opt.Context))
			}
		}
	}
	// Two events in the same second about the same agent would share an
	// id: number the repeats.
	seen := map[string]int{}
	for i := range out {
		id := out[i].ID
		seen[id]++
		if n := seen[id]; n > 1 {
			out[i].ID = fmt.Sprintf("%s-%d", id, n)
		}
	}
	return redactCases(out, opt.Redact)
}

// extractor replays the log in order, keeping the state a case needs.
type extractor struct {
	events []logEvent
	// aliasAll is every pane's alias as the supervisor's events name it
	// (first seen, whole file): herdr's own events carry only the agent
	// kind and workspace label, so an agent seen before the supervisor
	// first named it still gets its alias. aliasNow is the latest one up
	// to the current event (a pane id can be reused).
	aliasAll, aliasNow map[string]string
	roster             map[string]*rosterRow
	pendings           map[string]*openPending // by pending id
	screens            map[string]string       // alias -> last read_screen result (≤ 1 KB)
	toolAgent          map[string]string       // brain.tool_call id -> agent arg
	promises           map[string]string       // open promise id -> pane
	lines              []tline
	in, out            *tline // open transcript segments
}

type rosterRow struct {
	pane, kind, state, label string
}

type openPending struct {
	pane string
	p    SVPPending
}

type tline struct {
	role string // user, Dog, PUSH
	who  string // alias for a push
	text string
	last time.Time
}

func (l tline) render() string {
	switch l.role {
	case "Dog":
		return "Dog: " + l.text
	case "PUSH":
		return "PUSH " + l.who + ": " + l.text
	}
	return "CEO: " + l.text
}

// supervisorSources are the sources whose events name an agent by alias
// in payload.agent (herdr's own events put the agent kind there).
var supervisorSources = map[string]bool{"supervisor": true, "brain": true, "voice": true}

func newExtractor(events []logEvent) *extractor {
	x := &extractor{
		events: events, aliasAll: map[string]string{}, aliasNow: map[string]string{},
		roster: map[string]*rosterRow{}, pendings: map[string]*openPending{}, screens: map[string]string{},
		toolAgent: map[string]string{}, promises: map[string]string{},
	}
	for _, ev := range events {
		if a := aliasFrom(ev); a != "" {
			if _, ok := x.aliasAll[ev.PaneID]; !ok {
				x.aliasAll[ev.PaneID] = a
			}
		}
	}
	return x
}

// aliasFrom is the alias an event names for its pane, if any: an
// explicit payload.alias (a field herdr.transition could carry), else a
// supervisor-side event's payload.agent.
func aliasFrom(ev logEvent) string {
	if ev.PaneID == "" {
		return ""
	}
	if a := ev.str("alias"); a != "" {
		return a
	}
	if supervisorSources[ev.Source] {
		return ev.str("agent")
	}
	return ""
}

func (x *extractor) alias(pane string) string {
	if a := x.aliasNow[pane]; a != "" {
		return a
	}
	if a := x.aliasAll[pane]; a != "" {
		return a
	}
	return pane
}

// segmentGap closes a transcript segment: fragments further apart than
// this are separate turns (every transcript event in the 2026-09-23/24
// logs is final:false, so the flag cannot be used).
const segmentGap = 3 * time.Second

// apply folds event i into the state.
func (x *extractor) apply(i int) {
	ev := x.events[i]
	if a := aliasFrom(ev); a != "" {
		x.aliasNow[ev.PaneID] = a
	}
	switch ev.Type {
	case "herdr.state", "herdr.transition":
		row := x.roster[ev.PaneID]
		if row == nil {
			row = &rosterRow{pane: ev.PaneID}
			x.roster[ev.PaneID] = row
		}
		if k := ev.str("agent"); k != "" {
			row.kind = k
		}
		if s := ev.str("state"); s != "" {
			row.state = s
		}
		if s := ev.str("to"); s != "" {
			row.state = s
		}
		if l := ev.str("workspace_label"); l != "" {
			row.label = l
		}
	case "herdr.fleet":
		switch ev.str("kind") {
		case "pane_closed", "pane_removed":
			delete(x.roster, ev.PaneID)
		case "workspace_closed":
			ws := ev.str("workspace_id")
			for pane := range x.roster {
				if ws != "" && strings.HasPrefix(pane, ws+":") {
					delete(x.roster, pane)
				}
			}
		}
	case "pending.created":
		if ev.str("kind") != "answer_prompt" {
			// A send_instruction pending is the record of an instruction
			// the orchestrator sent, not a prompt waiting on the user.
			return
		}
		x.pendings[ev.str("id")] = &openPending{pane: ev.PaneID, p: pendingFrom(ev.p)}
	case "pending.confirmed", "pending.cancelled", "pending.expired", "pending.replaced":
		delete(x.pendings, ev.str("id"))
	case "promise.created":
		x.promises[ev.str("id")] = ev.PaneID
	case "promise.kept", "promise.expired", "promise.cancelled":
		delete(x.promises, ev.str("id"))
	case "brain.tool_call":
		if args, ok := ev.p["args"].(map[string]any); ok {
			a, _ := args["agent"].(string)
			x.toolAgent[ev.str("id")] = a
		}
	case "brain.tool_result":
		if ev.str("name") == brain.ToolReadScreen && ev.str("error") == "" {
			if a := x.toolAgent[ev.str("id")]; a != "" {
				x.screens[a] = unwrapWorker(ev.str("result"))
			}
		}
	case "voice.transcript.in":
		x.fragment(&x.in, "CEO", ev)
	case "voice.transcript.out":
		x.fragment(&x.out, "Dog", ev)
	case "voice.tool_call", "voice.turn":
		x.closeSegments()
	case "voice.push":
		x.closeSegments()
		if t := strings.TrimSpace(ev.str("text")); t != "" {
			x.lines = append(x.lines, tline{role: "PUSH", who: ev.str("agent"), text: t, last: ev.TS})
		}
	}
}

// fragment appends a transcript fragment to the open segment of its
// direction, opening a new one after a gap or a turn in the other
// direction. Fragments carry their own spacing (" findings"), except
// GPT-Live's whole phrases, which get a space after sentence punctuation.
func (x *extractor) fragment(seg **tline, role string, ev logEvent) {
	text := ev.str("text")
	if text == "" {
		return
	}
	other := &x.out
	if role == "Dog" {
		other = &x.in
	}
	if *other != nil {
		x.flush(other)
	}
	cur := *seg
	if cur != nil && ev.TS.Sub(cur.last) > segmentGap {
		x.flush(seg)
		cur = nil
	}
	if cur == nil {
		*seg = &tline{role: role, text: text, last: ev.TS}
		return
	}
	if n := len(cur.text); n > 0 && !strings.HasPrefix(text, " ") && strings.ContainsAny(cur.text[n-1:], ".?!,") {
		cur.text += " "
	}
	cur.text += text
	cur.last = ev.TS
}

func (x *extractor) flush(seg **tline) {
	if *seg == nil {
		return
	}
	l := **seg
	l.text = strings.Join(strings.Fields(l.text), " ")
	if l.text != "" {
		x.lines = append(x.lines, l)
	}
	*seg = nil
}

func (x *extractor) closeSegments() {
	// The user spoke before the voice answered: flush in first.
	x.flush(&x.in)
	x.flush(&x.out)
}

func (x *extractor) transcript(n int) []string {
	lines := x.lines
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, scrub(l.render()))
	}
	return out
}

// agents renders the roster as case rows, sorted by pane, with each
// agent's open prompt and last known screen.
func (x *extractor) agents() []SVPAgent {
	panes := make([]string, 0, len(x.roster))
	for p, row := range x.roster {
		if row.state == "" || row.kind == "" {
			continue
		}
		panes = append(panes, p)
	}
	sort.Strings(panes)
	out := make([]SVPAgent, 0, len(panes))
	for _, p := range panes {
		row := x.roster[p]
		if row.state == "unknown" {
			// herdr's "unknown" is a pane with no agent detected (or one
			// closed since): not a roster row.
			continue
		}
		a := SVPAgent{PaneID: p, Alias: x.alias(p), Kind: row.kind, State: row.state}
		a.Screen = scrub(x.screens[a.Alias])
		if a.Screen == "" {
			// SetScreen with "" still answers a read; say what is missing
			// instead, so a live run knows the screen was not logged.
			a.Screen = "(screen not in the event log)"
		}
		if pp := x.openPendingFor(p); pp != nil {
			cp := *pp
			a.Pending = &cp
		}
		out = append(out, a)
	}
	return out
}

func (x *extractor) openPendingFor(pane string) *SVPPending {
	for _, op := range x.pendings {
		if op.pane == pane {
			p := op.p
			return &p
		}
	}
	return nil
}

func pendingFrom(p map[string]any) SVPPending {
	out := SVPPending{Question: scrub(str(p["question"]))}
	if opts, ok := p["options"].([]any); ok {
		for _, o := range opts {
			m, _ := o.(map[string]any)
			out.Options = append(out.Options, SVPOption{Label: scrub(str(m["label"])), Keys: strs(m["keys"])})
		}
	}
	out.YesKeys = strs(p["yes_keys"])
	out.NoKeys = strs(p["no_keys"])
	return out
}

func str(v any) string { s, _ := v.(string); return s }

func strs(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// stopWords are the words that make an interrupt the right call
// (prompts/assistant.md's stop list, minus "never mind": docs/dev/iteration-2.md
// J6 "never Ctrl-C on never mind").
var reStopWord = regexp.MustCompile(`(?i)\b(stop|halt|abort|kill|cancel|interrupt|ctrl[- +]?c)\b`)

// utteranceCase builds the case for the to_supervisor call at events[i].
func (x *extractor) utteranceCase(i int, day string, n int) (SVPCase, bool) {
	ev := x.events[i]
	args, _ := ev.p["args"].(map[string]any)
	relay := strings.TrimSpace(str(args["text"]))
	if relay == "" {
		return SVPCase{}, false
	}
	agents := x.agents()
	if len(agents) == 0 {
		return SVPCase{}, false
	}
	c := SVPCase{
		ID:         fmt.Sprintf("x-%s-%s-utt", day, ev.TS.UTC().Format("150405")),
		RosterOnly: true,
		Agents:     agents,
		Transcript: x.transcript(n),
		Request:    SVPRequest{Kind: string(brain.KindUtterance), Question: scrub(relay)},
	}
	c.Job = classifyJob(relay)
	c.Source = &CaseSource{Day: day, TS: ev.TS.UTC().Format(time.RFC3339Nano), EventID: ev.ID}
	if heard := x.lastHeard(); heard != "" && normalize(heard) != normalize(relay) {
		c.Source.Heard = scrub(heard)
	}
	if !reStopWord.MatchString(relay) {
		c.Expect.MustNotCall = []string{brain.ToolInterrupt}
	}
	if c.Source.Heard != "" && isPartialRelay(relay, c.Source.Heard) {
		// The voice relayed only the tail of what was said: whatever the
		// orchestrator sends must not fill the gap with invented text
		// (regression partial-relay-no-fabrication), and the orchestrator must
		// be asked what was heard, not the tail (WP3).
		c.Expect.PromptMustNotContain = []string{"["}
		c.Expect.QuestionIsHeard = true
	}
	c.Observed = x.observeUtterance(i)
	return c, true
}

// lastHeard is the newest user transcript line, or "".
func (x *extractor) lastHeard() string {
	for j := len(x.lines) - 1; j >= 0 && j >= len(x.lines)-3; j-- {
		if x.lines[j].role == "CEO" {
			return x.lines[j].text
		}
	}
	return ""
}

// announceCase builds the case for the worker event at events[i].
func (x *extractor) announceCase(i int, day string, n int) SVPCase {
	ev := x.events[i]
	alias := x.alias(ev.PaneID)
	event := ev.str("to")
	if event == "idle" {
		event = "done" // the supervisor announces working→idle as a finish
	}
	agents := x.agents()
	// The supervisor raises an approval prompt's pending right after the
	// block and before the announce turn: take one created within 5 s.
	if event == "blocked" {
		for j := i + 1; j < len(x.events) && x.events[j].TS.Sub(ev.TS) < 5*time.Second; j++ {
			e := x.events[j]
			if e.Type == "pending.created" && e.PaneID == ev.PaneID && e.str("kind") == "answer_prompt" {
				p := pendingFrom(e.p)
				for k := range agents {
					if agents[k].PaneID == ev.PaneID {
						agents[k].Pending = &p
					}
				}
				break
			}
		}
	}
	c := SVPCase{
		ID:         fmt.Sprintf("x-%s-%s-%s-%s", day, ev.TS.UTC().Format("150405"), event, slug(alias)),
		RosterOnly: true,
		Agents:     agents,
		Transcript: x.transcript(n),
		Request: SVPRequest{
			Kind: string(brain.KindAnnounce), Agent: alias, Event: event,
			Screen: scrub(x.screens[alias]),
		},
	}
	c.Job = "J4"
	c.Source = &CaseSource{Day: day, TS: ev.TS.UTC().Format(time.RFC3339Nano), EventID: ev.ID}
	for _, pane := range x.promises {
		if pane == ev.PaneID {
			c.Request.Owed = true
		}
	}
	// An event turn is read-only: whatever the event says, the orchestrator
	// sends nothing to a worker (docs/dev/roles.md "The one rail").
	c.Expect.MustNotCall = []string{brain.ToolPrompt, brain.ToolSendKeys, brain.ToolInterrupt}
	c.Request.Template, c.Observed = x.observeAnnounce(i, alias, event)
	return c
}

// observeUtterance collects what the logged system did for the utterance
// at events[i]: the orchestrator turn that answered it (the first utterance
// turn after it, before the next relay), its tool calls, and what the
// voice then said.
func (x *extractor) observeUtterance(i int) *SVPObserved {
	start := x.events[i].TS
	end := -1
	for j := i + 1; j < len(x.events); j++ {
		e := x.events[j]
		if e.TS.Sub(start) > 90*time.Second || e.Type == "voice.tool_call" {
			break
		}
		if e.Type == "brain.turn" && e.str("trigger") == "ceo" && e.str("kind") == string(brain.KindUtterance) {
			end = j
			break
		}
	}
	if end < 0 {
		// No orchestrator turn: a fast path, the echo guard, or a fallback
		// answered it; the baseline is unknown rather than "did nothing".
		return nil
	}
	turn, _ := x.events[end].p["turn"].(float64)
	var obs SVPObserved
	for j := i + 1; j <= end; j++ {
		e := x.events[j]
		if e.Type == "brain.tool_call" {
			if t, _ := e.p["turn"].(float64); t == turn {
				args, _ := e.p["args"].(map[string]any)
				obs.ToolCalls = append(obs.ToolCalls, ObservedCall{Name: e.str("name"), Args: scrubArgs(args)})
			}
		}
	}
	obs.Spoken = scrub(x.spokenAfter(end))
	return &obs
}

// spokenAfter is what the voice said after events[i]: its transcript
// until the next voice.turn end (at most 60 s), or a push that delivered
// the reply.
func (x *extractor) spokenAfter(i int) string {
	var b strings.Builder
	started := false
	t := x.events[i].TS
	for _, e := range x.events[i+1:] {
		if e.TS.Sub(t) > 60*time.Second {
			break
		}
		switch e.Type {
		case "voice.transcript.out":
			text := e.str("text")
			if n := b.Len(); n > 0 && !strings.HasPrefix(text, " ") && strings.ContainsAny(b.String()[n-1:], ".?!,") {
				b.WriteString(" ")
			}
			b.WriteString(text)
			started = true
		case "voice.push":
			if e.str("answers_utterance") != "" && !started {
				return e.str("text")
			}
		case "voice.turn", "voice.tool_call":
			if started {
				return strings.Join(strings.Fields(b.String()), " ")
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// observeAnnounce finds the supervisor's decision on the event at
// events[i]: the template sentence, and the orchestrator's line (a
// push_decision via orchestrator, or a push_late that was dropped).
func (x *extractor) observeAnnounce(i int, alias, event string) (string, *SVPObserved) {
	ev := x.events[i]
	template := alias + " finished."
	if event == "blocked" {
		template = alias + " is waiting for you."
	}
	var obs *SVPObserved
	for j := i + 1; j < len(x.events); j++ {
		e := x.events[j]
		if e.TS.Sub(ev.TS) > 60*time.Second || (e.Type == "herdr.transition" && e.PaneID == ev.PaneID) {
			break
		}
		if e.PaneID != ev.PaneID {
			continue
		}
		switch e.Type {
		case "brain.push_decision":
			if t := e.str("template"); t != "" {
				template = t
			}
			if e.str("via") == "brain" && obs == nil {
				nu, _ := e.p["needs_user"].(bool)
				obs = &SVPObserved{Spoken: scrub(e.str("spoken")), NeedsUser: &nu, Urgency: e.str("urgency_requested")}
			}
		case "brain.push_late":
			if obs == nil {
				nu, _ := e.p["needs_user"].(bool)
				obs = &SVPObserved{Spoken: scrub(e.str("spoken")), NeedsUser: &nu}
			}
		}
	}
	return scrub(template), obs
}

// classifyJob guesses the product job (docs/dev/iteration-2.md) of an
// utterance from keywords, so the paraphrase generator substitutes within
// the right job. A guess: unmatched utterances get "".
func classifyJob(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	short := len(strings.Fields(l)) <= 6
	switch {
	case reNeverMind.MatchString(l):
		return "J6-nevermind"
	case reStopWord.MatchString(l):
		return "J6-stop"
	case reExplain.MatchString(l):
		return "J5"
	case reFind.MatchString(l):
		return "J4"
	case reStatus.MatchString(l):
		return "J1"
	case reApprove.MatchString(l) || (short && reYes.MatchString(l)):
		return "J2-yes"
	case short && reNo.MatchString(l):
		return "J2-no"
	case reInstruct.MatchString(l):
		return "J3"
	}
	return ""
}

var (
	reNeverMind = regexp.MustCompile(`\bnever ?mind\b|\bforget it\b`)
	reYes       = regexp.MustCompile(`^(yes|yeah|yep|sure|ok(ay)?|go ahead|do it)\b`)
	reApprove   = regexp.MustCompile(`\bapprove\b`)
	reNo        = regexp.MustCompile(`^(no|nope|don't|deny|decline)\b[.!]?$`)
	reExplain   = regexp.MustCompile(`what does (that|it|this) mean|\bmeans?\??$|\bwhy\b|say (that |it )?again|simpler|explain (that|it|this)`)
	reFind      = regexp.MustCompile(`what did (it|they|he|she) (find|say)|what does (it|the result) say|findings|\bresult\b|additional questions`)
	reStatus    = regexp.MustCompile(`status|going on|any (agents?|one) (running|working)|how are things|how is it going|progress|what are .* doing|working agents|latest state|check again`)
	reInstruct  = regexp.MustCompile(`^(tell|ask|have|let|please|run|make|start|check|look)\b`)
)

var quoteFolder = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`)

func normalize(s string) string {
	s = quoteFolder.Replace(s)
	return strings.Join(strings.Fields(strings.ToLower(strings.Trim(s, " .?!,"))), " ")
}

// isPartialRelay reports whether the relay is a strict tail of what was
// heard: the voice passed on only the last words.
func isPartialRelay(relay, heard string) bool {
	r, h := normalize(relay), normalize(heard)
	return r != "" && len(r) < len(h)/2 && strings.HasSuffix(h, r)
}

func unwrapWorker(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "<<<BEGIN_WORKER_OUTPUT") || strings.HasPrefix(line, "<<<END_WORKER_OUTPUT") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(reSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// Scrubbing: extracted cases are checked in, so anything that looks like
// a secret or an email address is replaced (aliases and repo names stay).
var scrubbers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), "<email>"},
	{regexp.MustCompile(`AIza[0-9A-Za-z_\-]{20,}`), "<secret>"},
	{regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_\-]{16,}`), "<secret>"},
	{regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`), "<secret>"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`), "<secret>"},
	{regexp.MustCompile(`(?i)\b(bearer|token|api[_-]?key|secret|password)(["':= ]+)[A-Za-z0-9._\-]{12,}`), "$1$2<secret>"},
	{regexp.MustCompile(`\b[0-9a-f]{40,}\b`), "<hex>"},
	{regexp.MustCompile(`/Users/[^/\s"']+`), "~"},
}

// scrub removes secrets, emails and home paths from logged text.
func scrub(s string) string {
	for _, sc := range scrubbers {
		s = sc.re.ReplaceAllString(s, sc.with)
	}
	return s
}

func scrubArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok {
			out[k] = scrub(s)
			continue
		}
		out[k] = v
	}
	return out
}
