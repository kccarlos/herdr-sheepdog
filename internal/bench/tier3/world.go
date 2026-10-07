package tier3

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// Options configure tier-3 runs.
type Options struct {
	// Root is the scratch directory runs are made under.
	Root string
	// BenchRepo is the toy repo cloned per workspace
	// (/Users/kc/Documents/sheepdog-bench).
	BenchRepo string
	// EnvFile is the daemon's --env-file (the orchestrator's keys).
	EnvFile string
	// BrainModel is the daemon's brain.model.
	BrainModel string
	// SheepdogBin is the daemon binary (default: this executable).
	SheepdogBin string
	// HerdrBin is the herdr executable (default herdr).
	HerdrBin string
	// ClaudeModel is the default Claude worker model (default haiku).
	ClaudeModel string
	// CodexModel and CodexEffort are the Codex worker's model and
	// reasoning effort (the scarce free plan: the smallest model, low).
	CodexModel, CodexEffort string
	// Ports are the web ports runs take turns on.
	Ports chan int
	// NewSimUser and NewJudge are as in eval.SimOptions; nil runs the
	// scripted user and the fake judge.
	NewSimUser func(m *eval.Meter) eval.SimUser
	NewJudge   func(m *eval.Meter) eval.Judge
	// EventsDir, when set, gets each run's full daemon event log.
	EventsDir string
	// Keep keeps a run's scratch directory (clones, daemon data).
	Keep bool
	// Settle is how long to listen for pushes after a reply (default 4 s).
	Settle time.Duration
	// Voice, when set, makes the simulated user a caller: speech through
	// the tier-2 phone emulator into a scratch daemon with the real voice
	// model. Nil is the text router (the default).
	Voice *VoiceOptions
	// Log receives progress lines.
	Log io.Writer
}

// Record is a tier-3 run: the simulated-user record plus what only a
// real run has.
type Record struct {
	eval.SimRun
	Session string `json:"herdr_session"`
	// Isolation is the proof that the daemon saw only the bench panes.
	Isolation []string `json:"isolation,omitempty"`
	// Roster is the daemon's final roster (alias, spoken name, title).
	Roster []AgentRow `json:"roster,omitempty"`
	// Checks are the code checks' outcomes (events, screens, commands).
	Checks []string `json:"checks,omitempty"`
	// Excerpt are the run's key daemon events, for the write-up.
	Excerpt []string `json:"excerpt,omitempty"`
	// Screens are the workers' final screens (tail).
	Screens map[string]string `json:"screens,omitempty"`
	// Aliases maps the scenario's worker aliases to the daemon's.
	Aliases map[string]string `json:"aliases,omitempty"`
	// SetupMS is how long the setup took before the call.
	SetupMS int64 `json:"setup_ms"`
	// VoiceMode is set when the user spoke (Options.Voice); Voice then has
	// the call's audio side.
	VoiceMode bool      `json:"voice_mode,omitempty"`
	Voice     *VoiceRun `json:"voice,omitempty"`
	// EndState are the scenario's end-state checks against the world
	// after the call (Expect.EndState).
	EndState []northstar.EndCheck `json:"end_state,omitempty"`
}

// worker is a started worker.
type worker struct {
	Worker
	pane string
	repo string
	ws   string
}

// world is one run's herdr session, workers and daemon.
type world struct {
	sc     Scenario
	o      Options
	dir    string
	h      *Herdr
	d      *Daemon
	port   int
	start  time.Time
	logf   func(format string, a ...any)
	ws     map[string]string // label → workspace id
	byName map[string]*worker
	order  []*worker

	mu       sync.Mutex
	aliasOf  map[string]string // pane → scenario alias
	pushes   chan eval.SimLine
	actions  []eval.SimLine
	timeline []eval.SimLine
	stop     chan struct{}
	polled   chan struct{}
	// renamed maps a scenario alias to the daemon's (adoptAliases); sim
	// and expect are the scenario under the daemon's aliases.
	renamed map[string]string
	dlog    *os.File
	sim     eval.SimScenario
	expect  eval.SimExpect
}

// updatePrompt matches a CLI's own "update available" menu at start
// (Codex's defaults to "Update now"). The bench never answers it: on
// 2026-10-03 herdr reported Codex's update menu as idle, sheepdog's
// first prompt (text + Enter) chose "Update now", and Codex reinstalled
// itself (0.158.0 → 0.160.0).
var updatePrompt = regexp.MustCompile(`(?i)(update available|update now|new version of codex)`)

