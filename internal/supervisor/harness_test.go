package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

var t0 = time.Date(2026, 9, 17, 16, 55, 0, 0, time.UTC)

// clock is a settable fake time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// recLog records structured events.
type recLog struct {
	mu     sync.Mutex
	events []logging.Line
}

func (l *recLog) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, logging.Line{Source: source, Type: typ, PaneID: paneID, Payload: payload})
}

func (l *recLog) ofType(typ string) []logging.Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logging.Line
	for _, e := range l.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (l *recLog) count(typ string) int { return len(l.ofType(typ)) }

// fakePusher records pushes.
type fakePusher struct {
	mu     sync.Mutex
	pushes []voice.EventPush
	err    error
}

func (p *fakePusher) Push(text string, u voice.Urgency) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.pushes = append(p.pushes, voice.EventPush{Text: text, Urgency: u})
	return nil
}

func (p *fakePusher) all() []voice.EventPush {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]voice.EventPush(nil), p.pushes...)
}

func (p *fakePusher) last(t *testing.T) voice.EventPush {
	t.Helper()
	all := p.all()
	if len(all) == 0 {
		t.Fatal("no push recorded")
	}
	return all[len(all)-1]
}

// fakeLLM is a rule-based stand-in for the templates, keyed on the
// captured screens, so scenarios do not need per-test scripting. Every
// call is counted; failures can be injected (fail for every template,
// failDone for summarize_done only).
type fakeLLM struct {
	mu        sync.Mutex
	calls     map[string]int
	fail      error
	failDone  error
	summarize func(screen, alias string) llm.BlockSummary
	resolve   func(text string, agents []llm.AgentSummary, hint string) llm.Resolution
	done      func(screen, alias string) llm.DoneSummary
}

func newFakeLLM() *fakeLLM {
	return &fakeLLM{calls: map[string]int{}}
}

func (f *fakeLLM) bump(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	return f.fail
}

func (f *fakeLLM) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeLLM) SummarizeBlock(_ context.Context, screen, alias string) (llm.BlockSummary, error) {
	if err := f.bump("summarize_block"); err != nil {
		return llm.BlockSummary{}, err
	}
	if f.summarize != nil {
		return f.summarize(screen, alias), nil
	}
	return summarizeByScreen(screen, alias), nil
}

// summarizeByScreen mimics what the live model returned for the fixtures.
func summarizeByScreen(screen, alias string) llm.BlockSummary {
	switch {
	case strings.Contains(screen, "Would you like to run the following command?"):
		return llm.BlockSummary{
			Spoken: alias + " is asking whether it can run a command to create hello.txt.", Question: "whether it can create hello.txt",
			IsQuestion: true, NeedsDecision: true,
			Options:    []llm.Option{{Label: "Yes, proceed", Key: "enter"}, {Label: "Yes, and don't ask again", Key: "p"}, {Label: "No, and tell Codex what to do differently", Key: "esc"}},
			AnswerKeys: llm.AnswerKeys{Yes: []string{"enter"}, No: []string{"esc"}},
		}
	case strings.Contains(screen, "Do you want to create claude.txt?"):
		return llm.BlockSummary{
			Spoken: alias + " is asking whether it can create claude.txt.", Question: "whether it can create claude.txt",
			IsQuestion: true, NeedsDecision: true,
			Options:    []llm.Option{{Label: "Yes", Key: "1"}, {Label: "Yes, and switch to accept edits", Key: "2"}, {Label: "No", Key: "3"}},
			AnswerKeys: llm.AnswerKeys{Yes: []string{"1"}, No: []string{"3"}},
		}
	case strings.Contains(screen, "Accept this file edit?"):
		// Antigravity CLI 1.2.5 file-edit prompt (WP7b live summary): Esc is
		// disabled there, so "no" must be the digit.
		return llm.BlockSummary{
			Spoken: alias + " is asking whether to accept the edit to the readme file.", Question: "whether to accept the edit to the readme file",
			IsQuestion: true, NeedsDecision: true,
			Options:    []llm.Option{{Label: "Yes, accept this change", Key: "1"}, {Label: "No, reject this change", Key: "2"}},
			AnswerKeys: llm.AnswerKeys{Yes: []string{"1"}, No: []string{"2"}},
		}
	case strings.Contains(screen, "Run this command?"):
		// Antigravity CLI 1.2.5 command-approval prompt (WP7b live summary).
		return llm.BlockSummary{
			Spoken: alias + " is asking for permission to run tests.", Question: "whether it can run tests",
			IsQuestion: true, NeedsDecision: true,
			Options: []llm.Option{{Label: "Yes, run command", Key: "1"}, {Label: "Yes, and always allow in this conversation", Key: "2"},
				{Label: "Yes, and always allow (persist)", Key: "3"}, {Label: "No, cancel", Key: "4"}},
			AnswerKeys: llm.AnswerKeys{Yes: []string{"1"}, No: []string{"4"}},
		}
	case strings.Contains(screen, "Do you trust the contents of this directory?"):
		return llm.BlockSummary{Spoken: alias + " is showing its startup trust dialog."}
	case strings.Contains(screen, "Conversation interrupted"):
		return llm.BlockSummary{Spoken: alias + " was interrupted and is idle at its prompt."}
	case strings.Contains(screen, "Update available!"):
		return llm.BlockSummary{Spoken: alias + " is idle at its prompt with an update notice."}
	default:
		return llm.BlockSummary{Spoken: alias + " is idle."}
	}
}

