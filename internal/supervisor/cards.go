package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Cards: the orchestrator's show tool puts something on the user's screen
// (docs/dev/ws-contract.md "Cards"). Display only: building a card reads
// the roster, the pending action or a pane, and changes none of them;
// an approval card carries no way to answer (the one rail stays in
// send_keys and the tap path). The screen's visibility is the client's
// own report, kept by the web layer behind CardSink; with no sink, or a
// sink that says it is not visible, show is refused and the orchestrator
// speaks instead.

// CardsFile is the card store under the data dir: one JSON card per
// line, appended as each is shown (what the conversation history shows
// for a card.shown event; the event itself carries metadata only).
const CardsFile = "cards.jsonl"

// cardsKept is how many cards the store keeps; the file is rewritten
// with the newest ones when it holds twice as many.
const cardsKept = 500

// Card is one card as the client gets it (the /ws card message) and as
// the store keeps it.
type Card struct {
	ID   string    `json:"card_id"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// Title is the card's heading.
	Title string `json:"title"`
	// Markdown is a card kind's content.
	Markdown string `json:"markdown,omitempty"`
	// Agent is the alias and Spoken the spoken name of the agent an
	// agent, excerpt or approval card is about; PaneID opens its window.
	Agent  string `json:"agent,omitempty"`
	Spoken string `json:"spoken,omitempty"`
	PaneID string `json:"pane_id,omitempty"`
	// Text is an excerpt's screen lines, verbatim (monospace).
	Text string `json:"text,omitempty"`
	// PendingID, Question, Command and Options are an approval card's
	// open question, read-only: the client answers it, if the user wants,
	// through its existing approval card (POST /api/pending/answer).
	PendingID string          `json:"pending_id,omitempty"`
	Question  string          `json:"question,omitempty"`
	Command   string          `json:"command,omitempty"`
	Options   []PendingOption `json:"options,omitempty"`
}

// CardSink is the screen side of cards, implemented by the web layer:
// whether a client's screen is visible right now (it said so, and its
// voice screen is in front), and delivering a card to it. SendCard
// reports false when the card did not reach a visible screen.
type CardSink interface {
	ScreenVisible() bool
	SendCard(Card) bool
}

// cardState is the supervisor's card side: the sink and the store.
type cardState struct {
	mu    sync.Mutex
	sink  CardSink
	cards []Card // oldest first, at most cardsKept
	file  int    // lines in the store file
	seq   uint64
}

// SetCardSink connects the screen (the web layer, a simulated screen in
// tests); nil disconnects it.
func (s *Supervisor) SetCardSink(sink CardSink) {
	s.cards.mu.Lock()
	s.cards.sink = sink
	s.cards.mu.Unlock()
}

func (s *Supervisor) cardSink() CardSink {
	s.cards.mu.Lock()
	defer s.cards.mu.Unlock()
	return s.cards.sink
}

// ScreenVisible implements brain.DisplayHost: whether show can put
// something on the user's screen now.
func (s *Supervisor) ScreenVisible() bool {
	sink := s.cardSink()
	return sink != nil && sink.ScreenVisible()
}

// The orchestrator's DisplayHost.
func (h brainHost) ScreenVisible() bool { return h.s.ScreenVisible() }
func (h brainHost) Show(ctx context.Context, req brain.CardRequest) (brain.CardResult, error) {
	return h.s.Show(ctx, req)
}

// Card returns a stored card by id.
func (s *Supervisor) Card(id string) (Card, bool) {
	s.cards.mu.Lock()
	defer s.cards.mu.Unlock()
	for i := len(s.cards.cards) - 1; i >= 0; i-- {
		if s.cards.cards[i].ID == id {
			return s.cards.cards[i], true
		}
	}
	return Card{}, false
}

// Show implements brain.DisplayHost: build the card, deliver it to the
// visible screen, store it and log card.shown (metadata only). Not
// visible is brain.ErrNotVisible, logged as card.refused.
func (s *Supervisor) Show(ctx context.Context, req brain.CardRequest) (brain.CardResult, error) {
	sink := s.cardSink()
	if sink == nil || !sink.ScreenVisible() {
		s.log.Log(logSource, "card.refused", req.PaneID, map[string]any{"kind": req.Kind, "turn": req.Turn, "reason": "not_visible"})
		return brain.CardResult{}, brain.ErrNotVisible
	}
	card, err := s.buildCard(ctx, req)
	if err != nil {
		return brain.CardResult{}, err
	}
	s.cards.mu.Lock()
	s.cards.seq++
	card.ID = "c" + strconv.FormatInt(s.cfg.Now().UnixMilli(), 36) + strconv.FormatUint(s.cards.seq, 36)
	s.cards.mu.Unlock()
	card.At = s.cfg.Now().UTC()
	if !sink.SendCard(card) {
		s.log.Log(logSource, "card.refused", req.PaneID, map[string]any{"kind": req.Kind, "turn": req.Turn, "reason": "not_delivered"})
		return brain.CardResult{}, brain.ErrNotVisible
	}
	s.storeCard(card)
	s.log.Log(logSource, "card.shown", card.PaneID, map[string]any{
		"card_id": card.ID, "kind": card.Kind, "agent": card.Agent, "turn": req.Turn,
		"title_chars": utf8.RuneCountInString(card.Title), "bytes": len(card.Markdown) + len(card.Text), "options": len(card.Options),
	})
	return brain.CardResult{ID: card.ID, Title: card.Title}, nil
}

// buildCard makes the card for req from the daemon's own facts: the
// orchestrator's title and markdown for a card, the roster for an agent,
// a pane read for an excerpt, the pending action for an approval.
func (s *Supervisor) buildCard(ctx context.Context, req brain.CardRequest) (Card, error) {
	card := Card{Kind: req.Kind}
	if req.Kind == brain.ShowCard {
		card.Title, card.Markdown = req.Title, req.Markdown
		return card, nil
	}
	row, ok := s.store.Get(req.PaneID)
	if !ok {
		return Card{}, ErrNoSuchAgent
	}
	name := aliasOr(state.Say(row))
	card.Agent, card.Spoken, card.PaneID = row.Alias, state.Say(row), req.PaneID
	switch req.Kind {
	case brain.ShowAgent:
		card.Title = name
	case brain.ShowExcerpt:
		lines := req.Lines
		if lines <= 0 {
			lines = brain.ShowExcerptLines
		}
		read, err := s.herdr.Read(ctx, req.PaneID, herdr.SourceRecentUnwrapped, lines)
		if err != nil {
			return Card{}, err
		}
		card.Text = lastLines(reANSI.ReplaceAllString(read.Text, ""), lines)
		card.Title = name + ": its screen"
	case brain.ShowApproval:
		p, has := s.store.Pending()
		if !has || p.Kind != state.PendingAnswerPrompt || p.PaneID != req.PaneID {
			return Card{}, fmt.Errorf("%s has no open approval prompt to show", name)
		}
		card.Title = name + " is asking"
		card.PendingID, card.Command, card.Options = p.ID, p.Command, PendingOptions(p)
		card.Question = strings.TrimSpace(p.Question)
		if card.Question == "" {
			card.Question = strings.TrimSpace(p.SpokenSummary)
		}
	default:
		return Card{}, fmt.Errorf("show kind %q", req.Kind)
	}
	return card, nil
}

// lastLines is text's last n lines, trailing blank lines dropped.
func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n \t"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// storeCard keeps card in memory and appends it to the store file.
func (s *Supervisor) storeCard(card Card) {
	s.cards.mu.Lock()
	defer s.cards.mu.Unlock()
	s.cards.cards = append(s.cards.cards, card)
	if over := len(s.cards.cards) - cardsKept; over > 0 {
		s.cards.cards = append([]Card(nil), s.cards.cards[over:]...)
	}
	if s.cfg.DataDir == "" {
		return
	}
	path := filepath.Join(s.cfg.DataDir, CardsFile)
	if s.cards.file+1 >= 2*cardsKept {
		if err := writeCards(path, s.cards.cards); err != nil {
			s.log.Log(logSource, "card.store_error", "", map[string]any{"error": err.Error()})
			return
		}
		s.cards.file = len(s.cards.cards)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		line, _ := json.Marshal(card)
		_, err = f.Write(append(line, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		s.log.Log(logSource, "card.store_error", "", map[string]any{"error": err.Error()})
		return
	}
	s.cards.file++
}

// loadCards reads the store file at start (a missing file is no cards).
func (s *Supervisor) loadCards() {
	if s.cfg.DataDir == "" {
		return
	}
	f, err := os.Open(filepath.Join(s.cfg.DataDir, CardsFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.log.Log(logSource, "card.store_error", "", map[string]any{"error": err.Error()})
		}
		return
	}
	defer func() { _ = f.Close() }()
	var cards []Card
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		n++
		var c Card
		if json.Unmarshal(sc.Bytes(), &c) == nil && c.ID != "" {
			cards = append(cards, c)
		}
	}
	if over := len(cards) - cardsKept; over > 0 {
		cards = cards[over:]
	}
	s.cards.mu.Lock()
	s.cards.cards, s.cards.file = cards, n
	s.cards.mu.Unlock()
}

// writeCards replaces the store file with cards, atomically.
func writeCards(path string, cards []Card) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, c := range cards {
		line, _ := json.Marshal(c)
		_, _ = w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
