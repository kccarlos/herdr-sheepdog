// Package world drives an eval sim scenario's scripted workers into a
// running daemon over HTTP: it registers the workers as synthetic panes
// of the daemon's mock herdr (/api/inject/pane), follows /events for what
// sheepdog sends them (herdr.send: prompts, keys, ctrl+c) and plays the
// scenario's cues (at a time, after the CEO's nth utterance, on a
// prompt, keys or an interrupt) through /api/inject/state and
// /api/inject/screen, exactly as the in-process text sim's world does
// (internal/eval simworld.go). It keeps the ground truth the judge reads
// (eval.WorldFacts).
package world

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// Options configure a world.
type Options struct {
	// BaseURL is the daemon; Token its web.token, if any.
	BaseURL, Token string
	// FixtureDir is testdata/herdr (screens not in the scenario's own
	// screens/ directory).
	FixtureDir string
	// OnFire, when set, is told when a scripted event lands (index into
	// the scenario's events), e.g. to make the simulated CEO talk over
	// the push it sets off.
	OnFire func(i int, e eval.SimEvent)
	Log    io.Writer
}

// World is one scenario's scripted workers inside a daemon.
type World struct {
	sc    eval.SimScenario
	o     Options
	start time.Time
	http  *http.Client

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	panes    map[string]string // alias -> pane id
	aliasOf  map[string]string // pane id -> alias
	kinds    map[string]string
	states   map[string]string
	screens  map[string]string
	initial  map[string]string
	resolved map[string]string
	pending  map[string][]*time.Timer
	fired    map[int]bool
	timers   []*time.Timer
	timeline []eval.SimLine
	acts     []eval.SimLine
	errs     []string
	closed   bool
}

// New resolves the scenario's screens; nothing is sent yet.
func New(sc eval.SimScenario, o Options) (*World, error) {
	resolved, err := eval.ResolveScreens(sc, o.FixtureDir)
	if err != nil {
		return nil, err
	}
	w := &World{
		sc: sc, o: o, http: &http.Client{Timeout: 60 * time.Second}, resolved: resolved,
		panes: map[string]string{}, aliasOf: map[string]string{}, kinds: map[string]string{}, states: map[string]string{},
		screens: map[string]string{}, initial: map[string]string{}, pending: map[string][]*time.Timer{}, fired: map[int]bool{},
	}
	for i, a := range sc.Agents {
		pane := fmt.Sprintf("w%d:p1", i+1)
		w.panes[a.Alias], w.aliasOf[pane] = pane, a.Alias
		w.kinds[a.Alias] = orDefault(a.Kind, "codex")
	}
	return w, nil
}

// Start follows /events, registers every worker (roster order) and arms
// the timed cues. The clock starts now.
func (w *World) Start(ctx context.Context) error {
	w.start = time.Now()
	w.ctx, w.cancel = context.WithCancel(context.Background())
	ready := make(chan error, 1)
	w.wg.Add(1)
	go func() { defer w.wg.Done(); w.follow(ready) }()
	select {
	case err := <-ready:
		if err != nil {
			return err
		}
	case <-time.After(5 * time.Second):
		return errors.New("world: /events did not connect")
	}
	for _, a := range w.sc.Agents {
		screen := w.resolved[a.Screen]
		st := orDefault(a.State, "idle")
		w.mu.Lock()
		w.states[a.Alias], w.screens[a.Alias], w.initial[a.Alias] = st, screen, screen
		w.mu.Unlock()
		if err := w.post(ctx, "/api/inject/pane", map[string]any{"pane_id": w.panes[a.Alias], "alias": a.Alias, "kind": w.kinds[a.Alias], "state": st, "screen": screen}); err != nil {
			return fmt.Errorf("world: register %s: %w", a.Alias, err)
		}
	}
	for i, e := range w.sc.Events {
		if e.AtMS > 0 {
			i, e := i, e
			w.schedule(time.Duration(e.AtMS)*time.Millisecond, func() { w.apply(i, e, e.Agent) })
		}
	}
	return nil
}

func (w *World) since() int64 { return time.Since(w.start).Milliseconds() }

// AfterTurn fires the cues waiting on the CEO's nth utterance (call it
// once the answer to that utterance has been heard).
func (w *World) AfterTurn(n int) {
	for i, e := range w.sc.Events {
		if e.AfterTurn == n {
			i, e := i, e
			w.schedule(time.Duration(e.DelayMS)*time.Millisecond, func() { w.apply(i, e, e.Agent) })
		}
	}
}

func (w *World) schedule(d time.Duration, fn func()) *time.Timer {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	t := time.AfterFunc(d, fn)
	w.timers = append(w.timers, t)
	return t
}

