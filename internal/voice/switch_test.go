package voice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSess struct {
	name     string
	audio    chan []byte
	events   chan Event
	mu       sync.Mutex
	sent     [][]byte
	muted    bool
	client   bool
	closed   bool
	billed   float64
	startErr error
	reports  []string
	speaking []bool
}

func newFake(name string) *fakeSess {
	return &fakeSess{name: name, audio: make(chan []byte), events: make(chan Event, 16)}
}

func (f *fakeSess) Start(context.Context) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.events <- EventStatus{Status: StatusConnected, Reason: f.name}
	return nil
}
func (f *fakeSess) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		f.events <- EventStatus{Status: StatusClosed, Reason: "shutdown"}
		close(f.events)
		close(f.audio)
	}
	return nil
}
func (f *fakeSess) SendAudio(b []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, b)
	f.mu.Unlock()
	return nil
}
func (f *fakeSess) SendText(string) error      { return nil }
func (f *fakeSess) Push(string, Urgency) error { return nil }
func (f *fakeSess) Speaking() bool             { return false }
func (f *fakeSess) Audio() <-chan []byte       { return f.audio }
func (f *fakeSess) Events() <-chan Event       { return f.events }
func (f *fakeSess) SetMuted(m bool)            { f.mu.Lock(); f.muted = m; f.mu.Unlock() }
func (f *fakeSess) ClientAttached(a bool)      { f.mu.Lock(); f.client = a; f.mu.Unlock() }
func (f *fakeSess) BilledSeconds() float64     { return f.billed }
func (f *fakeSess) ClientPlayback(state string, _ int) {
	f.mu.Lock()
	f.reports = append(f.reports, state)
	f.mu.Unlock()
}
func (f *fakeSess) ClientSpeaking(on bool) {
	f.mu.Lock()
	f.speaking = append(f.speaking, on)
	f.mu.Unlock()
}
func (f *fakeSess) LastPlayback() PlaybackReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reports) == 0 {
		return PlaybackReport{}
	}
	return PlaybackReport{State: f.reports[len(f.reports)-1]}
}

func TestSwitcher(t *testing.T) {
	g, o := newFake("gemini"), newFake("openai")
	o.billed = 12
	sw, err := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return g, nil }},
		{ProviderInfo: ProviderInfo{ID: "openai", Available: true, Billing: BillingPerMinute}, New: func() (Session, error) { return o, nil }},
		{ProviderInfo: ProviderInfo{ID: "other", Available: false, Reason: "no key"}},
	}, "gemini", nil)
	if err != nil {
		t.Fatal(err)
	}
	g.client = true // Start must tell the first session the truth: no client yet
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.client {
		t.Fatal("Start did not tell the first session no client is attached")
	}
	if ev := <-sw.Events(); ev.(EventStatus).Reason != "gemini" {
		t.Fatalf("first event %#v", ev)
	}
	sw.SetMuted(true)
	sw.ClientAttached(true)
	_ = sw.SendAudio([]byte{1, 2})
	if err := sw.SetProvider("other", "test"); !errors.Is(err, ErrUnavailableProvider) {
		t.Fatalf("unavailable: %v", err)
	}
	if err := sw.SetProvider("nope", "test"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown: %v", err)
	}
	if err := sw.SetProvider("openai", "test"); err != nil {
		t.Fatal(err)
	}
	if sw.Provider() != "openai" || sw.Current() != o {
		t.Fatal("not switched")
	}
	if !o.muted || !o.client {
		t.Fatal("client state not replayed onto the new session")
	}
	_ = sw.SendAudio([]byte{3, 4})
	if len(g.sent) != 1 || len(o.sent) != 1 {
		t.Fatalf("audio routing g=%d o=%d", len(g.sent), len(o.sent))
	}
	// The new session's status arrives; the retired one's "closed" does not.
	deadline := time.After(time.Second)
	for sawOpenAI := false; !sawOpenAI; {
		select {
		case ev := <-sw.Events():
			if st, ok := ev.(EventStatus); ok {
				if st.Status == StatusClosed {
					t.Fatal("retired session's closed status forwarded")
				}
				sawOpenAI = st.Reason == "openai"
			}
		case <-deadline:
			t.Fatal("no status from the new session")
		}
	}
	// Back to a fresh Gemini session: the retired GPT-Live session's
	// billed seconds stay in the tally.
	g = newFake("gemini")
	if err := sw.SetProvider("gemini", "test"); err != nil {
		t.Fatal(err)
	}
	waitClosed := time.Now().Add(time.Second)
	for sw.BilledSeconds() != 12 && time.Now().Before(waitClosed) {
		time.Sleep(5 * time.Millisecond)
	}
	if sw.BilledSeconds() != 12 {
		t.Fatalf("billed %v, want the retired GPT-Live session's 12", sw.BilledSeconds())
	}
}

