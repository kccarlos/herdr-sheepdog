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

// The GPT-Live order of events (audio-e2e, 2026-09-23): the model's
// speech ends (turn complete, which closes the voice input) just after
// the shepherd's "Yes" fragment arrived, and the utterance's end then
// comes as an empty final fragment. The empty fragment must not become
// an input of its own: it made the newest input "" and the fast path
// silently refused the yes.
func TestEmptyFinalFragmentIsNotAnInput(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	blockedAuth(t, h)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: " Yes"})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Final: true})
	res := h.relay("Yes")
	if res.Spoken != "Told auth yes." {
		t.Fatalf("result = %+v", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 {
		t.Errorf("send_keys = %v", got)
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

// (3c) The fast path knows the shepherd's polite phrasings, not just a
// terse "yes": each of these, said whole, answers the one open prompt
// with no model call.
func TestFastPathPoliteYesPhrases(t *testing.T) {
	for _, text := range []string{
		"yes please", "yes, please", "yeah go ahead", "yes continue", "yes, continue",
		"go ahead", "approve", "approve it", "please approve", "do it", "ok", "okay", "sure",
	} {
		t.Run(text, func(t *testing.T) {
			m := braintest.NewModel()
			h := newBrainHarness(t, m)
			blockedAuth(t, h)
			p, _ := h.pending()
			before := len(m.Calls())

			res := h.say(text)
			if !strings.HasPrefix(res.Spoken, "Told auth yes") {
				t.Fatalf("result = %+v", res)
			}
			want := [2]string{"w5:p1", strings.Join(p.YesKeys, " ")}
			if got := h.sendKeysRequests(); len(got) != 1 || got[0] != want {
				t.Errorf("send_keys = %v, want %v", got, want)
			}
			if n := len(m.Calls()) - before; n != 0 {
				t.Errorf("%d model calls, want none on the fast path", n)
			}
		})
	}
}

// (3d) Likewise for a decline: "no", its polite forms, and a bare
// "don't" all reach the No option, never the Yes one.
func TestFastPathPoliteNoPhrases(t *testing.T) {
	for _, text := range []string{"no", "no thanks", "don't"} {
		t.Run(text, func(t *testing.T) {
			m := braintest.NewModel()
			h := newBrainHarness(t, m)
			blockedAuth(t, h)
			p, _ := h.pending()

			res := h.say(text)
			if !strings.HasPrefix(res.Spoken, "Told auth no") {
				t.Fatalf("result = %+v", res)
			}
			want := [2]string{"w5:p1", strings.Join(p.NoKeys, " ")}
			if got := h.sendKeysRequests(); len(got) != 1 || got[0] != want {
				t.Errorf("send_keys = %v, want %v", got, want)
			}
		})
	}
}

// (3e) Option phrases in numeral, word and ordinal form all pick the
// same option a plain "option N" would.
func TestFastPathOptionPhrases(t *testing.T) {
	cases := map[string]int{"option two": 2, "the second one": 2, "first one": 1}
	for text, n := range cases {
		t.Run(text, func(t *testing.T) {
			m := braintest.NewModel()
			h := newBrainHarness(t, m)
			blockedAuth(t, h)
			p, _ := h.pending()
			if n > len(p.Options) {
				t.Fatalf("the fixture has only %d options", len(p.Options))
			}

			res := h.say(text)
			if !strings.HasPrefix(res.Spoken, "Told auth ") {
				t.Fatalf("result = %+v", res)
			}
			want := [2]string{"w5:p1", strings.Join(p.Options[n-1].Keys, " ")}
			if got := h.sendKeysRequests(); len(got) != 1 || got[0] != want {
				t.Errorf("send_keys = %v, want %v (option %d)", got, want, n)
			}
		})
	}
}

// (3f) A phrase with extra content is not a bare answer, however it
// starts: "yes but only the tests" is a hedge, not an approval, and
// "don't approve it yet" is not the bare "don't" — both go to the
// brain, which the fast path never claims for them.
func TestFastPathRejectsPhrasesWithExtraContent(t *testing.T) {
	for _, text := range []string{"yes but only the tests", "don't approve it yet"} {
		t.Run(text, func(t *testing.T) {
			m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Which one?", "")})
			h := newBrainHarness(t, m)
			blockedAuth(t, h)

			res := h.say(text)
			if got := h.sendKeysRequests(); len(got) != 0 {
				t.Errorf("send_keys = %v, want none (the brain decides)", got)
			}
			if h.log.count("supervisor.fast_path") != 0 {
				t.Error("the fast path claimed a phrase with extra content")
			}
			if res.Spoken != "Which one?" {
				t.Errorf("result = %+v", res)
			}
		})
	}
}

// (3b) The voice relaying a push back through to_supervisor never
// reaches the brain as the CEO's words (docs/history/issues-2026-09-18.md Bug 2):
// with nothing said since the push it is the model's own initiative, and
// with something said, the brain gets what was said, not the push.
func TestPushRelayedBackNeverReachesTheBrain(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("It asked to run git status.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	const pushText = "auth asks to run git status --short."
	h.sup.push("w5:p1", pushText, voice.UrgencyIdle)
	before := len(m.Calls())

	if res := h.relay(pushText); res.Scheduling != voice.UrgencySilent || res.Spoken != "" {
		t.Errorf("relay of the push = %+v, want nothing said", res)
	}
	if n := len(m.Calls()) - before; n != 0 {
		t.Errorf("%d model calls, want none for a relayed push", n)
	}
	if h.log.count("supervisor.voice_initiative_ignored") != 1 {
		t.Errorf("voice_initiative_ignored = %d, want 1", h.log.count("supervisor.voice_initiative_ignored"))
	}

	h.hear("what did it say?")
	h.relay(pushText)
	calls := m.Calls()
	if len(calls)-before != 1 {
		t.Fatalf("%d model calls, want one", len(calls)-before)
	}
	if text := calls[len(calls)-1].Text(); !strings.Contains(text, `utterance="what did it say?"`) || strings.Contains(text, `utterance="auth asks`) {
		t.Errorf("the brain was not asked the shepherd's words:\n%s", text)
	}
}

// (3c) A bare short answer said after a push reaches the brain like
// anything else, however the push's own words overlap it (audit B1).
func TestBareAnswerAfterAPushReachesTheBrain(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("heard your no.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	h.sup.push("w5:p1", "I'll deploy it now unless you say otherwise.", voice.UrgencyIdle)
	if res := h.say("no"); res.Spoken != "heard your no." {
		t.Errorf(`"no" after a push containing "now" = %+v`, res)
	}
}

// (4) A bare stop word never reaches a model and never becomes an
// answer: phase 4 leaves the worker's question open.
func TestStopLeavesAnOpenPromptOpen(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	blockedAuth(t, h)

	res := h.say("stop")
	if res.Spoken != "Nothing to stop; auth is still waiting for your answer, so I left its question open." || res.Scheduling != voice.UrgencyInterrupt {
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
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	if res := h.call(ToolStop, nil); res.Spoken != "Nothing was in flight." {
		t.Errorf("stop = %+v", res)
	}
	assertStopped(t, h, stopPhaseNone, StopTriggerTool)
}

// "abort" and "halt" are bare stop words here exactly as they are on
// internal/inject's Router (GPT-Live's Classify path): both now read
// supervisor.BareStopWords, so the same word can no longer stop one
// voice provider and become an ordinary brain turn on the other (audit
// B8 — the router used to carry its own regex with these two words that
// this package's list lacked).
func TestBareStopWordsAgreeWithTheRouter(t *testing.T) {
	for _, word := range []string{"abort", "Halt."} {
		t.Run(word, func(t *testing.T) {
			m := braintest.NewModel()
			h := newBrainHarness(t, m)
			res := h.say(word)
			if res.Scheduling != voice.UrgencyInterrupt || res.Spoken != "Nothing was in flight." {
				t.Fatalf("say(%q) = %+v, want the stop procedure", word, res)
			}
			assertStopped(t, h, stopPhaseNone, StopTriggerBareWord)
			if got := len(m.Calls()); got != 0 {
				t.Errorf("model calls = %d, want 0 (the stop procedure runs no model)", got)
			}
		})
	}
}

// "Never mind" is ordinary speech, not a stop word (docs/iteration-2.md
// J6, eval case never-mind-is-not-stop): on 2026-09-24 00:13:18 it sent
// Ctrl-C to a worker 16 s after the brain gave it a task. It goes to the
// brain like any utterance; the stop procedure does not run and nothing
// is interrupted.
func TestNeverMindIsNotStop(t *testing.T) {
	for _, word := range []string{"never mind", "Never mind.", "nevermind", "Oh, never mind."} {
		t.Run(word, func(t *testing.T) {
			if bareStop(word) {
				t.Fatalf("bareStop(%q) = true", word)
			}
			m := braintest.NewModel(
				braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "review the code"})}},
				braintest.Step{Text: braintest.Reply("Telling auth to review the code. Sent.", "")},
				braintest.Step{Text: braintest.Reply("Okay. Auth carries on with the review.", "")},
			)
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			h.say("tell auth to review the code")
			h.clock.Advance(5 * time.Second)

			res := h.say(word)
			if res.Spoken != "Okay. Auth carries on with the review." {
				t.Errorf("say(%q) = %+v, want the brain's reply", word, res)
			}
			if ev := h.log.ofType("supervisor.stopped"); len(ev) != 0 {
				t.Errorf("the stop procedure ran: %v", ev)
			}
			for _, r := range h.sendKeysRequests() {
				if strings.Contains(r[1], "ctrl+c") {
					t.Errorf("Ctrl-C sent on %q: %v", word, r)
				}
			}
			if got := len(m.Calls()); got != 3 {
				t.Errorf("model calls = %d, want 3 (the instruction turn and an ordinary utterance turn)", got)
			}
		})
	}
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
	if stop.Spoken != `Stopped: your request "tell auth to add a readme" was withdrawn before it acted.` {
		t.Errorf("stop = %+v", stop)
	}
	assertStopped(t, h, stopPhaseCancelTurn, StopTriggerTool)
}

// (7b) A stop that lands after the turn already sent something is not a
// withdrawal: the worker gets the Ctrl-C of phase 3, and the log says so.
func TestStopAfterTheTurnActedInterrupts(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "add a readme"})}},
		braintest.Step{Block: release, Text: braintest.Reply("Telling auth to add a readme. Sent.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	done := make(chan voice.ToolResult, 1)
	go func() { done <- h.say("tell auth to add a readme") }()
	waitFor(t, func() bool { return len(h.promptRequests()) == 1 }, "the instruction to be sent")

	h.call(ToolStop, nil)
	close(release)
	<-done

	assertStopped(t, h, stopPhaseInterrupt, StopTriggerTool)
	ctrlC := 0
	for _, r := range h.sendKeysRequests() {
		if strings.Contains(r[1], "ctrl+c") {
			ctrlC++
		}
	}
	if ctrlC != 1 {
		t.Errorf("%d ctrl+c sends, want 1: %v", ctrlC, h.sendKeysRequests())
	}
}

// (7c) The stop acknowledgement never waits for the brain: on
// 2026-10-02 a Vertex 504 held "Stopped: …" 5.3 s behind the Ctrl-C,
// because a brain turn sat between the stop and its reply. With a model
// that would block forever, stop answers at once with the code sentence,
// records what was stopped as a context line, and calls no model.
func TestStopRepliesWithoutWaitingForTheBrain(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "add a readme"})}},
		braintest.Step{Text: braintest.Reply("Telling auth to add a readme. Sent.", "")},
	)
	m.Default = &braintest.Step{Block: block, Text: braintest.Reply("too late", "")}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if res := h.say("tell auth to add a readme"); !strings.HasSuffix(res.Spoken, "Sent.") {
		t.Fatalf("instruction = %+v", res)
	}
	calls := len(m.Calls())
	h.clock.Advance(5 * time.Second)

	start := time.Now()
	res := h.call(ToolStop, nil)
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Errorf("stop took %v, want a few ms", took)
	}
	if res.Spoken != "Sent an interrupt to auth." || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("stop = %+v", res)
	}
	if got := len(m.Calls()); got != calls {
		t.Errorf("model calls = %d after the stop, want %d (no stop turn)", got, calls)
	}
	assertStopped(t, h, stopPhaseInterrupt, StopTriggerTool)
	found := false
	for _, msg := range h.sup.Brain().Transcript().Messages() {
		if msg.Kind == brain.KindSystem && msg.Content == "the CEO said stop: an interrupt (Ctrl-C) was sent to auth" {
			found = true
		}
	}
	if !found {
		t.Error("the transcript has no context line for the stop")
	}
}