// apply carries out event i on target (the trigger's agent when the
// event names none), once unless it repeats.
func (w *World) apply(i int, e eval.SimEvent, target string) {
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
		screen = w.resolved[e.Screen]
	}
	if e.AppendScreen != "" {
		screen = strings.TrimRight(screen, "\n") + "\n" + e.AppendScreen + "\n"
	}
	if e.State != "" {
		st = e.State
	}
	w.mu.Lock()
	w.timeline = append(w.timeline, eval.SimLine{AtMS: w.since(), Who: "worker", Agent: target, Text: fmt.Sprintf("%s is now %s; its screen ends: %s", target, st, oneLineTail(screen, 6))})
	w.mu.Unlock()
	w.set(target, screen, st)
	if w.o.OnFire != nil {
		w.o.OnFire(i, e)
	}
}

// set puts a worker's screen and state into the daemon: a state change
// goes through the supervisor (/api/inject/state), a screen alone does
// not (/api/inject/screen), as the herdr watcher would report it.
func (w *World) set(alias, screen, st string) {
	w.mu.Lock()
	from := w.states[alias]
	w.states[alias], w.screens[alias] = st, screen
	pane := w.panes[alias]
	w.mu.Unlock()
	var err error
	if from != st {
		err = w.post(w.ctx, "/api/inject/state", map[string]any{"pane_id": pane, "state": st, "screen": screen})
	} else {
		err = w.post(w.ctx, "/api/inject/screen", map[string]any{"pane_id": pane, "screen": screen})
	}
	if err != nil {
		w.mu.Lock()
		w.errs = append(w.errs, fmt.Sprintf("%s -> %s: %v", alias, st, err))
		w.mu.Unlock()
	}
}

// onSend handles a herdr.send the daemon logged.
func (w *World) onSend(pane string, payload map[string]any) {
	w.mu.Lock()
	alias := w.aliasOf[pane]
	w.mu.Unlock()
	if alias == "" {
		return
	}
	at := w.since()
	switch payload["kind"] {
	case "prompt":
		text := fmt.Sprint(payload["text"])
		w.mu.Lock()
		w.acts = append(w.acts, eval.SimLine{AtMS: at, Who: "action", Agent: alias, Kind: eval.ActionPrompt, Detail: text, Text: fmt.Sprintf("sheepdog sent %s the prompt %q", alias, text)})
		// The mock appended the prompt to the screen; keep ours in step.
		w.screens[alias] += "\n› " + text + "\n"
		w.mu.Unlock()
		w.startWorking(alias)
		w.trigger(eval.ActionPrompt, alias, text)
	case "keys":
		keys := keysOf(payload["keys"])
		kind, verb := eval.ActionKeys, "answered %s's prompt with keys %s"
		if keys == "ctrl+c" {
			kind, verb = eval.ActionInterrupt, "interrupted %s (%s)"
		}
		w.mu.Lock()
		w.acts = append(w.acts, eval.SimLine{AtMS: at, Who: "action", Agent: alias, Kind: kind, Detail: keys, Text: "sheepdog " + fmt.Sprintf(verb, alias, keys)})
		w.mu.Unlock()
		if !w.trigger(kind, alias, keys) && kind == eval.ActionKeys {
			w.answered(alias, strings.Fields(keys))
		}
	}
}

// startWorking makes a prompted idle or finished worker busy, as a real
// one is a moment after it gets a prompt.
func (w *World) startWorking(alias string) {
	w.mu.Lock()
	st := w.states[alias]
	w.mu.Unlock()
	if st == "working" || st == "blocked" {
		return
	}
	w.schedule(50*time.Millisecond, func() {
		w.mu.Lock()
		screen := w.screens[alias]
		w.mu.Unlock()
		w.set(alias, screen, "working")
	})
}

// trigger fires the cues waiting on what sheepdog just did to alias. An
// interrupt also cancels what a prompt to alias set in motion.
func (w *World) trigger(kind, alias, text string) bool {
	if kind == eval.ActionInterrupt {
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
		case eval.ActionPrompt:
			hit = e.OnPromptTo == alias && (e.PromptMatches == "" || regexp.MustCompile("(?i)"+e.PromptMatches).MatchString(text))
		case eval.ActionKeys:
			hit = e.OnKeysTo == alias
		case eval.ActionInterrupt:
			hit = e.OnInterruptTo == alias
		}
		if !hit {
			continue
		}
		fired = true
		i, e := i, e
		d := max(time.Duration(e.DelayMS)*time.Millisecond, 200*time.Millisecond)
		t := w.schedule(d, func() { w.apply(i, e, alias) })
		if kind == eval.ActionPrompt && t != nil {
			w.mu.Lock()
			w.pending[alias] = append(w.pending[alias], t)
			w.mu.Unlock()
		}
	}
	return fired
}

