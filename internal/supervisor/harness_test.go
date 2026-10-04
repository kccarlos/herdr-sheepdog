package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
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

// fakePusher records pushes and implements voiceResetter (reset.go) so
// tests can see a "New conversation" reach the voice side.
type fakePusher struct {
	mu     sync.Mutex
	pushes []voice.EventPush
	tags   []map[string]any
	err    error
	resets []string
	// playback is the client's last playback report (LastPlayback);
	// onset when the client's speech detector last opened.
	playback voice.PlaybackReport
	onset    time.Time
}

// LastSpeechOnset implements the client speech onset the echo guard
// reads (utterance.go).
func (p *fakePusher) LastSpeechOnset() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.onset, !p.onset.IsZero()
}

func (p *fakePusher) setOnset(t time.Time) {
	p.mu.Lock()
	p.onset = t
	p.mu.Unlock()
}

// LastPlayback implements the playback reports the echo guard anchors
// its window to (utterance.go).
func (p *fakePusher) LastPlayback() voice.PlaybackReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playback
}

func (p *fakePusher) setPlayback(r voice.PlaybackReport) {
	p.mu.Lock()
	p.playback = r
	p.mu.Unlock()
}

// Reset implements voiceResetter.
func (p *fakePusher) Reset(reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resets = append(p.resets, reason)
	return nil
}

func (p *fakePusher) allResets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.resets...)
}

func (p *fakePusher) Push(text string, u voice.Urgency) error {
	return p.PushTagged(text, u, nil)
}

// PushTagged implements TagPusher: the tags of each push are kept
// alongside it (answers_job).
func (p *fakePusher) PushTagged(text string, u voice.Urgency, tags map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.pushes = append(p.pushes, voice.EventPush{Text: text, Urgency: u})
	p.tags = append(p.tags, tags)
	return nil
}

// tagsOf returns the tags of the i-th push.
func (p *fakePusher) tagsOf(i int) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.tags) {
		return nil
	}
	return p.tags[i]
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
	pusher *fakePusher
	log    *recLog
	clock  *clock
	sup    *Supervisor
	dir    string
}

func newHarness(t *testing.T, opts ...func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, srv: herdrtest.New(t), pusher: &fakePusher{}, log: &recLog{}, clock: &clock{now: t0}, dir: t.TempDir()}
	h.client = herdr.NewClient(h.srv.Path)
	h.client.Log = h.log
	var err error
	h.store, err = state.Open(filepath.Join(h.dir, "state.json"), state.Options{Aliases: map[string]string{"w5:p1": "auth", "w6:p1": "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	// inputGrace off: a relay with no shepherd input is refused at once
	// rather than after waiting for a transcript that will not come.
	// relayAfter off: a heard utterance nobody relays stays unrelayed
	// unless a test turns the daemon's relay on (relay_test.go).
	// bareAnswers on: most tests answer a prompt with a bare "yes" as a
	// shortcut; the daemon itself never does (dual-brain ablation G4).
	cfg := Config{PendingTTL: 120 * time.Second, Now: h.clock.Now, DataDir: h.dir, inputGrace: -1, relayAfter: -1, interruptCheck: 40 * time.Millisecond, bareAnswers: true}
	for _, o := range opts {
		o(&cfg)
	}
	h.sup, err = New(h.store, h.client, nil, h.pusher, h.log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.sup.SetVoiceConnected(true)
	// The daemon's shutdown order: the supervisor's background turns end
	// before the brain closes (withBrain's cleanup, registered earlier,
	// runs later).
	t.Cleanup(h.sup.shutdown)
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

// call runs one voice tool (to_supervisor or stop) through Handler, as
// the voice session does.
func (h *harness) call(name string, args map[string]any) voice.ToolResult {
	h.t.Helper()
	res, err := h.sup.Handler()(context.Background(), voice.ToolCall{ID: "c", Name: name, Args: args})
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	return res
}

// roster is the roster sentence every request carries and a status
// question with no brain is answered with (composeBriefing, served from
// the cache).
func (h *harness) roster() string {
	text, _ := h.sup.briefingText()
	return text
}

// send is what the brain's prompt tool does: an instruction typed into
// the pane, with the send_instruction pending action around it (the
// template path used to reach this through propose).
func (h *harness) send(pane, text string) voice.ToolResult {
	h.t.Helper()
	row, ok := h.store.Get(pane)
	if !ok {
		h.t.Fatalf("no agent %s", pane)
	}
	res, _ := h.sup.sendInstruction(context.Background(), row, text, "Telling "+row.Alias+" to "+text+".")
	return res
}

// say is the shepherd saying text: the voice's input transcription of
// it, then the voice model relaying it verbatim through to_supervisor
// (fast paths included).
func (h *harness) say(text string) voice.ToolResult {
	h.t.Helper()
	h.hear(text)
	return h.relay(text)
}

// hear is the voice's input transcription of the shepherd saying text,
// with nothing relayed yet.
func (h *harness) hear(text string) {
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: text, Final: true})
}

// relay is the voice model calling to_supervisor with text, whether or
// not the shepherd said anything.
func (h *harness) relay(text string) voice.ToolResult {
	h.t.Helper()
	return h.call(ToolToSupervisor, map[string]any{"text": text})
}

// payload returns the event's payload map.
func payload(e logging.Line) map[string]any {
	m, _ := e.Payload.(map[string]any)
	return m
}

// noFreshWords reports whether res is the silent fact a to_supervisor
// call with no new CEO words gets (no brain turn ran).
func noFreshWords(res voice.ToolResult) bool {
	return res.Scheduling == voice.UrgencySilent && strings.HasPrefix(res.Spoken, "Nothing was handed on")
}
