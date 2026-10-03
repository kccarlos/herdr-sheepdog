package brain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// Every event a turn logs carries its turn_id and the utterance it
// answers; brain.turn_started says how long the turn lock took; an
// announce turn names the worker event it serves; RunningTurn is set
// from brain.turn_started until the turn's brain.turn.
func TestTurnIDsOnEvents(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"})}, Block: gate},
		braintest.Step{Text: braintest.Reply("Auth is asking whether it can create hello.txt.", "")},
		braintest.Step{Text: braintest.Reply("Auth finished.", "")},
	), nil)
	if _, _, ok := h.brain.RunningTurn(); ok {
		t.Fatal("a turn is running before any was asked for")
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "what does auth want?", UtteranceID: "utt-a-1"})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, budget, ok := h.brain.RunningTurn(); ok {
			if budget <= 0 {
				t.Errorf("budget = %v", budget)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunningTurn never reported the turn")
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, ok := h.brain.RunningTurn(); ok {
		t.Error("RunningTurn still set after brain.turn")
	}

	started := h.log.ofType("brain.turn_started")
	if len(started) != 1 {
		t.Fatalf("brain.turn_started ×%d", len(started))
	}
	sp := payload(started[0])
	turnID, _ := sp["turn_id"].(string)
	if !strings.HasPrefix(turnID, "turn-") || !strings.HasSuffix(turnID, "-1") || sp["utterance_id"] != "utt-a-1" {
		t.Errorf("turn_started = %v", sp)
	}
	if _, ok := sp["queue_ms"].(int64); !ok {
		t.Errorf("queue_ms missing: %v", sp)
	}
	for _, typ := range []string{"brain.step", "brain.tool_call", "brain.tool_result", "brain.turn"} {
		evs := h.log.ofType(typ)
		if len(evs) == 0 {
			t.Errorf("no %s", typ)
		}
		for _, e := range evs {
			if p := payload(e); p["turn_id"] != turnID || p["utterance_id"] != "utt-a-1" {
				t.Errorf("%s = %v", typ, p)
			}
		}
	}

	// An announce turn: no utterance, the event id from its link.
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "auth", Event: "done", Link: "evt:evt-x-9"}); err != nil {
		t.Fatal(err)
	}
	turns := h.log.ofType("brain.turn")
	last := payload(turns[len(turns)-1])
	if last["event_id"] != "evt-x-9" || last["utterance_id"] != nil || last["turn_id"] == turnID || last["turn_id"] == nil {
		t.Errorf("announce brain.turn = %v", last)
	}
	started = h.log.ofType("brain.turn_started")
	if p := payload(started[len(started)-1]); p["event_id"] != "evt-x-9" {
		t.Errorf("announce turn_started = %v", p)
	}
}
