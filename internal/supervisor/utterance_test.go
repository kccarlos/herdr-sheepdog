package supervisor

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// blockedAuth puts auth at the Codex approval prompt with a pending
// answer_prompt (yes -> 1, no -> esc, two labelled options).
func blockedAuth(t *testing.T, h *harness) {
	t.Helper()
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if p, has := h.pending(); !has || p.Kind != state.PendingAnswerPrompt {
		t.Fatalf("no answer_prompt pending: %+v", p)
	}
}

// (1) to_supervisor runs one utterance turn: the request carries the
// verbatim words, no agent hint and the roster sentence, so the brain
// can answer a status question without a tool call.
func TestToSupervisorRunsUtteranceTurn(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("auth is working on the tests.", "From the roster.")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	res := h.say("what's going on")
	if res.Spoken != "auth is working on the tests." {
		t.Fatalf("result = %+v", res)
	}
	calls := m.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d model calls, want one (a status question costs no tool round)", len(calls))
	}
	text := calls[0].Text()
	for _, want := range []string{
		`Request from the assistant (utterance): utterance="what's going on"`,
		"agent=(none: the CEO did not say a name)",
		"Status as the daemon has it: ",
		"relayed verbatim",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("request lacks %q:\n%s", want, text)
		}
	}
}

// (2) The bare-answer fast path: "yes" with exactly one open prompt
// sends its keys with no model call at all.
func TestFastPathBareYesSendsKeys(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	blockedAuth(t, h)
	p, _ := h.pending()
	before := len(m.Calls())

	res := h.say("Yes.")
	if res.Spoken != "Told auth yes." {
		t.Fatalf("result = %+v", res)
	}
	want := [2]string{"w5:p1", strings.Join(p.YesKeys, " ")}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != want {
		t.Errorf("send_keys = %v, want %v", got, want)
	}
	if n := len(m.Calls()) - before; n != 0 {
		t.Errorf("%d model calls, want none on the fast path", n)
	}
	if ev := h.log.ofType("pending.confirmed"); len(ev) != 1 {
		t.Fatalf("pending.confirmed = %d", len(ev))
	} else if p, _ := ev[0].Payload.(map[string]any); p["via"] != "fast_path" {
		t.Errorf("pending.confirmed via = %v, want fast_path", p["via"])
	}
	if h.log.count("supervisor.fast_path") != 1 {
		t.Error("no supervisor.fast_path event")
	}
	// The brain saw the shepherd's word and what the daemon did with it.
	var seen string
	for _, msg := range h.sup.brain.Transcript().Messages() {
		if msg.Kind == brain.KindSystem {
			seen = msg.Content
		}
	}
	if !strings.Contains(seen, `"Yes."`) || !strings.Contains(seen, "told auth yes") {
		t.Errorf("transcript line = %q", seen)
	}
}

// (3) Everything the fast path does not claim goes to the brain: "yes"
// with nothing pending, a "yes" with more words, and an option label.
func TestFastPathBoundaries(t *testing.T) {
	t.Run("no pending prompt", func(t *testing.T) {
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Yes to what?", "")})
		h := newBrainHarness(t, m)
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		if res := h.say("yes"); res.Spoken != "Yes to what?" {
			t.Fatalf("result = %+v", res)
		}
		if got := h.sendKeysRequests(); len(got) != 0 {
			t.Errorf("send_keys = %v, want none", got)
		}
	})
	t.Run("not a bare answer", func(t *testing.T) {
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Which one?", "")})
		h := newBrainHarness(t, m)
		blockedAuth(t, h)
		if res := h.say("yes please run it"); res.Spoken != "Which one?" {
			t.Fatalf("result = %+v", res)
		}
		if got := h.sendKeysRequests(); len(got) != 0 {
			t.Errorf("send_keys = %v, want none (the brain decides)", got)
		}
		if h.log.count("supervisor.fast_path") != 0 {
			t.Error("the fast path claimed a multi-word utterance")
		}
	})
	t.Run("option label", func(t *testing.T) {
		m := braintest.NewModel()
		h := newBrainHarness(t, m)
		blockedAuth(t, h)
		p, _ := h.pending()
		if len(p.Options) == 0 {
			t.Fatal("the fixture has no options")
		}
		if res := h.say(p.Options[0].Label); !strings.HasPrefix(res.Spoken, "Told auth ") {
			t.Fatalf("result = %+v", res)
		}
		if got := h.sendKeysRequests(); len(got) != 1 {
			t.Errorf("send_keys = %v", got)
		}
	})
	t.Run("expired prompt", func(t *testing.T) {
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Nothing is waiting.", "")})
		h := newBrainHarness(t, m)
		blockedAuth(t, h)
		h.clock.Advance(3 * time.Minute)
		if res := h.say("yes"); res.Spoken != "Nothing is waiting." {
			t.Fatalf("result = %+v", res)
		}
		if got := h.sendKeysRequests(); len(got) != 0 {
			t.Errorf("send_keys = %v, want none past the TTL", got)
		}
	})
}

