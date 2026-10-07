package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// simWorld is one conversation's daemon, in process: the real supervisor
// and orchestrator over a mock herdr whose panes are the scenario's workers, a
// pusher that delivers announcements to the simulated user, and a
// recorder of every event. Everything lives under a temp directory; no
// port is opened and the real data dir is never touched.
type simWorld struct {
	sc         SimScenario
	scale      float64 // multiplies every scripted delay (offline: fast)
	start      time.Time
	dir        string
	fixtureDir string

	brain *brain.Brain
	sup   *supervisor.Supervisor
	mock  *inject.Herdr
	herd  *simHerdr
	push  *simPusher
	rec   *simRecorder

	runCancel context.CancelFunc
	runDone   chan struct{}

	mu       sync.Mutex
	panes    map[string]string // alias -> pane id
	aliasOf  map[string]string // pane id -> alias
	kinds    map[string]string // alias -> kind
	states   map[string]herdr.State
	screens  map[string]string        // alias -> current screen
	resolved map[string]string        // screen name -> text (resolveScreens)
	pending  map[string][]*time.Timer // alias -> changes a prompt set in motion
	fired    map[int]bool
	timers   []*time.Timer
	timeline []SimLine // worker changes, for the judge's facts
	closed   bool
}

// SimLine is one timed line of a conversation or its timeline.
type SimLine struct {
	AtMS int64 `json:"at_ms"`
	// Who is ceo, sheepdog, push (sheepdog unprompted), worker (a scripted
	// worker change) or action (something sheepdog sent to a worker).
	Who  string `json:"who"`
	Text string `json:"text"`
	// Agent is the worker an action or change is about.
	Agent string `json:"agent,omitempty"`
	// Kind and Detail are an action's kind (prompt, keys, interrupt) and
	// payload (the prompt text, the keys).
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Beat is the agenda beat a user line (or a scripted wait) carries
	// out (1-based; 0 for a line that does not move the agenda on), and
	// Tag why such a line was said: answer (to sheepdog's question) or
	// again (the user repeats or rephrases: user effort).
	Beat int    `json:"beat,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

// simWorldOptions are what a world is built from.
type simWorldOptions struct {
	brainCfg brain.Config
	model    brain.Model
	// fallbacks are more providers registered on the orchestrator (id to
	// model), the targets of its failover.
	fallbacks  map[string]brain.Model
	fixtureDir string
	meter      *Meter
	// events, when set, receives every logged event (the run's JSONL).
	events func(SimLogEvent)
	// timeScale multiplies every scripted delay (default 1).
	timeScale float64
}

// SimLogEvent is one logged daemon event of a simulated run.
type SimLogEvent struct {
	AtMS    int64  `json:"at_ms"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Pane    string `json:"pane,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

func newSimWorld(ctx context.Context, sc SimScenario, o simWorldOptions) (*simWorld, error) {
	dir, err := os.MkdirTemp("", "sheepdog-sim-")
	if err != nil {
		return nil, err
	}
	w := &simWorld{
		sc: sc, start: time.Now(), dir: dir, fixtureDir: o.fixtureDir,
		panes: map[string]string{}, aliasOf: map[string]string{}, kinds: map[string]string{},
		states: map[string]herdr.State{}, screens: map[string]string{}, fired: map[int]bool{}, pending: map[string][]*time.Timer{},
	}
	if err := w.resolveScreens(); err != nil {
		w.cleanupDir()
		return nil, err
	}
	w.scale = o.timeScale
	if w.scale <= 0 {
		w.scale = 1
	}
	w.rec = &simRecorder{w: w, meter: o.meter, sink: o.events}
	w.push = &simPusher{w: w, ch: make(chan SimLine, 64)}

	aliases := map[string]string{}
	for i, a := range sc.Agents {
		pane := fmt.Sprintf("w%d:p1", i+1)
		w.panes[a.Alias], w.aliasOf[pane], w.kinds[a.Alias] = pane, a.Alias, orDefault(a.Kind, "codex")
		aliases[pane] = a.Alias
	}
	store, err := state.Open(filepath.Join(dir, "state.json"), state.Options{Aliases: aliases})
	if err != nil {
		w.cleanupDir()
		return nil, err
	}
	w.mock = inject.NewHerdr(nil, w.rec)
	w.herd = &simHerdr{inner: w.mock, w: w}

	cfg := o.brainCfg
	cfg.DataDir = filepath.Join(dir, "brain")
	b, err := brain.New(cfg, o.model, w.rec)
	if err != nil {
		w.cleanupDir()
		return nil, err
	}
	for id, m := range o.fallbacks {
		if err := b.AddProvider(id, m); err != nil {
			w.cleanupDir()
			return nil, err
		}
	}
	w.brain = b
	w.rec.model = b.Model()
	empty := filepath.Join(dir, "none")
	sup, err := supervisor.New(store, w.herd, &simQuerier{w: w}, w.push, w.rec, supervisor.Config{
		DataDir: dir, Brain: b, ReplyWait: -1,
		// Never the real ~/.claude or ~/.codex.
		WorkerLog: workerlog.New(workerlog.Options{ClaudeProjects: empty, CodexSessions: empty}),
	})
	if err != nil {
		_ = b.Close()
		w.cleanupDir()
		return nil, err
	}
	w.sup = sup
	runCtx, cancel := context.WithCancel(context.Background())
	w.runCancel, w.runDone = cancel, make(chan struct{})
	go func() { defer close(w.runDone); sup.Run(runCtx) }()
	sup.SetVoiceConnected(true)

	for _, n := range sc.Notes {
		b.Noted(n)
	}
	for _, a := range sc.Agents {
		screen := w.screenText(a.Screen)
		st := herdr.State(orDefault(a.State, "idle"))
		w.setWorker(ctx, a.Alias, screen, st, true)
	}
	if len(sc.Transcript) > 0 {
		seedTranscript(b, sc.Transcript)
	}
	// Let the announce turns of the initial blocks settle before the user
	// speaks, as on a phone that connects to a workers already running.
	b.Quiesce()
	for i, e := range sc.Events {
		if e.AtMS > 0 {
			i, e := i, e
			w.schedule(time.Duration(e.AtMS)*time.Millisecond, func() { w.apply(context.Background(), i, e, e.Agent) })
		}
	}
	return w, nil
}

func (w *simWorld) since() int64 { return time.Since(w.start).Milliseconds() }

// setWorker puts a worker's screen and state into the mock herdr and, on
// a state change (or at start), reports the transition to the supervisor
// exactly like the herdr watcher would.
func (w *simWorld) setWorker(ctx context.Context, alias, screen string, st herdr.State, initial bool) {
	w.mu.Lock()
	pane, kind := w.panes[alias], w.kinds[alias]
	from := w.states[alias]
	w.states[alias], w.screens[alias] = st, screen
	w.mu.Unlock()
	w.mock.SetScreen(pane, kind, screen, st)
	if initial || from != st {
		w.sup.HandleTransition(ctx, herdr.Transition{PaneID: pane, From: from, To: st, At: time.Now(), Agent: kind, WorkspaceID: "ws-" + alias, WorkspaceLabel: alias, Source: state.SourceInject})
	}
}

// apply carries out event i on target (the trigger's agent when the
// event names none), once unless it repeats.
func (w *simWorld) apply(ctx context.Context, i int, e SimEvent, target string) {
	w.mu.Lock()
	if w.closed || (w.fired[i] && !e.Repeat) {
		w.mu.Unlock()
		return
	}
	w.fired[i] = true
	if e.Agent != "" {
		target = e.Agent
	}
	screen, st := w.screens[target], w.states[target]
	w.mu.Unlock()
	if e.Screen != "" {
		screen = w.screenText(e.Screen)
	}
	if e.AppendScreen != "" {
		screen = strings.TrimRight(screen, "\n") + "\n" + e.AppendScreen + "\n"
	}
	if e.State != "" {
		st = herdr.State(e.State)
	}
	w.mu.Lock()
	w.timeline = append(w.timeline, SimLine{AtMS: w.since(), Who: "worker", Agent: target, Text: fmt.Sprintf("%s is now %s; its screen ends: %s", target, st, oneLineTail(screen, 6))})
	w.mu.Unlock()
	w.setWorker(ctx, target, screen, st, false)
}

// startWorking makes a prompted idle or finished worker busy, as a real
// one is a moment after it gets a prompt (herdr reports working). On its
// own goroutine: Prompt runs inside an orchestrator tool call.
func (w *simWorld) startWorking(alias string) {
	w.mu.Lock()
	st, screen := w.states[alias], w.screens[alias]
	w.mu.Unlock()
	if st == herdr.StateWorking || st == herdr.StateBlocked {
		return
	}
	w.schedule(50*time.Millisecond, func() { w.setWorker(context.Background(), alias, screen, herdr.StateWorking, false) })
}

// trigger fires the events that wait on what sheepdog just did to alias.
// An interrupt also cancels the changes a prompt to alias set in motion:
// a worker stopped mid-task does not finish it.
func (w *simWorld) trigger(kind, alias, text string) bool {
	if kind == ActionInterrupt {
		w.mu.Lock()
		for _, t := range w.pending[alias] {
			t.Stop()
		}
		delete(w.pending, alias)
		w.mu.Unlock()
	}
	fired := false
	for i, e := range w.sc.Events {
		var hit bool
		switch kind {
		case ActionPrompt:
			hit = e.OnPromptTo == alias && (e.PromptMatches == "" || regexp.MustCompile("(?i)"+e.PromptMatches).MatchString(text))
		case ActionKeys:
			hit = e.OnKeysTo == alias
		case ActionInterrupt:
			hit = e.OnInterruptTo == alias
		}
		if hit {
			fired = true
			i, e := i, e
			// After startWorking's working transition, so a finish is a
			// working -> done finish.
			d := max(time.Duration(e.DelayMS)*time.Millisecond, 200*time.Millisecond)
			t := w.schedule(d, func() { w.apply(context.Background(), i, e, alias) })
			if kind == ActionPrompt && t != nil {
				w.mu.Lock()
				w.pending[alias] = append(w.pending[alias], t)
				w.mu.Unlock()
			}
		}
	}
	return fired
}

// answered is a worker's default reaction to keys no scenario event
// handles: a blocked worker leaves its prompt — declined (esc) back to
// its input line, otherwise on to work — as a real one would, with the
// menu gone from the screen (ScreenAfterAnswer), so the supervisor does
// not keep seeing a prompt that was answered.
func (w *simWorld) answered(alias string, keys []string) {
	w.mu.Lock()
	st, kind := w.states[alias], w.kinds[alias]
	w.mu.Unlock()
	if st != herdr.StateBlocked {
		return
	}
	declined := len(keys) == 1 && (keys[0] == "esc" || keys[0] == "escape")
	w.schedule(300*time.Millisecond, func() {
		w.mu.Lock()
		screen := w.screens[alias]
		w.mu.Unlock()
		next := herdr.StateWorking
		if declined {
			next = herdr.StateIdle
		}
		w.setWorker(context.Background(), alias, ScreenAfterAnswer(screen, kind, declined), next, false)
	})
}

// interrupted is a worker's default reaction to a Ctrl-C no scenario
// event handles: a working worker stops its turn and comes back to its
// input line, as a real one does, so the interrupt can be seen to take
// (the orchestrator's interrupt checks it, campaign D N5).
func (w *simWorld) interrupted(alias string) {
	w.mu.Lock()
	st, kind := w.states[alias], w.kinds[alias]
	w.mu.Unlock()
	if st != herdr.StateWorking {
		return
	}
	w.schedule(300*time.Millisecond, func() {
		w.mu.Lock()
		screen := w.screens[alias]
		w.mu.Unlock()
		w.setWorker(context.Background(), alias, ScreenAfterInterrupt(screen, kind), herdr.StateIdle, false)
	})
}

// afterTurn fires the events cued on the user's nth utterance, and on
// the agenda beat that utterance carried out (0: none).
func (w *simWorld) afterTurn(n, beat int) {
	for i, e := range w.sc.Events {
		if e.AfterTurn == n || (beat > 0 && e.AfterBeat == beat) {
			i, e := i, e
			w.schedule(time.Duration(e.DelayMS)*time.Millisecond, func() { w.apply(context.Background(), i, e, e.Agent) })
		}
	}
}

func (w *simWorld) schedule(d time.Duration, fn func()) *time.Timer {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	t := time.AfterFunc(time.Duration(float64(d)*w.scale), fn)
	w.timers = append(w.timers, t)
	return t
}

// say is one user utterance: recorded as the user's own input (so the
// one rail and the claim gate see it), relayed like the voice model's
// to_supervisor call, and answered with the reply the user hears.
func (w *simWorld) say(ctx context.Context, text string) (voice.ToolResult, error) {
	w.sup.ShepherdInput(supervisor.InputTyped, text)
	w.sup.OnVoiceEvent(voice.EventToolCall{Call: voice.ToolCall{Name: "to_supervisor"}})
	res, err := w.sup.ToSupervisor(ctx, text)
	if s := strings.TrimSpace(res.Spoken); s != "" {
		// What the voice then says is the assistant's own speech, as the
		// out transcript would report it.
		w.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: s, Final: true})
		w.sup.OnVoiceEvent(voice.EventTurnComplete{})
	}
	return res, err
}

// close ends the supervisor's background work first (Run's shutdown
// waits for every turn), then the orchestrator, then removes the temp dir.
func (w *simWorld) close() {
	w.mu.Lock()
	w.closed = true
	for _, t := range w.timers {
		t.Stop()
	}
	w.mu.Unlock()
	w.runCancel()
	<-w.runDone
	w.brain.Quiesce()
	_ = w.brain.Close()
	w.cleanupDir()
}

func (w *simWorld) cleanupDir() { _ = os.RemoveAll(w.dir) }

// facts is the ground truth for the judge: every worker's screen at the
// start and now, the scripted changes, and every action sheepdog sent.
func (w *simWorld) facts(initial map[string]string) string {
	acts := w.rec.actions() // before w.mu: the recorder takes w.mu under its own lock
	w.mu.Lock()
	defer w.mu.Unlock()
	ws := WorldState{Agents: w.sc.Agents, Kinds: map[string]string{}, States: map[string]string{}, Initial: initial, Screens: w.screens, Timeline: w.timeline, Actions: acts}
	for k, v := range w.kinds {
		ws.Kinds[k] = v
	}
	for k, v := range w.states {
		ws.States[k] = string(v)
	}
	return WorldFacts(ws)
}

// WorldState is a scripted world's state for the judge's facts.
type WorldState struct {
	Agents []SimAgent
	// Kinds and States are each worker's kind and state now, by alias.
	Kinds, States map[string]string
	// Initial and Screens are each worker's screen at the start and now.
	Initial, Screens map[string]string
	// Timeline are the scripted worker changes, Actions what sheepdog
	// sent to the workers.
	Timeline, Actions []SimLine
}

// WorldFacts renders the judge's ground truth: every worker's screen at
// the start and what changed since, the scripted changes and every
// action sheepdog sent (shared by the text sim and the voice bench).
func WorldFacts(ws WorldState) string {
	var b strings.Builder
	for _, a := range ws.Agents {
		fmt.Fprintf(&b, "Worker %s (%s), now %s.\n", a.Alias, ws.Kinds[a.Alias], ws.States[a.Alias])
		if a.CLIHangs {
			fmt.Fprintf(&b, "  run_cli and investigations of %s start but never finish: they time out or run on without a result, so nothing they would show was verified. Saying a check was started or is running is true; saying it was checked or verified is not.\n", a.Alias)
		} else if a.Answer != "" {
			fmt.Fprintf(&b, "  What an investigation of %s finds: %s\n", a.Alias, a.Answer)
		}
		b.WriteString("  Screen at the start (last lines):\n")
		for _, l := range lastLines(ws.Initial[a.Alias], 25) {
			b.WriteString("    " + l + "\n")
		}
		if now := ws.Screens[a.Alias]; now != ws.Initial[a.Alias] {
			if added, ok := strings.CutPrefix(now, ws.Initial[a.Alias]); ok {
				// The usual case: the worker wrote more below what was
				// there. Show only the new lines, so old output (a test
				// that failed before the fix) is not read as current.
				b.WriteString("  New output on the screen since the start (newest last):\n")
				for _, l := range lastLines(strings.Trim(added, "\n"), 25) {
					b.WriteString("    " + l + "\n")
				}
			} else {
				b.WriteString("  Screen at the end (last lines, newest last):\n")
				for _, l := range lastLines(now, 25) {
					b.WriteString("    " + l + "\n")
				}
			}
		}
	}
	b.WriteString("Notes: a worker screen shows the worker's own history, including what it was asked before this call; only the actions listed below were sent by sheepdog during the call. Every prompt sheepdog sends ends with a standard sign-off request the daemon appends (\"When you finish, end with three short lines for someone who cannot see the screen…\"); that is not part of what sheepdog decided.\n")
	lines := append(append([]SimLine(nil), ws.Timeline...), ws.Actions...)
	sortLines(lines)
	if len(lines) == 0 {
		b.WriteString("No scripted change happened to any worker during the call (each kept doing what its screen shows), and sheepdog sent nothing to any worker.\n")
	}
	for _, l := range lines {
		fmt.Fprintf(&b, "[t+%ds] %s\n", l.AtMS/1000, l.Text)
	}
	return b.String()
}

// resolveScreens reads every screen the scenario names: a file in the
// scenario's screens/ directory, a fixture under testdata/herdr, or —
// only when it spans lines — inline text. A one-line name that is no file
// is an error, not a one-line screen (a typo would otherwise run the
// scenario against the file name).
func (w *simWorld) resolveScreens() error {
	r, err := ResolveScreens(w.sc, w.fixtureDir)
	w.resolved = r
	return err
}

// ResolveScreens reads every screen sc names (its agents' and events'):
// a file in the scenario's screens/ directory, a fixture under
// fixtureDir, or — only when it spans lines — inline text. A one-line
// name that is no file is an error.
func ResolveScreens(sc SimScenario, fixtureDir string) (map[string]string, error) {
	resolved := map[string]string{}
	names := []string{}
	for _, a := range sc.Agents {
		names = append(names, a.Screen)
	}
	for _, e := range sc.Events {
		if e.Screen != "" {
			names = append(names, e.Screen)
		}
	}
	for _, n := range names {
		if strings.Contains(n, "\n") {
			resolved[n] = n
			continue
		}
		var err error
		var text string
		if sc.screenDir != "" {
			text, err = readFixture(n, sc.screenDir)
		}
		if sc.screenDir == "" || err != nil {
			text, err = readFixture(n, fixtureDir)
		}
		if err != nil {
			return resolved, fmt.Errorf("screen %q: %w", n, err)
		}
		resolved[n] = text
	}
	return resolved, nil
}

func (w *simWorld) screenText(s string) string { return w.resolved[s] }

func oneLineTail(s string, n int) string {
	var keep []string
	for _, l := range lastLines(s, n*3) {
		if t := strings.TrimSpace(l); t != "" {
			keep = append(keep, t)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " / ")
}

// simHerdr is the mock herdr the supervisor talks to: the inject mock,
// plus the scenario's cues on what sheepdog sends.
type simHerdr struct {
	inner *inject.Herdr
	w     *simWorld
}

func (h *simHerdr) Read(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
	return h.inner.Read(ctx, paneID, source, lines)
}

func (h *simHerdr) Prompt(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
	info, err := h.inner.Prompt(ctx, paneID, text)
	if err == nil {
		alias := h.w.alias(paneID)
		// The mock appended the prompt to the screen; keep ours in step.
		if r, rerr := h.inner.Read(ctx, paneID, herdr.ReadSource(""), 0); rerr == nil {
			h.w.mu.Lock()
			h.w.screens[alias] = r.Text
			h.w.mu.Unlock()
		}
		h.w.startWorking(alias)
		h.w.trigger(ActionPrompt, alias, text)
	}
	return info, err
}

func (h *simHerdr) SendKeys(ctx context.Context, paneID string, keys ...string) error {
	err := h.inner.SendKeys(ctx, paneID, keys...)
	if err == nil {
		kind := ActionKeys
		if len(keys) == 1 && keys[0] == "ctrl+c" {
			kind = ActionInterrupt
		}
		alias := h.w.alias(paneID)
		if !h.w.trigger(kind, alias, strings.Join(keys, " ")) {
			if kind == ActionKeys {
				h.w.answered(alias, keys)
			} else {
				h.w.interrupted(alias)
			}
		}
	}
	return err
}

func (h *simHerdr) Interrupt(ctx context.Context, paneID string) error {
	return h.SendKeys(ctx, paneID, "ctrl+c")
}

func (h *simHerdr) ListAgents(ctx context.Context) ([]herdr.AgentInfo, error) {
	return h.inner.ListAgents(ctx)
}

func (w *simWorld) alias(paneID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.aliasOf[paneID]
}

// simPusher delivers the supervisor's pushes to the user. A spoken push is
// also reported back as the voice would (sent, spoken, turn complete) on
// its own goroutine: Push may be called with supervisor locks held.
type simPusher struct {
	w  *simWorld
	ch chan SimLine
}

func (p *simPusher) Push(text string, u voice.Urgency) error { return p.PushTagged(text, u, nil) }

func (p *simPusher) PushTagged(text string, u voice.Urgency, _ map[string]any) error {
	if u == voice.UrgencySilent || strings.TrimSpace(text) == "" {
		return nil // context for the voice model, never heard
	}
	line := SimLine{AtMS: p.w.since(), Who: "push", Text: strings.TrimSpace(text)}
	select {
	case p.ch <- line:
	default: // a runaway: the user would not have heard 64 queued lines either
	}
	go func() {
		p.w.sup.OnVoiceEvent(voice.EventPush{Text: text, Urgency: u})
		p.w.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: text, Final: true})
		p.w.sup.OnVoiceEvent(voice.EventTurnComplete{})
	}()
	return nil
}

// HasListener implements supervisor.Listener: the user is always there.
func (p *simPusher) HasListener() bool { return true }

// drain returns the pushes that arrived so far.
func (p *simPusher) drain() []SimLine {
	var out []SimLine
	for {
		select {
		case l := <-p.ch:
			out = append(out, l)
		default:
			return out
		}
	}
}

// simQuerier answers run_cli and investigations about an agent with the
// scenario's scripted finding.
type simQuerier struct{ w *simWorld }

func (q *simQuerier) answerFor(workspace string) (SimAgent, bool) {
	for _, a := range q.w.sc.Agents {
		if workspace == "ws-"+a.Alias || strings.HasSuffix(workspace, "/"+a.Alias) || workspace == a.Alias {
			return a, a.Answer != "" || a.CLIHangs
		}
	}
	// The workspace is the pane's cwd, which the mock does not report:
	// with one scripted answer, that is the one.
	var hit SimAgent
	n := 0
	for _, a := range q.w.sc.Agents {
		if a.Answer != "" || a.CLIHangs {
			hit, n = a, n+1
		}
	}
	return hit, n == 1
}

func (q *simQuerier) Query(ctx context.Context, workspace, _ string) (string, error) {
	a, ok := q.answerFor(workspace)
	if !ok {
		return "", brain.ErrNoCLI
	}
	if a.CLIHangs {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return a.Answer, nil
}

func (q *simQuerier) Investigate(ctx context.Context, iq supervisor.InvestigationQuery) (supervisor.InvestigationResult, error) {
	a, ok := q.answerFor(iq.Workspace)
	if !ok {
		return supervisor.InvestigationResult{}, brain.ErrNoCLI
	}
	if a.CLIHangs {
		<-ctx.Done()
		return supervisor.InvestigationResult{}, ctx.Err()
	}
	d := time.Duration(a.AnswerDelayMS) * time.Millisecond
	if d == 0 {
		d = 3 * time.Second
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
		return supervisor.InvestigationResult{}, ctx.Err()
	}
	return supervisor.InvestigationResult{Backend: orDefault(a.Kind, "codex"), Answer: a.Answer}, nil
}

// simRecorder is the run's logger: it keeps the actions sent to workers
// and the orchestrator's turn outcomes, meters the orchestrator's tokens, and forwards
// every event to the run's sink.
type simRecorder struct {
	w     *simWorld
	meter *Meter
	model string
	sink  func(SimLogEvent)

	mu        sync.Mutex
	acts      []SimLine
	toolCalls []string
	fallbacks []string
	timeouts  int
	// failovers lists the orchestrator's failovers ("vertex to deepseek
	// (http_504)"); steps counts the answered orchestrator steps per provider;
	// answeredBy is the provider of the last answered step.
	failovers  []string
	steps      map[string]int
	answeredBy string
}

func (r *simRecorder) Log(source, typ, pane string, payload any) {
	m, _ := payload.(map[string]any)
	at := r.w.since()
	if r.sink != nil {
		r.sink(SimLogEvent{AtMS: at, Source: source, Type: typ, Pane: pane, Payload: payload})
	}
	alias := ""
	if typ == "herdr.send" {
		alias = r.w.alias(pane) // before r.mu: lock order is w.mu, then r.mu never nested
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch typ {
	case "herdr.send":
		switch m["kind"] {
		case "prompt":
			text := fmt.Sprint(m["text"])
			r.acts = append(r.acts, SimLine{AtMS: at, Who: "action", Agent: alias, Kind: ActionPrompt, Detail: text, Text: fmt.Sprintf("sheepdog sent %s the prompt %q", alias, text)})
		case "keys":
			keys := keysOf(m["keys"])
			kind, verb := ActionKeys, "answered %s's prompt with keys %s"
			if keys == "ctrl+c" {
				kind, verb = ActionInterrupt, "interrupted %s (%s)"
			}
			r.acts = append(r.acts, SimLine{AtMS: at, Who: "action", Agent: alias, Kind: kind, Detail: keys, Text: "sheepdog " + fmt.Sprintf(verb, alias, keys)})
		}
	case "brain.tool_call":
		if name, _ := m["name"].(string); name != "" {
			r.toolCalls = append(r.toolCalls, name)
		}
	case "brain.fallback":
		e, _ := m["error"].(string)
		reason, _ := m["reason"].(string)
		r.fallbacks = append(r.fallbacks, strings.TrimSpace(reason+": "+firstLine(e)))
		if isTimeoutText(e) || reason == "timeout" {
			r.timeouts++
		}
	case "brain.failover":
		from, _ := m["from"].(string)
		to, _ := m["to"].(string)
		reason, _ := m["reason"].(string)
		r.failovers = append(r.failovers, fmt.Sprintf("%s to %s (%s)", from, to, reason))
	case "brain.step":
		if r.meter != nil {
			model, _ := m["model"].(string)
			if model == "" {
				model = r.model // a failover step bills its own model
			}
			r.meter.Add("brain", model, stepUsage(m))
		}
		if _, failed := m["error"]; !failed {
			if p, _ := m["provider"].(string); p != "" {
				if r.steps == nil {
					r.steps = map[string]int{}
				}
				r.steps[p]++
				r.answeredBy = p
			}
		}
	}
}

func (r *simRecorder) actions() []SimLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SimLine(nil), r.acts...)
}

func keysOf(v any) string {
	switch k := v.(type) {
	case []string:
		return strings.Join(k, " ")
	case []any:
		parts := make([]string, len(k))
		for i, x := range k {
			parts[i] = fmt.Sprint(x)
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprint(v)
}

func isTimeoutText(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "deadline") || strings.Contains(s, "timeout") || strings.Contains(s, "timed out")
}
