package web

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// Cards over /ws (docs/dev/ws-contract.md "Cards"): the client reports
// whether its screen is visible (visibility), and the orchestrator's show
// tool puts a card on it (card). The web layer owns the visibility: only
// the attached client counts, it starts not visible, and it stops counting
// when it is replaced or goes away, so a client that never reports never
// gets a card and the orchestrator speaks instead.

// CardHost is the supervisor's card side, when it has one: the screen it
// shows cards on, and the card store the conversation history reads.
type CardHost interface {
	SetCardSink(supervisor.CardSink)
	Card(id string) (supervisor.Card, bool)
}

// cardHost returns the supervisor's card side, if any.
func (s *Server) cardHost() (CardHost, bool) {
	if s.deps.Supervisor == nil {
		return nil, false
	}
	ch, ok := s.deps.Supervisor.(CardHost)
	return ch, ok
}

// cardSink is the hub as the supervisor's CardSink.
type cardSink struct{ h *hub }

// ScreenVisible implements supervisor.CardSink: the attached client said
// its screen is visible (its app is active), on any screen.
func (k cardSink) ScreenVisible() bool {
	c := k.h.current()
	return c != nil && c.screenVisible()
}

// SendCard implements supervisor.CardSink: the card goes to the attached
// client while its screen is visible, presented when its voice screen is
// in front and placed (filed as a notice, same frame) when another screen is.
func (k cardSink) SendCard(card supervisor.Card) supervisor.Delivery {
	c := k.h.current()
	if c == nil || !c.screenVisible() {
		return supervisor.NotDelivered
	}
	d := supervisor.Placed
	if c.voiceScreenFront() {
		d = supervisor.Presented
	}
	c.enqueue(controlFrame(wsproto.CardFrame(cardWire(card))))
	return d
}

// voiceScreenFront reports that the client's voice screen is in front.
func (c *client) voiceScreenFront() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.voiceScreen
}

// screenVisible reports the client's last visibility report.
func (c *client) screenVisible() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closing && c.visible
}

// setVisibility records a visibility report, logging a change.
func (c *client) setVisibility(visible, voiceScreen bool) {
	c.mu.Lock()
	changed := !c.visReported || c.visible != visible || c.voiceScreen != voiceScreen
	c.visible, c.voiceScreen, c.visReported = visible, voiceScreen, true
	c.mu.Unlock()
	if changed {
		c.h.s.log.Log(logSource, "web.client.visibility", "", map[string]any{"client_id": c.id, "visible": visible, "voice_screen": voiceScreen})
	}
}

// cardWire is a card's wire form.
func cardWire(c supervisor.Card) wsproto.Card {
	w := wsproto.Card{
		ID: c.ID, Kind: c.Kind, Title: c.Title, Markdown: c.Markdown,
		Agent: c.Agent, Spoken: c.Spoken, PaneID: c.PaneID, Text: c.Text,
		PendingID: c.PendingID, Question: c.Question, Command: c.Command,
	}
	if !c.At.IsZero() {
		w.At = c.At.UTC().Format(time.RFC3339Nano)
	}
	for _, o := range c.Options {
		w.Options = append(w.Options, wsproto.CardOption{Index: o.Index, Label: o.Label, Keys: append([]string{}, o.Keys...), Default: o.Default})
	}
	return w
}