// (4) A bare stop word never reaches a model and never becomes an
// answer: phase 4 leaves the worker's question open.
func TestStopLeavesAnOpenPromptOpen(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Left auth's question open. Shall I answer it?", "")})
	h := newBrainHarness(t, m)
	blockedAuth(t, h)

	res := h.say("stop")
	if !strings.Contains(res.Spoken, "auth") || res.Scheduling != voice.UrgencyInterrupt {
		t.Fatalf("result = %+v", res)
	}
	if p, has := h.pending(); !has || p.Kind != state.PendingAnswerPrompt {
		t.Fatal("stop dropped the open question; stop is not \"no\"")
	}
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("send_keys = %v, want none", got)
	}
	assertStopped(t, h, stopPhasePromptOpen, StopTriggerBareWord)
}

// (5) The stop() tool runs the same procedure as the bare word, and
// logs which trigger fired.
func TestStopToolRunsTheProcedure(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Nothing was in flight.", "")})
	h := newBrainHarness(t, m)
	h.call(ToolStop, nil)
	assertStopped(t, h, stopPhaseNone, StopTriggerTool)
}

// (6) Phase 3: an instruction that went to a working agent within the
// cancel window is interrupted; past the window it is not.
func TestStopInterruptsARecentInstruction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after time.Duration
		phase string
	}{
		{"within the window", 5 * time.Second, stopPhaseInterrupt},
		{"past the window", 90 * time.Second, stopPhaseNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel(
				braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "add a readme"})}},
				braintest.Step{Text: braintest.Reply("Telling auth to add a readme. Sent.", "")},
				braintest.Step{Text: braintest.Reply("Stopped auth.", "")},
			)
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			if res := h.say("tell auth to add a readme"); !strings.HasSuffix(res.Spoken, "Sent.") {
				t.Fatalf("propose = %+v", res)
			}
			h.clock.Advance(tc.after)

			h.say("stop")
			assertStopped(t, h, tc.phase, StopTriggerBareWord)
			ctrlC := 0
			for _, r := range h.sendKeysRequests() {
				if strings.Contains(r[1], "ctrl+c") {
					ctrlC++
				}
			}
			if tc.phase == stopPhaseInterrupt && ctrlC != 1 {
				t.Errorf("%d ctrl+c sends, want 1: %v", ctrlC, h.sendKeysRequests())
			}
			if tc.phase == stopPhaseNone && ctrlC != 0 {
				t.Errorf("%d ctrl+c sends past the cancel window, want 0", ctrlC)
			}
		})
	}
}

// (7) Phases 1 and 2: a stop while the words are with the brain cancels
// the turn, and — the regression that matters — the words must not then
// fall through to the template path and be sent after all.
func TestStopCancelsTheBrainTurnAndSendsNothing(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{
		Block: release,
		Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "add a readme"})},
	})
	m.Default = &braintest.Step{Text: braintest.Reply("Withdrew your request before anything was sent.", "")}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	done := make(chan voice.ToolResult, 1)
	go func() { done <- h.say("tell auth to add a readme") }()
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != nil && h.sup.utterance.inBrain
	}, "the utterance reached the brain")

	stop := h.call(ToolStop, nil)
	close(release)
	res := <-done

	if len(h.promptRequests()) != 0 {
		t.Errorf("an instruction was sent after the stop: %v", h.promptRequests())
	}
	if res.Scheduling != voice.UrgencySilent {
		t.Errorf("a dropped utterance must not speak: %+v", res)
	}
	if !strings.Contains(stop.Spoken, "Withdrew") {
		t.Errorf("stop = %+v", stop)
	}
	assertStopped(t, h, stopPhaseCancelTurn, StopTriggerTool)
}

