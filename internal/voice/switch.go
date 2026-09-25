package voice

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Billing modes a provider reports (GET /api/voice/providers).
const (
	// BillingPerMinute: the provider bills session duration, idle or not.
	BillingPerMinute = "per_minute"
)

// ProviderInfo describes one voice provider for the API and the /debug
// picker. Unavailable providers are listed with the reason.
type ProviderInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Model     string `json:"model"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Billing   string `json:"billing,omitempty"`
}

// Provider is a ProviderInfo plus the constructor of its Session (nil
// when unavailable).
type Provider struct {
	ProviderInfo
	New func() (Session, error)
}

// ErrUnknownProvider and ErrUnavailableProvider are SetProvider's
// refusals.
var (
	ErrUnknownProvider     = errors.New("voice: unknown provider")
	ErrUnavailableProvider = errors.New("voice: provider unavailable")
)

// Switcher is the daemon's one voice.Session: it runs the current
// provider's session behind stable Audio and Events channels, so the
// provider can change at runtime (PUT /api/voice/provider) without the
// consumers noticing. The client's mute and attach state is replayed
// onto a new provider's session.
type Switcher struct {
	log       logging.Logger
	providers []Provider
	// ctxlog, when set, gets a marker at every provider switch (the
	// /debug Context tab).
	ctxlog *ctxcap.VoiceLog

	audio  chan []byte
	events chan Event

	mu        sync.RWMutex
	ctx       context.Context
	started   bool
	closed    bool
	switching bool
	current   string
	inner     Session
	muted     bool
	client    bool
	// billedRetired is the billed duration of retired sessions (a
	// GPT-Live session switched away from).
	billedRetired float64
	fwd           sync.WaitGroup
	retire        sync.WaitGroup
}

// NewSwitcher builds a Switcher starting on initial, which must be
// available.
func NewSwitcher(providers []Provider, initial string, log logging.Logger) (*Switcher, error) {
	if log == nil {
		log = logging.NopLogger{}
	}
	sw := &Switcher{log: log, providers: providers, audio: make(chan []byte), events: make(chan Event, 1024)}
	if _, err := sw.lookup(initial); err != nil {
		return nil, err
	}
	sw.current = initial
	return sw, nil
}

func (sw *Switcher) lookup(id string) (Provider, error) {
	for _, p := range sw.providers {
		if p.ID == id {
			if !p.Available || p.New == nil {
				reason := p.Reason
				if reason == "" {
					reason = "not configured"
				}
				return p, fmt.Errorf("%w: %s (%s)", ErrUnavailableProvider, id, reason)
			}
			return p, nil
		}
	}
	return Provider{}, fmt.Errorf("%w: %q", ErrUnknownProvider, id)
}

// SetContextLog sets the voice context log the switch is marked in.
// Call before Start.
func (sw *Switcher) SetContextLog(l *ctxcap.VoiceLog) { sw.ctxlog = l }

// Providers lists every provider, available or not.
func (sw *Switcher) Providers() []ProviderInfo {
	out := make([]ProviderInfo, len(sw.providers))
	for i, p := range sw.providers {
		out[i] = p.ProviderInfo
	}
	return out
}

// Provider is the current provider's id.
func (sw *Switcher) Provider() string {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return sw.current
}

// Model is the current provider's model.
func (sw *Switcher) Model() string {
	cur := sw.Provider()
	for _, p := range sw.providers {
		if p.ID == cur {
			return p.Model
		}
	}
	return ""
}

// Start implements Session: it starts the current provider's session.
func (sw *Switcher) Start(ctx context.Context) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.started {
		return errors.New("voice: Start called twice")
	}
	p, err := sw.lookup(sw.current)
	if err != nil {
		return err
	}
	inner, err := p.New()
	if err != nil {
		return err
	}
	if err := inner.Start(ctx); err != nil {
		return err
	}
	sw.ctx, sw.started, sw.inner = ctx, true, inner
	// Tell the first session whether a /ws client is attached — at
	// daemon start usually none — exactly as a provider switch does, so
	// a push made before the page connects is held for it instead of
	// spoken to nobody (Gemini's hold is off until told).
	if ca, ok := inner.(interface{ ClientAttached(bool) }); ok {
		ca.ClientAttached(sw.client)
	}
	sw.forwardLocked(inner)
	return nil
}

// SetProvider switches to provider id. Before Start it only changes the
// provider Start begins with (the state file's session.voice_provider).
// After Start the new session is started before the old one is closed
// (in the background, so a graceful close does not hold the caller), the
// client state is replayed onto it, and the old session's final
// "closed" status is not forwarded. Logs voice.provider_changed.
func (sw *Switcher) SetProvider(id, via string) error {
	sw.mu.Lock()
	if sw.closed {
		sw.mu.Unlock()
		return errors.New("voice: switcher closed")
	}
	if sw.switching {
		sw.mu.Unlock()
		return errors.New("voice: a provider switch is already in progress")
	}
	p, err := sw.lookup(id)
	if err != nil {
		sw.mu.Unlock()
		return err
	}
	from := sw.current
	if id == from {
		sw.mu.Unlock()
		return nil
	}
	if !sw.started {
		sw.current = id
		sw.mu.Unlock()
		sw.log.Log("voice", "voice.provider_changed", "", map[string]any{"from": from, "to": id, "via": via})
		return nil
	}
	sw.switching = true
	ctx := sw.ctx
	sw.mu.Unlock()

	// Built and started outside the lock: a Gemini Start dials.
	inner, err := p.New()
	if err == nil {
		err = inner.Start(ctx)
	}
	sw.mu.Lock()
	sw.switching = false
	if err != nil {
		sw.mu.Unlock()
		return fmt.Errorf("voice: start %s: %w", id, err)
	}
	if sw.closed {
		sw.mu.Unlock()
		_ = inner.Close()
		return errors.New("voice: switcher closed")
	}
	old := sw.inner
	sw.current, sw.inner = id, inner
	muted, client := sw.muted, sw.client
	sw.forwardLocked(inner)
	sw.retire.Add(1)
	sw.mu.Unlock()

	inner.SetMuted(muted)
	if ca, ok := inner.(interface{ ClientAttached(bool) }); ok {
		ca.ClientAttached(client)
	}
	sw.log.Log("voice", "voice.provider_changed", "", map[string]any{"from": from, "to": id, "via": via})
	sw.carryPushes(old, inner, from, id)
	sw.ctxlog.Marker("provider switch", fmt.Sprintf("Voice provider switched from %s to %s (via %s): the new provider holds none of the old one's context.", from, id, via), false)
	sw.emit(EventInterrupted{}) // drop the old voice's audio (its queued pushes were carried)
	go func() {
		defer sw.retire.Done()
		_ = old.Close()
		if b, ok := old.(interface{ BilledSeconds() float64 }); ok {
			sw.mu.Lock()
			sw.billedRetired += b.BilledSeconds()
			sw.mu.Unlock()
		}
	}()
	return nil
}

// carryPushes moves the pushes still queued in the retired session onto
// the new one, in order, so a provider switch does not discard lines
// nobody has heard yet (docs/timelines.md C15: the old session's queue
// used to be closed with it, with no event). Each goes through the new
// session's own queue and rules (hold, urgency, arbitration); its queue
// age restarts. Logs voice.push_carried {count}; a line the new session
// refuses is logged voice.push_dropped {reason: provider_switch}. A
// session without DrainPushes carries nothing.
func (sw *Switcher) carryPushes(old, inner Session, from, to string) {
	d, ok := old.(interface{ DrainPushes() []QueuedPush })
	if !ok {
		return
	}
	queued := d.DrainPushes()
	if len(queued) == 0 {
		return
	}
	carried := 0
	for _, q := range queued {
		var err error
		if tp, ok := inner.(interface {
			PushTagged(string, Urgency, map[string]any) error
		}); ok {
			err = tp.PushTagged(q.Text, q.Urgency, q.Tags)
		} else {
			err = inner.Push(q.Text, q.Urgency)
		}
		if err != nil {
			p := map[string]any{"text": q.Text, "urgency": q.Urgency.String(), "reason": "provider_switch", "error": err.Error(), "from": from, "to": to}
			for k, v := range q.Tags {
				p[k] = v
			}
			sw.log.Log("voice", "voice.push_dropped", "", p)
			continue
		}
		carried++
	}
	sw.log.Log("voice", "voice.push_carried", "", map[string]any{"count": carried, "from": from, "to": to})
}

// forwardLocked pumps inner's audio and events onto the switcher's while
// inner is the current session.
func (sw *Switcher) forwardLocked(inner Session) {
	ctx := sw.ctx
	sw.fwd.Add(2)
	go func() {
		defer sw.fwd.Done()
		for chunk := range inner.Audio() {
			if !sw.isCurrent(inner) {
				continue
			}
			select {
			case sw.audio <- chunk:
			case <-ctx.Done():
			}
		}
	}()
	go func() {
		defer sw.fwd.Done()
		for ev := range inner.Events() {
			if !sw.isCurrent(inner) {
				continue // a retired session's goodbyes
			}
			sw.emit(ev)
		}
	}()
}

func (sw *Switcher) isCurrent(s Session) bool {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return sw.inner == s
}

func (sw *Switcher) emit(ev Event) {
	select {
	case sw.events <- ev:
	default:
		sw.log.Log("voice", "voice.event_dropped", "", map[string]any{"event": fmt.Sprintf("%T", ev)})
	}
}

// Close implements Session.
func (sw *Switcher) Close() error {
	sw.mu.Lock()
	if !sw.started || sw.closed {
		sw.closed = true
		sw.mu.Unlock()
		return nil
	}
	sw.closed = true
	inner := sw.inner
	sw.mu.Unlock()
	err := inner.Close()
	sw.retire.Wait()
	sw.fwd.Wait()
	close(sw.audio)
	close(sw.events)
	return err
}

func (sw *Switcher) cur() Session {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return sw.inner
}

// errNotStarted is returned by the forwarding methods before Start.
var errNotStarted = errors.New("voice: not started")

// SendAudio implements Session.
func (sw *Switcher) SendAudio(pcm16 []byte) error {
	if s := sw.cur(); s != nil {
		return s.SendAudio(pcm16)
	}
	return errNotStarted
}

// SendText implements Session.
func (sw *Switcher) SendText(text string) error {
	if s := sw.cur(); s != nil {
		return s.SendText(text)
	}
	return errNotStarted
}

// Push implements Session.
func (sw *Switcher) Push(text string, u Urgency) error {
	if s := sw.cur(); s != nil {
		return s.Push(text, u)
	}
	return errNotStarted
}

// PushTagged forwards a tagged push (supervisor.TagPusher) when the
// current session takes tags.
func (sw *Switcher) PushTagged(text string, u Urgency, tags map[string]any) error {
	s := sw.cur()
	if s == nil {
		return errNotStarted
	}
	if tp, ok := s.(interface {
		PushTagged(string, Urgency, map[string]any) error
	}); ok && len(tags) > 0 {
		return tp.PushTagged(text, u, tags)
	}
	return s.Push(text, u)
}

// Reset drops the current session's conversational memory and starts it
// over fresh, on the same provider ("New conversation",
// docs/roles.md): for Gemini, the resumption handle is dropped and the
// session reconnects with none; for GPT-Live, an open billed session is
// closed (no recap) and reopens later on the next speech or push. A
// no-op before Start, after Close, or when the current provider's
// session does not implement it.
func (sw *Switcher) Reset(reason string) error {
	if s, ok := sw.cur().(interface{ Reset(string) error }); ok {
		return s.Reset(reason)
	}
	return nil
}

// Speaking implements Session.
func (sw *Switcher) Speaking() bool {
	if s := sw.cur(); s != nil {
		return s.Speaking()
	}
	return false
}

// Audio implements Session.
func (sw *Switcher) Audio() <-chan []byte { return sw.audio }

// Events implements Session.
func (sw *Switcher) Events() <-chan Event { return sw.events }

// SetMuted implements Session (remembered for the next provider).
func (sw *Switcher) SetMuted(muted bool) {
	sw.mu.Lock()
	sw.muted = muted
	s := sw.inner
	sw.mu.Unlock()
	if s != nil {
		s.SetMuted(muted)
	}
}

// ClientAttached tells the session whether a /ws client is attached
// (remembered for the next provider).
func (sw *Switcher) ClientAttached(attached bool) {
	sw.mu.Lock()
	sw.client = attached
	s := sw.inner
	sw.mu.Unlock()
	if ca, ok := s.(interface{ ClientAttached(bool) }); ok {
		ca.ClientAttached(attached)
	}
}

// HasListener reports whether a /ws client is attached now
// (supervisor.Listener): a reply returned as a tool result would be
// spoken to nobody without one.
func (sw *Switcher) HasListener() bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.client
}

// ClientPlayback forwards a /ws client playback report to the current
// session (docs/ws-contract.md "Playback reports"). A new provider's
// session starts with no run, so a report about the old one's audio
// (the flush a switch sends) is logged and otherwise ignored there.
func (sw *Switcher) ClientPlayback(state string, playedMs int) {
	if p, ok := sw.cur().(interface{ ClientPlayback(string, int) }); ok {
		p.ClientPlayback(state, playedMs)
	}
}

// ClientSpeaking forwards the /ws client's speech detector.
func (sw *Switcher) ClientSpeaking(on bool) {
	if p, ok := sw.cur().(interface{ ClientSpeaking(bool) }); ok {
		p.ClientSpeaking(on)
	}
}

// LastPlayback is the current session's last client playback report.
func (sw *Switcher) LastPlayback() PlaybackReport {
	if p, ok := sw.cur().(interface{ LastPlayback() PlaybackReport }); ok {
		return p.LastPlayback()
	}
	return PlaybackReport{}
}

// AudioStreamEnd forwards the client's silence gate closing.
func (sw *Switcher) AudioStreamEnd() {
	if ae, ok := sw.cur().(interface{ AudioStreamEnd() }); ok {
		ae.AudioStreamEnd()
	}
}

// Status reports the current session's status.
func (sw *Switcher) Status() Status {
	sw.mu.RLock()
	closed := sw.closed
	s := sw.inner
	sw.mu.RUnlock()
	if closed {
		return StatusClosed
	}
	if st, ok := s.(interface{ Status() Status }); ok {
		return st.Status()
	}
	return StatusConnecting
}

// BilledSeconds is the per-minute provider duration billed in this run:
// retired sessions plus the current one (0 for Gemini).
func (sw *Switcher) BilledSeconds() float64 {
	sw.mu.RLock()
	t := sw.billedRetired
	s := sw.inner
	sw.mu.RUnlock()
	if b, ok := s.(interface{ BilledSeconds() float64 }); ok {
		t += b.BilledSeconds()
	}
	return t
}

// VoiceStats is merged into web.client.stats: the provider, the billed
// seconds so far and the current session's own stats.
func (sw *Switcher) VoiceStats() map[string]any {
	out := map[string]any{}
	if vs, ok := sw.cur().(interface{ VoiceStats() map[string]any }); ok {
		for k, v := range vs.VoiceStats() {
			out[k] = v
		}
	}
	out["voice_provider"] = sw.Provider()
	out["voice_billed_seconds"] = sw.BilledSeconds()
	return out
}