// trustPrompt matches the folder trust prompts of Claude Code and Codex.
var trustPrompt = regexp.MustCompile(`(?i)(trust this folder|do you trust|trust the (files|contents))`)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// repoOf is a scenario worker's repo clone ("" when unknown).
func (w *world) repoOf(worker string) string {
	if wk := w.byName[worker]; wk != nil {
		return wk.repo
	}
	return ""
}

// daemonAlias is the daemon's alias for a scenario worker.
func (w *world) daemonAlias(scenario string) string {
	if n, ok := w.renamed[scenario]; ok {
		return n
	}
	return scenario
}

func (w *world) since() int64 { return time.Since(w.start).Milliseconds() }

// setup builds the session, the clones and the workers, runs the
// scenario's setup steps, starts the daemon and proves isolation.
func (w *world) setup(ctx context.Context, rec *Record) error {
	w.h = &Herdr{Bin: w.o.HerdrBin, Session: rec.Session}
	hlog, err := os.Create(filepath.Join(w.dir, "herdr-server.log"))
	if err != nil {
		return err
	}
	defer func() { _ = hlog.Close() }()
	if err := w.h.Start(ctx, hlog); err != nil {
		return err
	}
	for _, spec := range w.sc.Workers {
		wk := &worker{Worker: spec}
		if wk.Model == "" {
			wk.Model = w.o.ClaudeModel
			if wk.Kind == KindCodex {
				wk.Model = w.o.CodexModel
			}
		}
		wk.repo = filepath.Join(w.dir, "repos", spec.Workspace)
		if _, err := os.Stat(wk.repo); err != nil {
			if out, err := exec.CommandContext(ctx, "git", "clone", "-q", w.o.BenchRepo, wk.repo).CombinedOutput(); err != nil {
				return fmt.Errorf("clone: %w: %s", err, out)
			}
		}
		var pane Pane
		if id, ok := w.ws[spec.Workspace]; ok {
			first := ""
			for _, o := range w.order {
				if o.ws == id {
					first = o.pane
					break
				}
			}
			pane, err = w.h.Split(ctx, first, wk.repo, nil)
			wk.ws = id
		} else {
			pane, err = w.h.CreateWorkspace(ctx, wk.repo, spec.Workspace, nil)
			wk.ws = pane.WorkspaceID
			w.ws[spec.Workspace] = pane.WorkspaceID
		}
		if err != nil {
			return fmt.Errorf("worker %s: pane: %w", spec.Alias, err)
		}
		wk.pane = pane.PaneID
		w.byName[spec.Alias], w.aliasOf[wk.pane] = wk, spec.Alias
		w.order = append(w.order, wk)
		time.Sleep(1500 * time.Millisecond) // the shell's prompt
		if err := w.h.Run(ctx, wk.pane, w.command(wk)); err != nil {
			return fmt.Errorf("worker %s: start: %w", spec.Alias, err)
		}
	}
	for _, wk := range w.order {
		if err := w.ready(ctx, wk); err != nil {
			return err
		}
	}
	for i, st := range w.sc.Setup {
		if err := w.step(ctx, st); err != nil {
			return fmt.Errorf("setup %d: %w", i, err)
		}
	}
	sock, err := w.h.SocketPath(ctx)
	if err != nil {
		return err
	}
	dlog, err := os.Create(filepath.Join(w.dir, "daemon.log"))
	if err != nil {
		return err
	}
	w.dlog = dlog
	w.d, err = StartDaemon(ctx, DaemonOptions{Bin: w.o.SheepdogBin, Port: w.port, Dir: filepath.Join(w.dir, "daemon"), Socket: sock, EnvFile: w.o.EnvFile, BrainModel: w.o.BrainModel, Voice: w.o.Voice != nil, Log: dlog})
	if err != nil {
		return err
	}
	rec.Isolation, err = w.isolation(ctx, sock)
	return err
}

// command is the shell line that starts a worker.
func (w *world) command(wk *worker) string {
	var argv []string
	switch wk.Kind {
	case KindClaude:
		argv = []string{"claude", "--model", wk.Model}
	case KindCodex:
		argv = []string{"codex", "-m", wk.Model, "-c", "model_reasoning_effort=" + orDefault(w.o.CodexEffort, "low")}
	}
	argv = append(argv, wk.Args...)
	for i, a := range argv {
		argv[i] = shellQuote(a)
	}
	return strings.Join(argv, " ")
}

