package web

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// A card reaches the attached client only while it reports a visible
// voice screen; it is logged without its content and appears in the
// conversation history with the card from the store.
func TestWSCards(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	trackWorking(t, h, "fake:1", "auth", "line one\nline two\nline three")
	c, features := readyClient(t, h)
	if !slices.Contains(features, wsproto.FeatureCards) {
		t.Fatalf("features %v lack %s", features, wsproto.FeatureCards)
	}
	ctx := context.Background()
	md := "| Agent | State |\n|---|---|\n| auth | working |"
	card := brain.CardRequest{Kind: brain.ShowCard, Title: "Agents", Markdown: md, Turn: 3}

	// A client that has not reported is not visible.
	if h.sup.ScreenVisible() {
		t.Fatal("visible before any report")
	}
	if _, err := h.sup.Show(ctx, card); !errors.Is(err, brain.ErrNotVisible) {
		t.Fatalf("show before a report = %v", err)
	}

	// The page in the background: still not visible.
	c.send(wsproto.Visibility(false, true))
	c.send(wsproto.Ping())
	c.expect(wsproto.TypePong)
	if h.sup.ScreenVisible() {
		t.Fatal("visible while in the background")
	}

	c.send(wsproto.Visibility(true, true))
	waitFor(t, 2*time.Second, h.sup.ScreenVisible, "visible")
	res, err := h.sup.Show(ctx, card)
	if err != nil {
		t.Fatal(err)
	}
	m := c.expect(wsproto.TypeCard)
	if m.Card.ID != res.ID || m.Card.Kind != "card" || m.Card.Title != "Agents" || m.Card.Markdown != md || m.Card.At == "" {
		t.Fatalf("card = %+v", m.Card)
	}

	// An excerpt is read by the daemon itself.
	if _, err := h.sup.Show(ctx, brain.CardRequest{Kind: brain.ShowExcerpt, PaneID: "fake:1", Lines: 2}); err != nil {
		t.Fatal(err)
	}
	m = c.expect(wsproto.TypeCard)
	if m.Card.Kind != "excerpt" || m.Card.Text != "line two\nline three" || m.Card.PaneID != "fake:1" || m.Card.Agent != "auth" {
		t.Fatalf("excerpt = %+v", m.Card)
	}
	// No open approval: refused.
	if _, err := h.sup.Show(ctx, brain.CardRequest{Kind: brain.ShowApproval, PaneID: "fake:1"}); err == nil || errors.Is(err, brain.ErrNotVisible) {
		t.Fatalf("approval without a pending prompt = %v", err)
	}

	// The log carries metadata only.
	var shown int
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "card.shown" {
			shown++
			if strings.Contains(string(ev.Payload), "working") || strings.Contains(string(ev.Payload), "line two") {
				t.Errorf("card.shown carries content: %s", ev.Payload)
			}
		}
	}
	if shown != 2 {
		t.Fatalf("card.shown logged %d times", shown)
	}

	// History: the card item with its card.
	var conv ConversationResponse
	h.getJSON("/api/conversation", &conv)
	var cards []ConversationItem
	for _, it := range conv.Items {
		if it.Kind == "card" {
			cards = append(cards, it)
		}
	}
	if len(cards) != 2 || cards[0].Card == nil || cards[0].Card.Markdown != md || cards[0].Text != "Agents" || cards[1].Agent != "auth" {
		b, _ := json.Marshal(cards)
		t.Fatalf("history cards = %s", b)
	}

	// Hidden again, then gone: not visible.
	c.send(wsproto.Visibility(false, true))
	waitFor(t, 2*time.Second, func() bool { return !h.sup.ScreenVisible() }, "hidden")
	c.send(wsproto.Visibility(true, true))
	waitFor(t, 2*time.Second, h.sup.ScreenVisible, "visible again")
	_ = c.conn.CloseNow()
	waitFor(t, 2*time.Second, func() bool { return !h.sup.ScreenVisible() }, "not visible after disconnect")
	if !subsequence(h.types(), []string{"card.refused", "web.client.visibility", "card.shown", "card.shown", "web.client.visibility"}) {
		t.Fatalf("events = %v", h.types())
	}
}

// A replacement client starts not visible, whatever the one before said.
func TestWSCardsVisibilityNotInherited(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c, _ := readyClient(t, h)
	c.send(wsproto.Visibility(true, true))
	waitFor(t, 2*time.Second, h.sup.ScreenVisible, "visible")
	c2, _ := readyClient(t, h)
	waitFor(t, 2*time.Second, func() bool { return !h.sup.ScreenVisible() }, "a replacement starts not visible")
	c2.send(wsproto.Visibility(true, true))
	waitFor(t, 2*time.Second, h.sup.ScreenVisible, "the replacement's own report")
}

// The app active but on another screen than the conversation still gets
// the card, filed as a notice (Placed); with the conversation in front
// it is presented.
func TestWSCardsPlacedOnOtherScreen(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c, _ := readyClient(t, h)
	c.send(wsproto.Visibility(true, false))
	waitFor(t, 2*time.Second, h.sup.ScreenVisible, "visible on another screen")
	ctx := context.Background()
	res, err := h.sup.Show(ctx, brain.CardRequest{Kind: brain.ShowCard, Title: "Agents", Markdown: "- auth"})
	if err != nil || !res.Placed {
		t.Fatalf("show on another screen = %+v, %v; want Placed", res, err)
	}
	if m := c.expect(wsproto.TypeCard); m.Card.ID != res.ID {
		t.Fatalf("card = %+v", m.Card)
	}
	c.send(wsproto.Visibility(true, true))
	waitFor(t, 2*time.Second, func() bool {
		r, err := h.sup.Show(ctx, brain.CardRequest{Kind: brain.ShowCard, Title: "Again", Markdown: "- auth"})
		return err == nil && !r.Placed
	}, "presented with the conversation in front")
}