// answered is a blocked worker's default reaction to keys no cue
// handles: declined (esc) back to its input line, otherwise on to work.
func (w *World) answered(alias string, keys []string) {
	w.mu.Lock()
	st, kind := w.states[alias], w.kinds[alias]
	w.mu.Unlock()
	if st != "blocked" {
		return
	}
	declined := len(keys) == 1 && (keys[0] == "esc" || keys[0] == "escape")
	w.schedule(300*time.Millisecond, func() {
		w.mu.Lock()
		screen := w.screens[alias]
		w.mu.Unlock()
		next, footer := "working", "• Running the approved step…"
		if declined {
			next, footer = "idle", "• Okay, I won't do that."
		}
		input := "› Ask Codex to do anything"
		if kind == "claude" {
			input = "❯ "
		}
		w.set(alias, strings.TrimRight(screen, "\n")+"\n\n"+footer+"\n\n"+input+"\n", next)
	})
}

// Facts is the judge's ground truth (eval.WorldFacts).
func (w *World) Facts() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	ws := eval.WorldState{Agents: w.sc.Agents, Kinds: copyMap(w.kinds), States: copyMap(w.states), Initial: copyMap(w.initial), Screens: copyMap(w.screens), Timeline: append([]eval.SimLine(nil), w.timeline...), Actions: append([]eval.SimLine(nil), w.acts...)}
	return eval.WorldFacts(ws)
}

// Actions are what sheepdog sent to the workers.
func (w *World) Actions() []eval.SimLine {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]eval.SimLine(nil), w.acts...)
}

// Timeline are the scripted worker changes.
func (w *World) Timeline() []eval.SimLine {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]eval.SimLine(nil), w.timeline...)
}

// Errors are the inject calls that failed.
func (w *World) Errors() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.errs...)
}

// Close stops the cues and the event stream.
func (w *World) Close() {
	w.mu.Lock()
	w.closed = true
	for _, t := range w.timers {
		t.Stop()
	}
	w.mu.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

func (w *World) post(ctx context.Context, path string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.o.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.o.Token)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// follow reads /events from now on and hands every herdr.send over.
func (w *World) follow(ready chan<- error) {
	u := w.o.BaseURL + "/events?since=" + strconv.FormatInt(time.Now().Add(-100*time.Millisecond).UnixMicro(), 10)
	req, err := http.NewRequestWithContext(w.ctx, http.MethodGet, u, nil)
	if err != nil {
		ready <- err
		return
	}
	if w.o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.o.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ready <- fmt.Errorf("world: /events: %w", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		ready <- fmt.Errorf("world: /events: %s", resp.Status)
		return
	}
	ready <- nil
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<23)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var ev struct {
			Type    string         `json:"type"`
			Pane    string         `json:"pane_id"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(line[6:], &ev) != nil || ev.Type != "herdr.send" {
			continue
		}
		w.onSend(ev.Pane, ev.Payload)
	}
}

// Querier answers run_cli and investigations with the scenario's
// scripted findings (SimAgent.Answer), like the text sim's: give it to
// the scratch daemon (app.Options.Querier).
type Querier struct {
	Agents []eval.SimAgent
}

func (q Querier) answerFor(workspace string) (eval.SimAgent, bool) {
	for _, a := range q.Agents {
		if workspace == "ws-"+a.Alias || strings.HasSuffix(workspace, "/"+a.Alias) || workspace == a.Alias {
			return a, a.Answer != ""
		}
	}
	// A synthetic pane reports no workspace: with one scripted answer,
	// that is the one.
	var hit eval.SimAgent
	n := 0
	for _, a := range q.Agents {
		if a.Answer != "" {
			hit, n = a, n+1
		}
	}
	return hit, n == 1
}

// Query implements supervisor.Querier.
func (q Querier) Query(_ context.Context, workspace, _ string) (string, error) {
	a, ok := q.answerFor(workspace)
	if !ok {
		return "", brain.ErrNoCLI
	}
	return a.Answer, nil
}

// Investigate implements supervisor.Investigator.
func (q Querier) Investigate(ctx context.Context, iq supervisor.InvestigationQuery) (supervisor.InvestigationResult, error) {
	a, ok := q.answerFor(iq.Workspace)
	if !ok {
		return supervisor.InvestigationResult{}, brain.ErrNoCLI
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

func oneLineTail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			keep = append([]string{t}, keep...)
		}
	}
	return strings.Join(keep, " / ")
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