func shellQuote(s string) string {
	if s != "" && regexp.MustCompile(`^[A-Za-z0-9_./=:,+-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ready waits for a worker to come up: answers (or leaves) its trust
// prompt, then waits for idle.
func (w *world) ready(ctx context.Context, wk *worker) error {
	deadline := time.Now().Add(90 * time.Second)
	answered := false
	for time.Now().Before(deadline) {
		screen, _ := w.h.Read(ctx, wk.pane, 40)
		if updatePrompt.MatchString(screen) {
			return fmt.Errorf("worker %s shows an update prompt; the bench does not answer it (update or skip it yourself first); screen ends:\n%s", wk.Alias, tail(screen, 12))
		}
		if trustPrompt.MatchString(screen) && !answered {
			if wk.Trust == TrustLeave {
				w.logf("%s: trust prompt left for sheepdog", wk.Alias)
				return nil
			}
			keys := trustKeys(wk.Kind, screen)
			w.logf("%s: accepting the trust prompt (%s)", wk.Alias, strings.Join(keys, " "))
			if err := w.h.SendKeys(ctx, wk.pane, keys...); err != nil {
				return err
			}
			answered = true
			time.Sleep(2 * time.Second)
			continue
		}
		st, _ := w.h.AgentStatus(ctx, wk.pane)
		if st == "idle" || st == "done" {
			if wk.Trust == TrustLeave && !answered {
				w.logf("%s: no trust prompt shown (already trusted?)", wk.Alias)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	screen, _ := w.h.Read(ctx, wk.pane, 30)
	return fmt.Errorf("worker %s did not become idle; screen ends:\n%s", wk.Alias, tail(screen, 15))
}

// trustKeys answers a trust prompt with yes. Claude Code's list starts
// on "No, exit"; Codex's on "Yes".
func trustKeys(kind, screen string) []string {
	if kind == KindClaude {
		return []string{"Down", "Enter"}
	}
	_ = screen
	return []string{"Enter"}
}

// step runs one setup step.
func (w *world) step(ctx context.Context, st Step) error {
	wk := w.byName[st.Worker]
	switch {
	case st.Prompt != "":
		w.logf("setup: prompt %s: %s", wk.Alias, oneLine(st.Prompt, 80))
		if err := w.h.Prompt(ctx, wk.pane, st.Prompt); err != nil {
			return err
		}
	case len(st.Keys) > 0:
		if err := w.h.SendKeys(ctx, wk.pane, st.Keys...); err != nil {
			return err
		}
	}
	timeout := time.Duration(orInt(st.TimeoutS, 180)) * time.Second
	if len(st.WaitFor) > 0 || st.WaitScreen != "" {
		if err := w.waitWorker(ctx, wk, st.WaitFor, st.WaitScreen, timeout); err != nil {
			return err
		}
	}
	if st.SleepS > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(st.SleepS) * time.Second):
		}
	}
	return nil
}

func (w *world) waitWorker(ctx context.Context, wk *worker, states []string, screenRe string, timeout time.Duration) error {
	var re *regexp.Regexp
	if screenRe != "" {
		re = regexp.MustCompile("(?i)" + screenRe)
	}
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok := true
		if len(states) > 0 {
			st, _ := w.h.AgentStatus(ctx, wk.pane)
			last = st
			ok = contains(states, st)
		}
		if ok && re != nil {
			screen, _ := w.h.Read(ctx, wk.pane, 60)
			ok = re.MatchString(screen)
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	screen, _ := w.h.Read(ctx, wk.pane, 30)
	return fmt.Errorf("worker %s: wait for %v %q timed out (status %s); screen ends:\n%s", wk.Alias, states, screenRe, last, tail(screen, 15))
}

// isolation proves the daemon sees the bench panes and nothing else:
// its roster against the bench session's panes, and the default
// session's agents (which must not be in it). A failure aborts the run.
func (w *world) isolation(ctx context.Context, sock string) ([]string, error) {
	var rows []AgentRow
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r, _, err := w.d.Agents(ctx)
		if err == nil && len(r) >= len(w.order) {
			rows = r
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	proof := []string{"daemon herdr socket: " + sock}
	panes, err := w.h.Panes(ctx)
	if err != nil {
		return proof, err
	}
	benchPanes := map[string]bool{}
	var bp []string
	for _, p := range panes {
		benchPanes[p.PaneID] = true
		bp = append(bp, p.PaneID+" ("+p.Agent+", "+filepath.Base(p.Cwd)+")")
	}
	proof = append(proof, "bench session panes: "+strings.Join(bp, "; "))
	var rp []string
	labels := map[string]bool{}
	for _, wk := range w.order {
		labels[wk.Workspace] = true
	}
	bad := 0
	for _, r := range rows {
		rp = append(rp, r.PaneID+" "+r.Alias+" ("+r.WorkspaceLabel+")")
		if !benchPanes[r.PaneID] || !labels[r.WorkspaceLabel] {
			bad++
		}
	}
	proof = append(proof, fmt.Sprintf("daemon /api/state agents (%d): %s", len(rows), strings.Join(rp, "; ")))
	if def, err := DefaultAgentPanes(ctx, w.o.HerdrBin); err == nil {
		proof = append(proof, fmt.Sprintf("default session agents (not in the roster): %d", len(def)))
	}
	if bad > 0 || len(rows) != len(w.order) {
		return proof, fmt.Errorf("isolation check failed: the daemon lists %d agents (%d outside the bench session), want exactly the %d bench workers", len(rows), bad, len(w.order))
	}
	w.adoptAliases(rows)
	return proof, nil
}

// adoptAliases switches the run to the daemon's own aliases: the daemon
// numbers same-kind workers of a workspace in the order it discovers
// their panes, which need not be the scenario's order. Expectations
// and the judge's facts then name each worker as sheepdog does.
func (w *world) adoptAliases(rows []AgentRow) {
	rename := map[string]string{}
	w.mu.Lock()
	for _, r := range rows {
		if old, ok := w.aliasOf[r.PaneID]; ok && r.Alias != "" {
			rename[old] = r.Alias
			w.aliasOf[r.PaneID] = r.Alias
		}
	}
	w.mu.Unlock()
	w.renamed = rename
	re := func(a string) string {
		if n, ok := rename[a]; ok {
			return n
		}
		return a
	}
	sc := w.sc
	sc.Expect.Actions = append([]eval.SimAction(nil), sc.Expect.Actions...)
	for i := range sc.Expect.Actions {
		sc.Expect.Actions[i].Agent = re(sc.Expect.Actions[i].Agent)
	}
	sc.Expect.Forbid = append([]eval.SimAction(nil), sc.Expect.Forbid...)
	for i := range sc.Expect.Forbid {
		sc.Expect.Forbid[i].Agent = re(sc.Expect.Forbid[i].Agent)
	}
	sc.Expect.Agent = re(sc.Expect.Agent)
	sc.Workers = append([]Worker(nil), sc.Workers...)
	for i := range sc.Workers {
		sc.Workers[i].Alias = re(sc.Workers[i].Alias)
	}
	w.sim = sc.Sim()
	w.expect = sc.Expect.SimExpect
}

// poll feeds pushes, actions and transitions from the daemon's events.
func (w *world) poll(ctx context.Context) {
	defer close(w.polled)
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		evs, err := w.d.NewEvents(ctx)
		if err == nil {
			for _, e := range evs {
				w.event(e)
			}
		}
		select {
		case <-w.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *world) atMS(ts time.Time) int64 {
	if ts.IsZero() {
		return w.since()
	}
	return ts.Sub(w.start).Milliseconds()
}

// event turns one daemon event into the run's lines.
func (w *world) event(e Event) {
	var p map[string]any
	_ = json.Unmarshal(e.Payload, &p)
	str := func(k string) string { s, _ := p[k].(string); return s }
	w.mu.Lock()
	defer w.mu.Unlock()
	alias := w.aliasOf[e.PaneID]
	if alias == "" && e.PaneID != "" {
		// A pane sheepdog opened during the call (an agent started by
		// voice).
		alias = "new agent in pane " + e.PaneID
	}
	switch e.Type {
	case "voice.push":
		if str("urgency") == "silent" || strings.TrimSpace(str("text")) == "" {
			return
		}
		line := eval.SimLine{AtMS: w.atMS(e.TS), Who: "push", Text: strings.TrimSpace(str("text"))}
		select {
		case w.pushes <- line:
		default:
		}
	case "herdr.send":
		if str("error") != "" {
			w.timeline = append(w.timeline, eval.SimLine{AtMS: w.atMS(e.TS), Who: "worker", Agent: alias, Text: "send to " + alias + " failed: " + str("error")})
		}
		var a eval.SimLine
		switch str("kind") {
		case "prompt":
			a = eval.SimLine{Kind: eval.ActionPrompt, Detail: str("text"), Text: fmt.Sprintf("prompt to %s: %q", alias, str("text"))}
		case "keys":
			var keys []string
			if ks, ok := p["keys"].([]any); ok {
				for _, k := range ks {
					keys = append(keys, fmt.Sprint(k))
				}
			}
			a = eval.SimLine{Kind: eval.ActionKeys, Detail: strings.Join(keys, " "), Text: fmt.Sprintf("keys to %s: %s", alias, strings.Join(keys, " "))}
			if len(keys) == 1 && keys[0] == "ctrl+c" {
				a.Kind, a.Text = eval.ActionInterrupt, "interrupt (ctrl+c) to "+alias
			}
		default:
			return
		}
		a.AtMS, a.Who, a.Agent = w.atMS(e.TS), "action", alias
		w.actions = append(w.actions, a)
	case "herdr.transition":
		if alias == "" {
			return
		}
		w.timeline = append(w.timeline, eval.SimLine{AtMS: w.atMS(e.TS), Who: "worker", Agent: alias, Text: fmt.Sprintf("%s: %s → %s (herdr)", alias, orDefault(str("from"), "new"), str("to"))})
	}
}

func (w *world) drain() []eval.SimLine {
	var out []eval.SimLine
	for {
		select {
		case l := <-w.pushes:
			out = append(out, l)
		default:
			return out
		}
	}
}

// waitPushes waits up to d for a push, then keeps listening settle more
// after each one.
func (w *world) waitPushes(d, settle time.Duration, deadline time.Time) []eval.SimLine {
	if until := time.Until(deadline); d > until {
		d = until
	}
	if d <= 0 {
		return w.drain()
	}
	var out []eval.SimLine
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case l := <-w.pushes:
			out = append(out, l)
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(settle)
		case <-timer.C:
			return out
		}
	}
}

// screens reads every worker's pane.
func (w *world) screens(ctx context.Context, lines int) map[string]string {
	out := map[string]string{}
	for _, wk := range w.order {
		s, err := w.h.Read(ctx, wk.pane, lines)
		if err != nil {
			s = "(unreadable: " + err.Error() + ")"
		}
		out[wk.Alias] = s
	}
	return out
}

// facts is the judge's ground truth: the real workers, their screens at
// the start and the end, what herdr reported and what sheepdog sent.
func (w *world) facts(initial, final map[string]string, roster []AgentRow) string {
	var b strings.Builder
	b.WriteString("These are REAL coding agents in a real terminal multiplexer; the screens are their real panes.\n")
	for _, wk := range w.order {
		fmt.Fprintf(&b, "Worker %s (%s, model %s).", w.daemonAlias(wk.Alias), wk.Kind, wk.Model)
		for _, r := range roster {
			if r.PaneID == wk.pane {
				fmt.Fprintf(&b, " Spoken name %q, session title %q, state now %s.", r.Spoken, r.Title, r.State)
			}
		}
		b.WriteString("\n")
	}
	for _, wk := range w.order {
		fmt.Fprintf(&b, "\nScreen of %s at the start (last lines):\n%s\n", w.daemonAlias(wk.Alias), tail(initial[wk.Alias], 25))
		fmt.Fprintf(&b, "\nScreen of %s at the end (last lines):\n%s\n", w.daemonAlias(wk.Alias), tail(final[wk.Alias], 40))
	}
	for _, r := range roster {
		if _, ours := w.aliasOf[r.PaneID]; ours || r.Alias == "" {
			continue
		}
		fmt.Fprintf(&b, "\nWorker %s (%s) was started by sheepdog during the call (a new agent, not one of the workers above). Spoken name %q, session title %q, state now %s.\n", r.Alias, r.Kind, r.Spoken, r.Title, r.State)
		if s, ok := final[r.Alias]; ok {
			fmt.Fprintf(&b, "Screen of %s at the end (last lines):\n%s\n", r.Alias, tail(s, 40))
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	b.WriteString("\nWhat herdr reported (state changes):\n")
	for _, l := range w.timeline {
		fmt.Fprintf(&b, "[t+%ds] %s\n", l.AtMS/1000, l.Text)
	}
	b.WriteString("\nWhat sheepdog sent to the workers:\n")
	if len(w.actions) == 0 {
		b.WriteString("(nothing)\n")
	}
	for _, a := range w.actions {
		fmt.Fprintf(&b, "[t+%ds] %s\n", a.AtMS/1000, a.Text)
	}
	return b.String()
}

// teardown ends the daemon, the workers and the session and removes the
// scratch directory (unless kept).
func (w *world) teardown() []error {
	var errs []error
	if w.d != nil {
		w.d.Stop()
	}
	if w.dlog != nil {
		_ = w.dlog.Close()
	}
	if w.h != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		for _, wk := range w.order {
			// Two ctrl+c end Claude Code and Codex cleanly, so their
			// session files are complete.
			_ = w.h.SendKeys(ctx, wk.pane, "C-c")
			time.Sleep(300 * time.Millisecond)
			_ = w.h.SendKeys(ctx, wk.pane, "C-c")
		}
		time.Sleep(time.Second)
		for _, id := range w.ws {
			if err := w.h.CloseWorkspace(ctx, id); err != nil {
				errs = append(errs, err)
			}
		}
		cancel()
		if err := w.h.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if !w.o.Keep && w.dir != "" {
		if err := os.RemoveAll(w.dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// checks runs the scenario's code checks after the call.
func (w *world) checks(ctx context.Context, evs []Event, final map[string]string) (lines []string, fails []eval.Failure) {
	for _, c := range w.sc.Expect.Events {
		n := 0
		var re *regexp.Regexp
		if c.Matches != "" {
			re = regexp.MustCompile("(?i)" + c.Matches)
		}
		for _, e := range evs {
			if strings.HasPrefix(e.Type, c.Type) && (re == nil || re.Match(e.Payload)) {
				n++
			}
		}
		ok := n > 0
		if c.Absent {
			ok = n == 0
		}
		desc := fmt.Sprintf("event %s%s: %d seen%s", c.Type, matchNote(c.Matches), n, absentNote(c.Absent))
		lines = append(lines, verdict(ok)+" "+desc+whyNote(c.Why))
		if !ok {
			fails = append(fails, eval.Failure{Type: eval.FailCheck, Detail: desc + whyNote(c.Why)})
		}
	}
	for _, c := range w.sc.Expect.Screens {
		hit := regexp.MustCompile("(?i)" + c.Matches).MatchString(final[c.Worker])
		ok := hit != c.Absent
		desc := fmt.Sprintf("screen %s%s%s", c.Worker, matchNote(c.Matches), absentNote(c.Absent))
		lines = append(lines, verdict(ok)+" "+desc+whyNote(c.Why))
		if !ok {
			fails = append(fails, eval.Failure{Type: eval.FailCheck, Detail: desc + whyNote(c.Why)})
		}
	}
	for _, c := range w.sc.Expect.Commands {
		wk := w.byName[c.Worker]
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		cmd := exec.CommandContext(cctx, c.Argv[0], c.Argv[1:]...)
		cmd.Dir = wk.repo
		out, err := cmd.CombinedOutput()
		cancel()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		ok := code == c.Exit
		desc := fmt.Sprintf("%s in %s: exit %d (want %d)", strings.Join(c.Argv, " "), c.Worker, code, c.Exit)
		lines = append(lines, verdict(ok)+" "+desc+whyNote(c.Why))
		if !ok {
			fails = append(fails, eval.Failure{Type: eval.FailCheck, Detail: desc + ": " + oneLine(tail(string(out), 3), 160)})
		}
	}
	return lines, fails
}

// excerptTypes are the daemon events a write-up quotes.
var excerptTypes = []string{"web.utterance", "voice.tool_result", "voice.push", "herdr.send", "herdr.transition", "pending.", "supervisor.", "investigation.", "promise.", "worker.title", "brain.tool_call", "brain.fallback", "brain.failover"}

func excerpt(evs []Event, start time.Time, aliasOf map[string]string) []string {
	var out []string
	for _, e := range evs {
		keep := false
		for _, t := range excerptTypes {
			if strings.HasPrefix(e.Type, t) {
				keep = true
				break
			}
		}
		if !keep || e.Type == "supervisor.names" {
			continue
		}
		who := aliasOf[e.PaneID]
		if who == "" {
			who = e.PaneID
		}
		out = append(out, fmt.Sprintf("t+%.1fs %s %s %s", e.TS.Sub(start).Seconds(), e.Type, who, oneLine(string(e.Payload), 300)))
	}
	return out
}

func verdict(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func matchNote(m string) string {
	if m == "" {
		return ""
	}
	return " matching /" + m + "/"
}

func absentNote(a bool) string {
	if a {
		return " (must be absent)"
	}
	return ""
}

func whyNote(w string) string {
	if w == "" {
		return ""
	}
	return " — " + w
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orInt(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// tail is the last n non-empty-trailing lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n "), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