func TestSwitcherStartFailureKeepsOld(t *testing.T) {
	g, o := newFake("gemini"), newFake("openai")
	o.startErr = errors.New("boom")
	sw, _ := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return g, nil }},
		{ProviderInfo: ProviderInfo{ID: "openai", Available: true}, New: func() (Session, error) { return o, nil }},
	}, "gemini", nil)
	_ = sw.Start(context.Background())
	if err := sw.SetProvider("openai", "test"); err == nil {
		t.Fatal("want error")
	}
	if sw.Provider() != "gemini" || sw.Current() != g {
		t.Fatal("old session not kept")
	}
	_ = sw.Close()
}

func TestSwitcherSetBeforeStart(t *testing.T) {
	g, o := newFake("gemini"), newFake("openai")
	sw, _ := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return g, nil }},
		{ProviderInfo: ProviderInfo{ID: "openai", Available: true}, New: func() (Session, error) { return o, nil }},
	}, "gemini", nil)
	if err := sw.SetProvider("openai", "state"); err != nil {
		t.Fatal(err)
	}
	_ = sw.Start(context.Background())
	if sw.Current() != o {
		t.Fatal("did not start on the restored provider")
	}
	_ = sw.Close()
	for range sw.Events() {
	}
}

// TestSwitcherForwardsClientPlayback: playback reports and the client's
// speech detector reach the current provider's session.
func TestSwitcherForwardsClientPlayback(t *testing.T) {
	g := newFake("gemini")
	sw, err := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return g, nil }},
	}, "gemini", nil)
	if err != nil {
		t.Fatal(err)
	}
	sw.ClientPlayback(PlaybackStarted, 0) // before Start: no session, no panic
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sw.Close() })
	sw.ClientPlayback(PlaybackDrained, 1200)
	sw.ClientSpeaking(true)
	g.mu.Lock()
	reports, speaking := g.reports, g.speaking
	g.mu.Unlock()
	if len(reports) != 1 || reports[0] != PlaybackDrained || len(speaking) != 1 || !speaking[0] {
		t.Fatalf("forwarded reports %v speaking %v", reports, speaking)
	}
	if got := sw.LastPlayback(); got.State != PlaybackDrained {
		t.Fatalf("LastPlayback %+v", got)
	}
}

// pushFake is a fakeSess with a push queue: DrainPushes hands it over,
// PushTagged records what arrives.
type pushFake struct {
	*fakeSess
	pmu    sync.Mutex
	queued []QueuedPush
	got    []QueuedPush
	refuse bool
}

func (f *pushFake) DrainPushes() []QueuedPush {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	q := f.queued
	f.queued = nil
	return q
}

func (f *pushFake) PushTagged(text string, u Urgency, tags map[string]any) error {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	if f.refuse {
		return errors.New("fake: closed")
	}
	f.got = append(f.got, QueuedPush{Text: text, Urgency: u, Tags: tags})
	return nil
}

type lineLog struct {
	mu    sync.Mutex
	lines []map[string]any
	types []string
}

func (l *lineLog) Log(_, typ, _ string, payload any) {
	p, _ := payload.(map[string]any)
	l.mu.Lock()
	l.types = append(l.types, typ)
	l.lines = append(l.lines, p)
	l.mu.Unlock()
}

func (l *lineLog) of(typ string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for i, t := range l.types {
		if t == typ {
			out = append(out, l.lines[i])
		}
	}
	return out
}

// TestProviderSwitchCarriesQueuedPushes: docs/timelines.md C15 — the
// pushes queued in the old session (a held answer, a silent line) move
// to the new one, in order and with their tags, instead of being closed
// with it without an event; one the new session refuses is logged
// voice.push_dropped {reason: provider_switch}.
func TestProviderSwitchCarriesQueuedPushes(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		g := &pushFake{fakeSess: newFake("gemini")}
		o := &pushFake{fakeSess: newFake("openai"), refuse: refuse}
		log := &lineLog{}
		sw, err := NewSwitcher([]Provider{
			{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return g, nil }},
			{ProviderInfo: ProviderInfo{ID: "openai", Available: true}, New: func() (Session, error) { return o, nil }},
		}, "gemini", log)
		if err != nil {
			t.Fatal(err)
		}
		if err := sw.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		g.queued = []QueuedPush{
			{Text: "Auth has two failing tests.", Urgency: UrgencyIdle, Tags: map[string]any{"answers_utterance": "utt-1", "push_id": "push-1"}},
			{Text: "docs is idle", Urgency: UrgencySilent},
		}
		if err := sw.SetProvider("openai", "test"); err != nil {
			t.Fatal(err)
		}
		if refuse {
			d := log.of("voice.push_dropped")
			if len(d) != 2 || d[0]["reason"] != "provider_switch" || d[0]["push_id"] != "push-1" || d[0]["to"] != "openai" {
				t.Errorf("refused: voice.push_dropped = %v", d)
			}
			if c := log.of("voice.push_carried"); len(c) != 1 || c[0]["count"] != 0 {
				t.Errorf("refused: voice.push_carried = %v", c)
			}
		} else {
			if len(o.got) != 2 || o.got[0].Text != "Auth has two failing tests." || o.got[0].Tags["answers_utterance"] != "utt-1" || o.got[1].Urgency != UrgencySilent {
				t.Errorf("carried = %+v", o.got)
			}
			if c := log.of("voice.push_carried"); len(c) != 1 || c[0]["count"] != 2 || c[0]["from"] != "gemini" {
				t.Errorf("voice.push_carried = %v", c)
			}
			if len(log.of("voice.push_dropped")) != 0 {
				t.Error("a carried push was logged dropped")
			}
		}
		_ = sw.Close()
	}
}

