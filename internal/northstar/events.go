package northstar

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Event is one daemon event, in the form the checks read. Event logs come
// in two shapes: the bench's (at_ms since the run started) and the
// daemon's day files (ts); AtMS is filled from either (ts relative to
// the origin the reader is given).
type Event struct {
	AtMS    int64          `json:"at_ms"`
	TS      time.Time      `json:"ts"`
	Type    string         `json:"type"`
	Pane    string         `json:"pane,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

type rawEvent struct {
	AtMS    *int64          `json:"at_ms"`
	TS      time.Time       `json:"ts"`
	Type    string          `json:"type"`
	Pane    *string         `json:"pane"`
	PaneID  string          `json:"pane_id"`
	Payload json.RawMessage `json:"payload"`
}

// ReadEvents reads a JSONL event file leniently (a bad line is skipped).
// A missing file yields no events and no error.
func ReadEvents(path string) ([]Event, error) {
	f, err := os.Open(path) //nolint:gosec // a result directory the caller names
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseEvents(f)
}

// ParseEvents parses JSONL events. Events with a ts and no at_ms get
// AtMS relative to the first event's ts (Align moves them).
func ParseEvents(r io.Reader) ([]Event, error) {
	var out []Event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var origin time.Time
	for sc.Scan() {
		var re rawEvent
		if err := json.Unmarshal(sc.Bytes(), &re); err != nil || re.Type == "" {
			continue
		}
		e := Event{TS: re.TS, Type: re.Type}
		if re.Pane != nil {
			e.Pane = *re.Pane
		}
		if e.Pane == "" {
			e.Pane = re.PaneID
		}
		if len(re.Payload) > 0 {
			_ = json.Unmarshal(re.Payload, &e.Payload)
		}
		switch {
		case re.AtMS != nil:
			e.AtMS = *re.AtMS
		case !re.TS.IsZero():
			if origin.IsZero() {
				origin = re.TS
			}
			e.AtMS = re.TS.Sub(origin).Milliseconds()
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// FromTS sets every event's AtMS to its ts relative to origin (events
// without a ts keep theirs).
func FromTS(evs []Event, origin time.Time) {
	for i := range evs {
		if !evs[i].TS.IsZero() {
			evs[i].AtMS = evs[i].TS.Sub(origin).Milliseconds()
		}
	}
}

// Align shifts the events' clock onto a record's (tier 3: records count
// from the call's start, events carry wall-clock ts). The record's first
// action and the first herdr.send are the same event, so they align
// exactly; without an action the first supervisor.relay (the daemon
// receiving the user's first line) is put at firstUserMS. Returns false
// when there is nothing to align on.
func Align(evs []Event, firstActionMS, firstUserMS int64) bool {
	anchor := func(typ string, at int64) bool {
		if at < 0 {
			return false
		}
		for _, e := range evs {
			if e.Type == typ {
				shift := at - e.AtMS
				for i := range evs {
					evs[i].AtMS += shift
				}
				return true
			}
		}
		return false
	}
	return anchor("herdr.send", firstActionMS) || anchor("supervisor.relay", firstUserMS)
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch v := m[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func strs(m map[string]any, k string) []string {
	arr, _ := m[k].([]any)
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func num(m map[string]any, k string) (float64, bool) {
	v, ok := m[k].(float64)
	return v, ok
}

// EventFacts is what an event log adds to a record.
type EventFacts struct {
	// Checks are reads that can back a "verified" claim, spawns and
	// closes (never in a record's actions).
	Actions []Action
	// Sends are prompt, keys and interrupt sends (herdr.send), for a log
	// without a record.
	Sends []Action
	// Answers are pending.confirmed answers by time, to tell an approval
	// from a refusal.
	Answers []Action
	// News are investigations returned and failed spawns; Transitions the
	// workers' finishes and blocks (herdr.transition), for a log without
	// a record.
	News        []News
	Transitions []News
	// Orchestrator is the orchestrator's spoken text (voice.tool_result
	// spoken, voice.push spoken).
	Orchestrator []Speech
	// Voice is what the voice model said (voice.transcript.out fragments
	// joined into lines), and User what the daemon received
	// (supervisor.relay), for a log without a record.
	Voice []Speech
	User  []UserLine
	// LatencyMS are turn.latency totals, or brain.turn durations of the
	// user's turns when there are none.
	LatencyMS []float64
	// Aliases maps a pane to the agent alias seen for it.
	Aliases map[string]string
}

// voiceGapMS splits the voice model's transcript fragments into lines.
const voiceGapMS = 1500

// ReadFacts extracts what the checks need from an event log.
func ReadFacts(evs []Event) EventFacts {
	f := EventFacts{Aliases: map[string]string{}}
	for _, e := range evs {
		if e.Pane == "" {
			continue
		}
		if a := str(e.Payload, "alias"); a != "" {
			f.Aliases[e.Pane] = a
		} else if a := str(e.Payload, "agent"); a != "" && !strings.HasPrefix(e.Type, "herdr.") && f.Aliases[e.Pane] == "" {
			f.Aliases[e.Pane] = a
		}
	}
	alias := func(e Event) string {
		if a := str(e.Payload, "agent"); a != "" && !strings.HasPrefix(e.Type, "herdr.") {
			return a
		}
		if a := f.Aliases[e.Pane]; a != "" {
			return a
		}
		return e.Pane
	}
	type call struct {
		name, agent string
		at          int64
	}
	calls := map[string]call{}
	timedOut := map[string]bool{}
	for _, e := range evs {
		if e.Type == "brain.tool_timeout" {
			timedOut[str(e.Payload, "id")] = true
		}
	}
	var turnLat, brainLat []float64
	var frag *Speech
	lastFrag := int64(-1 << 62)
	flush := func() {
		if frag != nil && strings.TrimSpace(frag.Text) != "" {
			frag.Text = strings.TrimSpace(frag.Text)
			f.Voice = append(f.Voice, *frag)
		}
		frag = nil
	}
	lastSpawn := -1
	for _, e := range evs {
		p := e.Payload
		switch e.Type {
		case "brain.tool_call":
			args, _ := p["args"].(map[string]any)
			calls[str(p, "id")] = call{name: str(p, "name"), agent: str(args, "agent"), at: e.AtMS}
		case "brain.tool_result":
			c := calls[str(p, "id")]
			name := orStr(str(p, "name"), c.name)
			switch name {
			case "run_cli", "read_screen", "investigate":
			default:
				continue
			}
			a := Action{AtMS: e.AtMS, Kind: ActCheck, Agent: c.agent, Tool: name, Outcome: OutcomeOK}
			res := str(p, "result")
			switch {
			case timedOut[str(p, "id")]:
				a.Outcome = OutcomeTimeout
			case failedResult(res):
				a.Outcome = OutcomeError
			case name == "investigate":
				a.Kind = ActCheckStarted
			}
			f.Actions = append(f.Actions, a)
		case "investigation.done":
			f.Actions = append(f.Actions, Action{AtMS: e.AtMS, Kind: ActCheck, Agent: str(p, "agent"), Tool: "investigate", Outcome: OutcomeOK})
			f.News = append(f.News, News{AtMS: e.AtMS, Kind: NewsResult, Agent: str(p, "agent"), Detail: "investigation done"})
		case "investigation.error":
			f.Actions = append(f.Actions, Action{AtMS: e.AtMS, Kind: ActCheck, Agent: str(p, "agent"), Tool: "investigate", Outcome: OutcomeError, Detail: str(p, "error")})
			f.News = append(f.News, News{AtMS: e.AtMS, Kind: NewsResult, Agent: str(p, "agent"), Detail: "investigation failed: " + str(p, "error")})
		case "supervisor.spawn":
			f.Actions = append(f.Actions, Action{AtMS: e.AtMS, Kind: ActSpawn, Agent: str(p, "kind"), Detail: str(p, "kind") + " in " + lastPath(str(p, "dir")), Outcome: OutcomeOK})
			lastSpawn = len(f.Actions) - 1
		case "supervisor.spawn_ready":
			if lastSpawn >= 0 {
				f.Actions[lastSpawn].Agent = orStr(str(p, "agent"), f.Actions[lastSpawn].Agent)
			}
		case "supervisor.spawn_error", "supervisor.spawn_not_ready", "supervisor.spawn_refused":
			if lastSpawn >= 0 && e.Type != "supervisor.spawn_refused" {
				f.Actions[lastSpawn].Outcome = OutcomeError
			}
			f.News = append(f.News, News{AtMS: e.AtMS, Kind: NewsSpawnFailed, Agent: str(p, "agent"), Detail: e.Type})
		case "supervisor.close":
			f.Actions = append(f.Actions, Action{AtMS: e.AtMS, Kind: ActClose, Agent: str(p, "agent"), Outcome: OutcomeOK})
		case "supervisor.close_error":
			f.Actions = append(f.Actions, Action{AtMS: e.AtMS, Kind: ActClose, Agent: str(p, "agent"), Outcome: OutcomeError, Detail: str(p, "error")})
		case "pending.confirmed":
			f.Answers = append(f.Answers, Action{AtMS: e.AtMS, Kind: ActKeys, Agent: alias(e), Answer: str(p, "answer"), Detail: strings.Join(strs(p, "keys"), " ")})
		case "herdr.send":
			a := Action{AtMS: e.AtMS, Agent: alias(e), Outcome: OutcomeOK}
			if str(p, "error") != "" {
				a.Outcome = OutcomeError
			}
			keys := strs(p, "keys")
			switch {
			case str(p, "kind") == "prompt":
				a.Kind, a.Detail = ActPrompt, str(p, "text")
			case len(keys) == 1 && (keys[0] == "ctrl+c" || keys[0] == "C-c"):
				a.Kind, a.Detail = ActInterrupt, keys[0]
			default:
				a.Kind, a.Detail = ActKeys, strings.Join(keys, " ")
			}
			f.Sends = append(f.Sends, a)
		case "herdr.transition":
			if n, ok := transitionNews(alias(e), str(p, "from"), str(p, "to")); ok {
				n.AtMS = e.AtMS
				f.Transitions = append(f.Transitions, n)
			}
		case "voice.tool_result":
			if s := strings.TrimSpace(str(p, "spoken")); s != "" {
				f.Orchestrator = append(f.Orchestrator, Speech{AtMS: e.AtMS, Side: SideOrchestrator, Text: s})
			}
		case "voice.push":
			if str(p, "urgency") == "silent" || str(p, "from") == "daemon" {
				continue
			}
			if s := strings.TrimSpace(str(p, "text")); s != "" {
				f.Orchestrator = append(f.Orchestrator, Speech{AtMS: e.AtMS, Side: SideOrchestrator, Text: s, Push: true})
			}
		case "voice.transcript.out":
			t := str(p, "text")
			if frag == nil || e.AtMS-lastFrag > voiceGapMS {
				flush()
				frag = &Speech{AtMS: e.AtMS, Side: SideVoice}
			}
			frag.Text += t
			lastFrag = e.AtMS
		case "voice.turn":
			flush()
		case "supervisor.relay":
			if t := orStr(str(p, "heard"), str(p, "said")); t != "" {
				f.User = append(f.User, UserLine{AtMS: e.AtMS, Text: t})
			}
		case "turn.latency":
			if v, ok := num(p, "total_ms"); ok {
				turnLat = append(turnLat, v)
			}
		case "brain.turn":
			if v, ok := num(p, "dur_ms"); ok && str(p, "trigger") == "ceo" {
				brainLat = append(brainLat, v)
			}
		}
	}
	flush()
	f.LatencyMS = turnLat
	if len(f.LatencyMS) == 0 {
		f.LatencyMS = brainLat
	}
	return f
}

// failedResult reports whether a tool result says the tool failed.
func failedResult(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(low, "error:") {
		return true
	}
	for _, p := range []string{"error", "failed", "timed out", "timeout", "could not", "couldn't", "no cli", "not available", "unavailable"} {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// transitionNews is the news a worker state change carries: a finish (to
// done, or from working to idle) or a block.
func transitionNews(agent, from, to string) (News, bool) {
	switch {
	case to == "blocked":
		return News{Kind: NewsBlocked, Agent: agent, Detail: from + " → blocked"}, true
	case to == "done" && from != "new" && from != "unknown" && from != "":
		return News{Kind: NewsFinished, Agent: agent, Detail: from + " → done"}, true
	case to == "idle" && from == "working":
		return News{Kind: NewsFinished, Agent: agent, Detail: "working → idle"}, true
	}
	return News{}, false
}

// AttachAnswers sets each keys action's Answer from the pending answer
// logged within a second of it.
func AttachAnswers(acts []Action, answers []Action) {
	for i := range acts {
		if acts[i].Kind != ActKeys || acts[i].Answer != "" {
			continue
		}
		for _, a := range answers {
			if d := a.AtMS - acts[i].AtMS; d >= -1000 && d <= 1000 {
				acts[i].Answer = a.Answer
				break
			}
		}
	}
}

// SortRun puts a run's lines, actions and news in time order.
func SortRun(r *Run) {
	sort.SliceStable(r.User, func(i, j int) bool { return r.User[i].AtMS < r.User[j].AtMS })
	sort.SliceStable(r.Speech, func(i, j int) bool { return r.Speech[i].AtMS < r.Speech[j].AtMS })
	sort.SliceStable(r.Actions, func(i, j int) bool { return r.Actions[i].AtMS < r.Actions[j].AtMS })
	sort.SliceStable(r.News, func(i, j int) bool { return r.News[i].AtMS < r.News[j].AtMS })
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func lastPath(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

var (
	timelineNow   = regexp.MustCompile(`^(\S+) is now (\w+)`)
	timelineArrow = regexp.MustCompile(`^(.+?): (\w+) → (\w+)`)
)

// NewsFromTimeline reads a record's timeline line about a worker change:
// the mock world's "<alias> is now done; …" (tiers 1 and 2) or the
// real world's "<alias>: working → idle (herdr)" (tier 3). agent, when
// set, is the line's agent.
func NewsFromTimeline(atMS int64, agent, text string) (News, bool) {
	if m := timelineArrow.FindStringSubmatch(text); m != nil {
		n, ok := transitionNews(orStr(agent, m[1]), m[2], m[3])
		if strings.HasPrefix(m[1], "new agent in pane") && agent == "" {
			n.Agent = ""
		}
		n.AtMS = atMS
		return n, ok
	}
	if m := timelineNow.FindStringSubmatch(text); m != nil {
		// The mock world reports the new state only; any move to done or
		// blocked is news, and idle after a scripted change is a finish.
		to := m[2]
		from := "working"
		n, ok := transitionNews(orStr(agent, m[1]), from, to)
		n.AtMS = atMS
		return n, ok
	}
	return News{}, false
}

// ConversationMS: in a real log, news within this long after the user's
// last line is news during a conversation (and graded).
const ConversationMS = 5 * 60 * 1000

// RunFromLog builds a run from an event log alone (a real session in
// `sheepdog scorecard`): what the daemon received, what the voice model
// and the orchestrator said, what was sent, and the workers' finishes and
// blocks. No judge and no scenario: only the programmatic checks apply.
func RunFromLog(label string, evs []Event) Run {
	f := ReadFacts(evs)
	r := Run{Tier: "real", Scenario: label, Repeat: 1, Status: "pass", HasRecord: true, HasEvents: len(evs) > 0, Heard: SideOrchestrator}
	r.User = f.User
	r.Speech = append(r.Speech, f.Orchestrator...)
	if len(f.Voice) > 0 {
		r.Heard = SideVoice
		r.Speech = append(r.Speech, f.Voice...)
	}
	r.Actions = append(append(r.Actions, f.Sends...), f.Actions...)
	AttachAnswers(r.Actions, f.Answers)
	SortRun(&r)
	// A real log spans hours with nobody on the phone: news is graded only
	// within a conversation (the user spoke in the ConversationMS before).
	for _, n := range append(append([]News(nil), f.Transitions...), f.News...) {
		talking := false
		for _, u := range r.User {
			if u.AtMS <= n.AtMS && n.AtMS-u.AtMS <= ConversationMS {
				talking = true
			}
		}
		if talking {
			r.News = append(r.News, n)
		} else {
			r.NewsOutside++
		}
	}
	r.LatencyMS = f.LatencyMS
	r.Turns = len(r.User)
	seen := map[string]bool{}
	for _, a := range f.Aliases {
		if !seen[a] {
			seen[a] = true
			r.Agents = append(r.Agents, Agent{Alias: a})
		}
	}
	if len(evs) > 0 {
		r.EndMS = evs[len(evs)-1].AtMS
	}
	SortRun(&r)
	return r
}