func (f *fakeLLM) ResolveInstruction(_ context.Context, text string, agents []llm.AgentSummary, hint string) (llm.Resolution, error) {
	if err := f.bump("resolve_instruction"); err != nil {
		return llm.Resolution{}, err
	}
	if f.resolve != nil {
		return f.resolve(text, agents, hint), nil
	}
	return resolveByRules(text, agents, hint), nil
}

// resolveByRules: an alias in the hint or the text picks that agent; a
// plain yes/no picks the single blocked agent; "it" with several agents is
// ambiguous.
func resolveByRules(text string, agents []llm.AgentSummary, hint string) llm.Resolution {
	lower := strings.ToLower(text)
	var target *llm.AgentSummary
	for i := range agents {
		a := &agents[i]
		if hint == a.Alias || strings.Contains(lower, a.Alias) {
			target = a
			break
		}
	}
	isAnswer := strings.HasPrefix(lower, "yes") || strings.HasPrefix(lower, "no") || strings.HasPrefix(lower, "option")
	if target == nil && isAnswer {
		var blocked []*llm.AgentSummary
		for i := range agents {
			if agents[i].State == "blocked" {
				blocked = append(blocked, &agents[i])
			}
		}
		if len(blocked) == 1 {
			target = blocked[0]
		}
	}
	if target == nil && len(agents) == 1 {
		target = &agents[0]
	}
	if target == nil {
		names := make([]string, 0, len(agents))
		for _, a := range agents {
			names = append(names, a.Alias)
		}
		return llm.Resolution{Ambiguous: true, ClarifyQuestion: "Do you mean " + strings.Join(names, " or ") + "?"}
	}
	instr := lower
	for _, prefix := range []string{"tell the " + target.Alias + " one to ", "tell " + target.Alias + " to ", "tell it to "} {
		instr = strings.TrimPrefix(instr, prefix)
	}
	if isAnswer {
		word := strings.Fields(lower)[0]
		if strings.HasPrefix(lower, "option") {
			word = strings.Join(strings.Fields(lower)[:2], " ")
		}
		return llm.Resolution{PaneID: target.PaneID, InstructionText: word, IsAnswer: true, Answer: word, Echo: "Telling " + target.Alias + " " + word + "."}
	}
	return llm.Resolution{PaneID: target.PaneID, InstructionText: instr, Echo: "Telling " + target.Alias + " to " + instr + "."}
}

func (f *fakeLLM) SummarizeDone(_ context.Context, screen, alias string) (llm.DoneSummary, error) {
	if err := f.bump("summarize_done"); err != nil {
		return llm.DoneSummary{}, err
	}
	f.mu.Lock()
	failDone, done := f.failDone, f.done
	f.mu.Unlock()
	if failDone != nil {
		return llm.DoneSummary{}, failDone
	}
	if done != nil {
		return done(screen, alias), nil
	}
	return doneByScreen(screen), nil
}

// doneByScreen mimics what the live model returned for the "after"
// fixtures (WP7b screens).
func doneByScreen(screen string) llm.DoneSummary {
	switch {
	case strings.Contains(screen, "You canceled the request"):
		return llm.DoneSummary{Spoken: "stopped after you declined the append to hello.txt", Outcome: "failed"}
	case strings.Contains(screen, "You approved codex to run"):
		return llm.DoneSummary{Spoken: "created hello.txt with the requested line", Outcome: "success"}
	case strings.Contains(screen, "Wrote 1 line to claude.txt"):
		return llm.DoneSummary{Spoken: "created claude.txt with the line hello from claude", Outcome: "success"}
	case strings.Contains(screen, "Conversation interrupted"):
		return llm.DoneSummary{Spoken: "was interrupted before the sleep finished", Outcome: "failed"}
	case strings.Contains(screen, "Run this command?"), strings.Contains(screen, "Accept this file edit?"):
		return llm.DoneSummary{Spoken: "stopped to ask for permission", Outcome: "partial", AskedQuestion: true}
	default:
		return llm.DoneSummary{Spoken: "did something the screen does not show", Outcome: "unknown"}
	}
}

