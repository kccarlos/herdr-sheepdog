package brain_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

const recheckLine = `{"spoken": "auth wants to run the diff. Approve?", "urgency": "interrupt", "needs_user": true}`

// spokenDuring returns a Facts func whose Spoken gains the template line
// from its second call on: the line went out while the first step ran.
func spokenDuring() func() EventFacts {
	var mu sync.Mutex
	n := 0
	return func() EventFacts {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return EventFacts{Prompt: PromptWaiting}
		}
		return EventFacts{Spoken: []string{"auth asks to run git diff."}, Prompt: PromptWaiting}
	}
}

func announceReq(facts func() EventFacts) Request {
	return Request{Kind: KindAnnounce, Trigger: TriggerEvent, Agent: "auth", Event: PromiseEventBlocked, Template: "auth asks to run git diff.", Facts: facts}
}

// A one-step announce whose event got a spoken line meanwhile is asked
// once more, answer-only, with the update; the second answer is the
// turn's.
func TestOneStepAnnounceIsRecheckedWithTheSpokenLine(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: recheckLine}, braintest.Step{Text: braintest.NothingToAdd().Text})
	h := newHarness(t, m, nil)
	rep, err := h.brain.Turn(context.Background(), announceReq(spokenDuring()))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Urgency != UrgencySkip {
		t.Errorf("reply = %+v, want the recheck's skip", rep)
	}
	calls := m.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	txt := calls[1].Text()
	if !strings.Contains(txt, `while you were working the assistant said, about this event: "auth asks to run git diff."`) || !strings.Contains(txt, "has not been spoken") {
		t.Errorf("recheck step lacks the update:\n%s", txt)
	}
	if strings.Contains(txt, "Time is up") {
		t.Errorf("recheck step claims the time is up:\n%s", txt)
	}
	if h.log.count("brain.recheck") != 1 {
		t.Errorf("brain.recheck = %d", h.log.count("brain.recheck"))
	}
}

// Nothing new was spoken, or the answer was a skip: no second step.
func TestAnnounceNotRecheckedWithoutANewLine(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: recheckLine})
	h := newHarness(t, m, nil)
	if _, err := h.brain.Turn(context.Background(), announceReq(func() EventFacts { return EventFacts{Prompt: PromptWaiting} })); err != nil {
		t.Fatal(err)
	}
	m2 := braintest.NewModel(braintest.Step{Text: braintest.NothingToAdd().Text})
	h2 := newHarness(t, m2, nil)
	if _, err := h2.brain.Turn(context.Background(), announceReq(spokenDuring())); err != nil {
		t.Fatal(err)
	}
	if len(m.Calls()) != 1 || len(m2.Calls()) != 1 {
		t.Errorf("calls = %d, %d", len(m.Calls()), len(m2.Calls()))
	}
}

// A recheck that does not answer (a model error, or the announce budget
// running out on a slow provider) is a fallback for an event nobody is
// owed: the first answer was written as if nothing had been said, and
// the spoken line already carried the event. On an owed event the first
// answer is the finding the user waits for, and stands.
func TestFailedRecheck(t *testing.T) {
	t.Run("block: model error falls back", func(t *testing.T) {
		m := braintest.NewModel(braintest.Step{Text: recheckLine}, braintest.Step{Err: errors.New("503 unavailable")})
		h := newHarness(t, m, nil)
		if rep, err := h.brain.Turn(context.Background(), announceReq(spokenDuring())); !errors.Is(err, ErrFallback) {
			t.Errorf("reply = %+v, err = %v; want a fallback", rep, err)
		}
		if ev := h.log.ofType("brain.recheck_failed"); len(ev) != 1 || payload(ev[0])["kept_first"] != false {
			t.Errorf("brain.recheck_failed = %v", ev)
		}
	})
	t.Run("block: the budget runs out", func(t *testing.T) {
		m := braintest.NewModel(braintest.Step{Text: recheckLine}, braintest.Step{FnCtx: func(ctx context.Context, _ braintest.Call) (*genai.GenerateContentResponse, error) {
			<-ctx.Done() // a Vertex step slower than what is left of 6 + 3 s
			return nil, ctx.Err()
		}})
		h := newHarness(t, m, func(c *Config) { c.AnnounceTurnTimeout, c.FinalGrace = 50*time.Millisecond, 50*time.Millisecond })
		if rep, err := h.brain.Turn(context.Background(), announceReq(spokenDuring())); !errors.Is(err, ErrFallback) {
			t.Errorf("reply = %+v, err = %v; want a fallback", rep, err)
		}
	})
	t.Run("owed: the first answer stands", func(t *testing.T) {
		m := braintest.NewModel(braintest.Step{Text: recheckLine}, braintest.Step{Err: errors.New("503 unavailable")})
		h := newHarness(t, m, nil)
		req := announceReq(spokenDuring())
		req.Owed = true
		rep, err := h.brain.Turn(context.Background(), req)
		if err != nil || rep.Spoken != "auth wants to run the diff. Approve?" || !rep.NeedsUser {
			t.Errorf("reply = %+v, err = %v", rep, err)
		}
		if ev := h.log.ofType("brain.recheck_failed"); len(ev) != 1 || payload(ev[0])["kept_first"] != true {
			t.Errorf("brain.recheck_failed = %v", ev)
		}
	})
}