// recapFake is a fakeSess with a recap: RecapEntries hands it over,
// SeedRecap records what arrives, and both note their order against
// pushes.
type recapFake struct {
	*pushFake
	entries []RecapEntry
	seeded  []RecapEntry
	order   *[]string
	focus   string
}

func (f *recapFake) RecapEntries() []RecapEntry { return f.entries }
func (f *recapFake) RecapFocus() string         { return f.focus }
func (f *recapFake) SeedFocus(text string) {
	f.focus = text
	*f.order = append(*f.order, "focus")
}
func (f *recapFake) SeedRecap(e []RecapEntry) {
	f.seeded = e
	*f.order = append(*f.order, "recap")
}
func (f *recapFake) PushTagged(text string, u Urgency, tags map[string]any) error {
	*f.order = append(*f.order, "push")
	return f.pushFake.PushTagged(text, u, tags)
}

// TestProviderSwitchCarriesTheRecap is the Pipecat-comparison item
// "carry context across a voice provider switch": the new provider used
// to start with none of the conversation. It now gets the old session's
// filtered recap, before the carried pushes, both ways.
func TestProviderSwitchCarriesTheRecap(t *testing.T) {
	var order []string
	recap := []RecapEntry{{Role: RecapUser, Text: "why did auth fail?"}, {Role: RecapAssistant, Text: "Two tests time out."}}
	g := &recapFake{pushFake: &pushFake{fakeSess: newFake("gemini")}, entries: recap, order: &order}
	o := &recapFake{pushFake: &pushFake{fakeSess: newFake("openai")}, order: &order}
	g2 := &recapFake{pushFake: &pushFake{fakeSess: newFake("gemini")}, order: &order}
	log := &lineLog{}
	gs := []Session{g, g2}
	sw, err := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { s := gs[0]; gs = gs[1:]; return s, nil }},
		{ProviderInfo: ProviderInfo{ID: "openai", Available: true}, New: func() (Session, error) { return o, nil }},
	}, "gemini", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sw.Close() }()
	g.queued = []QueuedPush{{Text: "docs is idle", Urgency: UrgencySilent}}
	if err := sw.SetProvider("openai", "test"); err != nil {
		t.Fatal(err)
	}
	if len(o.seeded) != 2 || o.seeded[1].Text != "Two tests time out." {
		t.Fatalf("seeded = %+v", o.seeded)
	}
	if strings.Join(order, ",") != "recap,push" {
		t.Errorf("order = %v, want the recap before the carried pushes", order)
	}
	o.entries = append(o.seeded, RecapEntry{Role: RecapUser, Text: "and docs?"})
	if err := sw.SetProvider("gemini", "test"); err != nil {
		t.Fatal(err)
	}
	if len(g2.seeded) != 3 || g2.seeded[2].Text != "and docs?" {
		t.Errorf("back to gemini: seeded = %+v", g2.seeded)
	}
	if c := log.of("voice.recap_carried"); len(c) != 2 || c[0]["items"] != 2 || c[0]["to"] != "openai" || c[1]["to"] != "gemini" {
		t.Errorf("voice.recap_carried = %v", c)
	}
}

// TestProviderSwitchCarriesTheFocus: the supervisor's current-focus
// line goes to the new provider ahead of the recap (a Gemini session
// sends the recap, focus last, at once).
func TestProviderSwitchCarriesTheFocus(t *testing.T) {
	var order []string
	g := &recapFake{pushFake: &pushFake{fakeSess: newFake("gemini")}, entries: []RecapEntry{{Role: RecapUser, Text: "how is auth?"}}, order: &order, focus: "Agent in focus: auth."}
	o := &recapFake{pushFake: &pushFake{fakeSess: newFake("openai")}, order: &order}
	sw, err := NewSwitcher([]Provider{
		{ProviderInfo: ProviderInfo{ID: "gemini", Available: true}, New: func() (Session, error) { return g, nil }},
		{ProviderInfo: ProviderInfo{ID: "openai", Available: true}, New: func() (Session, error) { return o, nil }},
	}, "gemini", &lineLog{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sw.Close() }()
	if err := sw.SetProvider("openai", "test"); err != nil {
		t.Fatal(err)
	}
	if o.focus != "Agent in focus: auth." || strings.Join(order, ",") != "focus,recap" {
		t.Errorf("focus = %q, order = %v", o.focus, order)
	}
}
