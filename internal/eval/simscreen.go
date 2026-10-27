package eval

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// The simulated phone screen (the orchestrator's show tool): a run's
// screen is visible or not, as SimOptions.Screen says, and a card shown
// on a visible screen reaches the simulated user and the judge as a
// "screen" line (seen, not heard), and is counted in SimRun.Cards.

// Screen modes of SimOptions.Screen (eval sim --screen).
const (
	// ScreenNone connects no screen: show finds none (main's behaviour
	// before cards; the default).
	ScreenNone = ""
	// ScreenVisible and ScreenHidden fix every run's screen.
	ScreenVisible = "visible"
	ScreenHidden  = "hidden"
	// ScreenAlternate makes about half the runs visible: by scenario and
	// repeat, deterministically, so every scenario run three times or
	// more gets both.
	ScreenAlternate = "alternate"
)

// ScreenModes lists the accepted --screen values.
var ScreenModes = []string{ScreenVisible, ScreenHidden, ScreenAlternate}

// ValidScreenMode reports whether m is a --screen value ("" included).
func ValidScreenMode(m string) bool {
	return m == ScreenNone || m == ScreenVisible || m == ScreenHidden || m == ScreenAlternate
}

// ScreenFor is the screen of scenario id's run repeat under mode:
// visible, hidden, or "" (no screen).
func ScreenFor(mode, id string, repeat int) string {
	switch mode {
	case ScreenVisible, ScreenHidden:
		return mode
	case ScreenAlternate:
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		if (int(h.Sum32()%2)+repeat)%2 == 0 {
			return ScreenVisible
		}
		return ScreenHidden
	}
	return ScreenNone
}

// SimCard is one card the orchestrator put on the simulated screen.
type SimCard struct {
	AtMS  int64  `json:"at_ms"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Agent string `json:"agent,omitempty"`
	// Bytes is the size of its content (markdown or excerpt).
	Bytes int `json:"bytes"`
	// Text is the card as the user saw it (simScreenText).
	Text string `json:"text"`
}

// simScreen is the run's screen, the supervisor's CardSink.
type simScreen struct {
	w       *simWorld
	visible bool

	mu    sync.Mutex
	cards []SimCard
}

// ScreenVisible implements supervisor.CardSink.
func (s *simScreen) ScreenVisible() bool { return s.visible }

// SendCard implements supervisor.CardSink: the card is seen at once.
func (s *simScreen) SendCard(c supervisor.Card) supervisor.Delivery {
	if !s.visible {
		return supervisor.NotDelivered
	}
	text := simScreenText(c)
	card := SimCard{AtMS: s.w.since(), Kind: c.Kind, Title: c.Title, Agent: c.Agent, Bytes: len(c.Markdown) + len(c.Text), Text: text}
	s.mu.Lock()
	s.cards = append(s.cards, card)
	s.mu.Unlock()
	select {
	case s.w.push.ch <- SimLine{AtMS: card.AtMS, Who: "screen", Text: text, Agent: c.Agent}:
	default:
	}
	return supervisor.Presented
}

// shown returns the cards shown so far.
func (s *simScreen) shown() []SimCard {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SimCard(nil), s.cards...)
}

// simScreenText is a card as text, the way the user would read it.
func simScreenText(c supervisor.Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q", c.Title)
	switch {
	case c.Markdown != "":
		b.WriteString("\n" + c.Markdown)
	case c.Text != "":
		b.WriteString("\n" + c.Text)
	case c.Question != "":
		b.WriteString("\n" + c.Question)
		if c.Command != "" {
			b.WriteString("\n$ " + c.Command)
		}
		var opts []string
		for _, o := range c.Options {
			opts = append(opts, o.Label)
		}
		if len(opts) > 0 {
			b.WriteString("\nOptions: " + strings.Join(opts, "; "))
		}
	case c.Kind == "agent":
		b.WriteString("\n(" + orDefault(c.Spoken, c.Agent) + "'s live terminal window)")
	}
	return b.String()
}