// (7c) A second utterance while one is in flight must not push the
// first one's words to be answered from the roster: the older turn
// keeps the brain, and nothing is sent twice.
func TestSecondUtteranceDoesNotDisplaceTheFirst(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("auth is working.", "")})
	m.Default = &braintest.Step{Text: braintest.Reply("auth is still working.", "")}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	first := make(chan voice.ToolResult, 1)
	go func() { first <- h.say("tell auth to run the tests") }()
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != nil && h.sup.utterance.inBrain
	}, "the first utterance to reach the brain")

	second := make(chan voice.ToolResult, 1)
	go func() { second <- h.say("what's going on") }()
	close(release)
	<-first
	<-second

	if got := h.promptRequests(); len(got) != 0 {
		t.Errorf("something was sent to auth: %v", got)
	}
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

// (9) With the brain off the daemon answers from the roster and sends
// nothing: judging what the words mean is the brain's work, and there
// is no second model to guess with.
func TestUtteranceWithoutABrainReportsStatus(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))

	want := "One agent: auth is working on something. " + noBrainTrailer
	if res := h.say("tell auth to run the tests"); res.Spoken != want || res.Clarify {
		t.Fatalf("instruction = %+v", res)
	}
	if got := h.promptRequests(); len(got) != 0 {
		t.Fatalf("something was sent with no brain: %v", got)
	}
	// Within supervisor.repeat_window_s the second identical roster is
	// flagged unchanged.
	want2 := "No change since I last checked: " + want
	if res := h.say("what is it doing"); res.Spoken != want2 {
		t.Fatalf("question = %+v", res)
	}
	// The agents it named count as dealt with, like a spoken briefing.
	if h.sup.doneKind(agentRow(t, h, "w5:p1")) != pushDoneInvolved {
		t.Error("the roster sentence did not count as a mention")
	}
}

// agentRow reads one row from the state table.
func agentRow(t *testing.T, h *harness, pane string) state.Agent {
	t.Helper()
	row, ok := h.store.Get(pane)
	if !ok {
		t.Fatalf("no agent %s", pane)
	}
	return row
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
