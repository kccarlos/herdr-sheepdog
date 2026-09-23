package voice

import (
	"context"
	"errors"
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
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
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