// (8) With the brain off the stop procedure speaks its own sentence.
func TestStopSentenceWithoutBrain(t *testing.T) {
	h := newHarness(t)
	if res := h.call(ToolStop, nil); res.Spoken != "Nothing was in flight." {
		t.Errorf("result = %+v", res)
	}
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if res := h.call(ToolStop, nil); !strings.HasPrefix(res.Spoken, "Nothing to stop;") {
		t.Errorf("result = %+v", res)
	}
}

// (9) With the brain off an utterance still reaches the template path,
// which splits it crudely: "tell …" is an instruction, the rest a
// question.
func TestUtteranceFallsBackToTheTemplatePath(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))

	if res := h.say("tell auth to run the tests"); !strings.HasSuffix(res.Spoken, "Sent.") {
		t.Fatalf("instruction = %+v", res)
	}
	if got := h.promptRequests(); len(got) != 1 {
		t.Fatalf("agent.prompt = %v", got)
	}
	if res := h.say("what is it doing"); res.Spoken == "" {
		t.Fatalf("question = %+v", res)
	}
	if h.llm.calls["spoken_answer"] != 1 {
		t.Errorf("spoken_answer calls = %d, want 1", h.llm.calls["spoken_answer"])
	}
}

// (10) The brain's note tool writes through the supervisor's notes
// store, and the note is in the ledger of the next turn.
func TestBrainNoteToolRecordsANote(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolNote, map[string]any{"text": "don't touch the migrations today"})}},
		braintest.Step{Text: braintest.Reply("Noted.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	if res := h.say("remember not to touch the migrations today"); res.Spoken != "Noted." {
		t.Fatalf("result = %+v", res)
	}
	notes := h.store.Snapshot().Session.Notes
	if len(notes) != 1 || notes[0].Text != "don't touch the migrations today" {
		t.Fatalf("notes = %+v", notes)
	}
	// Exactly one transcript line about it: the tool result, not a
	// second ingest from the notes store.
	n := 0
	for _, msg := range h.sup.brain.Transcript().Messages() {
		if msg.Kind == brain.KindNote {
			n++
		}
	}
	if n != 0 {
		t.Errorf("%d note messages, want 0 (the tool result already carries it)", n)
	}
	if h.log.count("note.created") != 1 {
		t.Errorf("note.created = %d", h.log.count("note.created"))
	}
}

// (11) pending travels from the brain's reply to the voice's tool
// result.
func TestPendingIsPlumbedThrough(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: `{"spoken": "Reading the diff, about half a minute.", "detail": "", "needs_user": false, "clarify": false, "pending": true}`})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if res := h.say("why did that fail?"); !res.Pending {
		t.Errorf("result = %+v", res)
	}
}

// (12) The one rail still refuses send_keys without an authorising
// utterance: an announce (event) turn may not answer a prompt.
func TestRailStillRefusesSendKeysOnAnEventTurn(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = &braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"1"}})}}
	h := newBrainHarness(t, m)
	blockedAuth(t, h)

	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Fatalf("the rail let an event turn answer a prompt: %v", got)
	}
	if h.log.count("brain.rail_blocked") == 0 {
		t.Error("no brain.rail_blocked event")
	}
	if p, has := h.pending(); !has || p.Kind != state.PendingAnswerPrompt {
		t.Error("the prompt was answered anyway")
	}
}

// assertStopped checks the one supervisor.stopped event's phase and
// trigger.
func assertStopped(t *testing.T, h *harness, phase, trigger string) {
	t.Helper()
	ev := h.log.ofType("supervisor.stopped")
	if len(ev) != 1 {
		t.Fatalf("%d supervisor.stopped events, want 1", len(ev))
	}
	p, _ := ev[0].Payload.(map[string]any)
	if p["phase"] != phase || p["trigger"] != trigger {
		t.Errorf("supervisor.stopped = %v, want phase %q trigger %q", p, phase, trigger)
	}
	if what, _ := p["what"].(string); what == "" {
		t.Error("supervisor.stopped has no what")
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