func (f *fakeLLM) SpokenAnswer(_ context.Context, raw, question, alias string) (llm.Answer, error) {
	if err := f.bump("spoken_answer"); err != nil {
		return llm.Answer{}, err
	}
	if strings.Contains(raw, "sleep 90") {
		return llm.Answer{Spoken: alias + " is running a sleep command and waiting for it.", CanAnswer: true}, nil
	}
	if strings.HasPrefix(raw, "QUERY:") {
		return llm.Answer{Spoken: "The deep tool says: " + strings.TrimPrefix(raw, "QUERY:") + ".", CanAnswer: true}, nil
	}
	return llm.Answer{Spoken: "The screen does not say.", CanAnswer: false}, nil
}

// fakeQuerier is a WP4 stand-in.
type fakeQuerier struct {
	mu    sync.Mutex
	calls []string
	dirs  []string
	err   error
}

func (q *fakeQuerier) Query(_ context.Context, workspace, question string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls = append(q.calls, question)
	q.dirs = append(q.dirs, workspace)
	if q.err != nil {
		return "", q.err
	}
	return "QUERY:" + question, nil
}

// harness bundles everything a scenario needs.
type harness struct {
	t      *testing.T
	srv    *herdrtest.Server
	client *herdr.Client
	store  *state.Store
	llm    *fakeLLM
	pusher *fakePusher
	log    *recLog
	clock  *clock
	sup    *Supervisor
	dir    string
}

func newHarness(t *testing.T, opts ...func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, srv: herdrtest.New(t), llm: newFakeLLM(), pusher: &fakePusher{}, log: &recLog{}, clock: &clock{now: t0}, dir: t.TempDir()}
	h.client = herdr.NewClient(h.srv.Path)
	h.client.Log = h.log
	var err error
	h.store, err = state.Open(filepath.Join(h.dir, "state.json"), state.Options{Aliases: map[string]string{"w5:p1": "auth", "w6:p1": "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{PendingTTL: 120 * time.Second, Now: h.clock.Now, DataDir: h.dir}
	for _, o := range opts {
		o(&cfg)
	}
	h.sup, err = New(h.store, h.client, h.llm, nil, h.pusher, h.log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.sup.SetVoiceConnected(true)
	return h
}

// addAgent registers a pane in the fake herdr and in the state table.
func (h *harness) addAgent(pane, workspace, kind string, st herdr.State) {
	h.t.Helper()
	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: workspace, Label: workspace})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: pane, WorkspaceID: workspace, TabID: workspace + ":t1", Agent: kind, Status: st, Cwd: "/work/" + pane})
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: pane, To: st, At: h.clock.Now(), Agent: kind, WorkspaceID: workspace, WorkspaceLabel: workspace})
}

// transition moves a pane to st: the fake server table and the supervisor.
func (h *harness) transition(pane string, from, to herdr.State) {
	h.t.Helper()
	h.srv.SetStatus(pane, to)
	row, _ := h.store.Get(pane)
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: pane, From: from, To: to, At: h.clock.Now(), Agent: row.Kind, WorkspaceID: row.Workspace, WorkspaceLabel: row.WorkspaceLabel})
}

// block puts the captured screen on the pane and transitions it to blocked.
func (h *harness) block(pane, screenFile string) {
	h.t.Helper()
	h.srv.SetScreen(pane, fixture(h.t, screenFile))
	row, _ := h.store.Get(pane)
	h.transition(pane, row.State, herdr.StateBlocked)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sendKeysRequests returns (pane, keys) for every pane.send_keys request.
func (h *harness) sendKeysRequests() [][2]string {
	var out [][2]string
	for _, r := range h.srv.RequestsFor("pane.send_keys") {
		var p struct {
			PaneID string   `json:"pane_id"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(r.Params, &p)
		out = append(out, [2]string{p.PaneID, strings.Join(p.Keys, " ")})
	}
	return out
}

// promptRequests returns (target, text) for every agent.prompt request.
func (h *harness) promptRequests() [][2]string {
	var out [][2]string
	for _, r := range h.srv.RequestsFor("agent.prompt") {
		var p struct {
			Target string `json:"target"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(r.Params, &p)
		out = append(out, [2]string{p.Target, p.Text})
	}
	return out
}

func (h *harness) pending() (state.PendingAction, bool) { return h.store.Pending() }

func (h *harness) call(name string, args map[string]any) voice.ToolResult {
	h.t.Helper()
	res, err := h.sup.Handler()(context.Background(), voice.ToolCall{ID: "c", Name: name, Args: args})
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	return res
}

var errBoom = errors.New("boom")
