package supervisor

import (
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Current focus, brain → voice (docs/bench-2026-10-03.md "Kept as
// options / backlog"). The voice model hears only what it says and what
// it is pushed; the brain's turn knows which agent the conversation is
// about, which question is open and what the answer's detail held. After
// a brain turn produced a line for the shepherd the daemon tells the
// voice those facts in one silent context line, so it can resolve "it"
// and "that one" and answer a simple follow-up ("what were the options
// again?") without a round trip. No extra model call: the facts come
// from the reply and the state table. The line is sent once the voice's
// turn is over (EventTurnComplete or EventInterrupted), never while a
// tool call it answers is still open, and only when it changed. It
// carries voice.TagFocus, so the voice session keeps the latest one and
// hands it to a fresh session with the recap (voice.Recap.SetFocus).
// The voice decides nothing from it: anything that needs a decision or
// new information still goes to the supervisor.

// focusMaxRunes bounds the focus line (a runaway guard: the line is
// context, never speech).
const focusMaxRunes = 500

// focusPointRunes bounds one key point.
const focusPointRunes = 160

// focusFacts is what one brain reply says about the conversation's
// focus: the agent it is about (paneID, "" to find it from the words),
// the reply as the shepherd heard it, its detail, its ask and options.
type focusFacts struct {
	paneID  string
	spoken  string
	detail  string
	ask     string
	options []string
}

// focusState is the focus line bookkeeping, under its own lock.
type focusState struct {
	mu   sync.Mutex
	sent string // the focus line last pushed
	want string // the line waiting for the voice's turn to end
}

// focusFromReply is the focus of a brain reply bound for the voice: the
// spoken line as earLine/toolResultFrom made it and the D9-scrubbed
// detail, ask and options.
func focusFromReply(paneID, spoken string, rep brain.Reply) focusFacts {
	f := focusFacts{paneID: paneID, spoken: spoken, detail: prose.Spoken(rep.Detail, 0), ask: prose.Spoken(rep.Ask, 0)}
	for _, o := range rep.Options {
		if o = strings.TrimSpace(prose.Spoken(o, 1)); o != "" {
			f.options = append(f.options, o)
		}
	}
	return f
}

// noteFocus composes the focus line of a reply the shepherd was given
// (asked: the shepherd's words it answers, "" for an event) and keeps it
// for the end of the voice's turn.
func (s *Supervisor) noteFocus(f focusFacts, asked string) {
	if strings.TrimSpace(f.spoken) == "" {
		return
	}
	line := s.focusLine(f, asked)
	if line == "" {
		return
	}
	s.focus.mu.Lock()
	s.focus.want = line
	s.focus.mu.Unlock()
}

// flushFocus pushes the waiting focus line, silently, unless it is the
// one last sent.
func (s *Supervisor) flushFocus() {
	s.focus.mu.Lock()
	line := s.focus.want
	s.focus.want = ""
	if line == "" || line == s.focus.sent {
		s.focus.mu.Unlock()
		return
	}
	s.focus.sent = line
	s.focus.mu.Unlock()
	s.log.Log(logSource, "supervisor.focus", "", map[string]any{"text": line})
	s.pushTagged("", line, voice.UrgencySilent, map[string]any{voice.TagFocus: true})
}

// forgetFocus drops the focus ("New conversation"): the fresh
// conversation starts without one.
func (s *Supervisor) forgetFocus() {
	s.focus.mu.Lock()
	s.focus.sent, s.focus.want = "", ""
	s.focus.mu.Unlock()
}

// focusLine renders the facts: the agent in focus by its spoken name,
// the open question with its options, and one to three key points of
// the answer. "" when there is nothing to say.
func (s *Supervisor) focusLine(f focusFacts, asked string) string {
	var b strings.Builder
	b.WriteString("Current focus, for follow-ups (context only, never read it out).")
	if row, ok := s.focusAgent(f, asked); ok {
		b.WriteString(" Agent in focus: " + state.Say(row) + ".")
	}
	if q := s.openQuestion(f); q != "" {
		b.WriteString(" " + q)
	}
	if pts := focusPoints(f); len(pts) > 0 {
		b.WriteString(" Key points of the last answer:")
		for i, p := range pts {
			b.WriteString(" (" + string(rune('1'+i)) + ") " + p)
		}
	}
	return prose.TruncateRunes(b.String(), focusMaxRunes)
}

// focusAgent is the agent the reply is about: the event's own agent,
// else the one agent the reply names (spoken name or alias), else the
// one the shepherd's words named, else the conversation's current
// subject (the agent the brain last read or acted on).
func (s *Supervisor) focusAgent(f focusFacts, asked string) (state.Agent, bool) {
	if f.paneID != "" {
		return s.store.Get(f.paneID)
	}
	rows := s.store.List()
	for _, text := range []string{f.spoken, asked} {
		lower := strings.ToLower(text)
		var named []state.Agent
		for _, row := range rows {
			say := strings.ToLower(state.Say(row))
			alias := strings.ToLower(row.Alias)
			if (say != "" && strings.Contains(lower, say)) || (alias != "" && strings.Contains(lower, alias)) {
				named = append(named, row)
			}
		}
		if len(named) == 1 {
			return named[0], true
		}
		if len(named) > 1 {
			return state.Agent{}, false
		}
	}
	s.mu.Lock()
	subject := s.lastSubject
	s.mu.Unlock()
	if subject == "" {
		return state.Agent{}, false
	}
	return s.store.Get(subject)
}

// openQuestion names what waits for the shepherd: the question the reply
// asked, with its options, and the worker's approval prompt that is
// open, with its answer options (never the free-text entry).
func (s *Supervisor) openQuestion(f focusFacts) string {
	var parts []string
	if f.ask != "" {
		q := "Open question you asked: " + sentence(f.ask)
		if len(f.options) > 0 {
			q += " Options: " + strings.Join(f.options, "; ") + "."
		}
		parts = append(parts, q)
	}
	if p, has := s.store.Pending(); has && p.Kind == state.PendingAnswerPrompt {
		q := s.sayOf(p.PaneID) + " is waiting for an answer: " + sentence(prose.Spoken(p.Question, 2))
		var labels []string
		for _, o := range p.Options {
			if !o.Meta && strings.TrimSpace(o.Label) != "" {
				labels = append(labels, strings.TrimSpace(o.Label))
			}
		}
		if len(labels) > 0 {
			q += " Options: " + strings.Join(labels, "; ") + "."
		}
		parts = append(parts, q)
	}
	return strings.Join(parts, " ")
}

// focusPoints are up to three key points of the answer: the spoken
// line's first two sentences (its ask aside, already the open question)
// and the detail's first sentence, each bounded.
func focusPoints(f focusFacts) []string {
	spoken := f.spoken
	if f.ask != "" {
		spoken = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(spoken), sentence(f.ask)))
	}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || len(out) >= 3 {
			return
		}
		for _, have := range out {
			if have == p {
				return
			}
		}
		out = append(out, prose.TruncateRunes(p, focusPointRunes))
	}
	for _, p := range sentences(spoken, 2) {
		add(p)
	}
	for _, p := range sentences(f.detail, 1) {
		add(p)
	}
	return out
}

// sentences returns the first n sentences of text (prose's CJK-aware
// sentence ends).
func sentences(text string, n int) []string {
	var out []string
	rest := prose.Spoken(text, 0)
	for len(out) < n && rest != "" {
		first := prose.Spoken(rest, 1)
		if first == "" {
			break
		}
		out = append(out, first)
		i := strings.Index(rest, first)
		if i < 0 {
			break
		}
		rest = strings.TrimSpace(rest[i+len(first):])
	}
	return out
}
