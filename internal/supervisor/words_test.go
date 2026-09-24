package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The shepherd's words (docs/iteration-2.md WP3): every utterance runs
// on what the daemon recorded, not on the voice model's argument; an
// utterance the voice leaves unanswered is relayed by the daemon; and
// back-to-back utterances queued behind a running turn are one turn.

// relayAfter turns the daemon's relay of an unanswered utterance on
// (the harness turns it off).
func relayAfter(d time.Duration) func(*Config) {
	return func(c *Config) { c.relayAfter = d }
}

// lastCall is the newest model call's text, for assertions on the
// words the brain was asked about.
func lastCall(t *testing.T, m *braintest.Model) string {
	t.Helper()
	calls := m.Calls()
	if len(calls) == 0 {
		t.Fatal("no model call")
	}
	return calls[len(calls)-1].Text()
}

// Gemini shape: the transcript is the shepherd's sentence, the tool
// argument the model's paraphrase of it; the brain gets the sentence, and
// the argument is logged beside it.
func TestBrainHearsTheRecordedWordsNotTheArgument(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("It is halfway.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "What's the progress of a Discord Copilot so far?"})
	h.relay("What does it say?")

	if text := lastCall(t, m); !strings.Contains(text, `utterance="What's the progress of a Discord Copilot so far?"`) || strings.Contains(text, "What does it say?") {
		t.Errorf("the brain was not asked the shepherd's words:\n%s", text)
	}
	ev := h.log.ofType("supervisor.relay")
	if len(ev) != 1 {
		t.Fatalf("supervisor.relay = %v", ev)
	}
	if p := payload(ev[0]); p["heard"] != "What's the progress of a Discord Copilot so far?" || p["said"] != "What does it say?" || p["call_id"] != "c" {
		t.Errorf("supervisor.relay = %v", p)
	}
}

// GPT-Live shape (2026-09-23 23:53:26): a pause mid-sentence closes the
// input (settle's empty final), so the sentence is two inputs; the
// relay carried only the tail ("sometimes"). The brain gets the whole
// sentence.
func TestSplitSentenceRunsWhole(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("I'll tell it.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "sheepdog", "claude", herdr.StateIdle)

	for _, ev := range []voice.EventTranscript{
		{Dir: voice.DirIn, Text: "Tell the sheepdog claude that the iOS app voice still have lags."},
		{Dir: voice.DirIn, Final: true},
		{Dir: voice.DirIn, Text: "The status switch to mute sometimes"},
		{Dir: voice.DirIn, Final: true},
	} {
		h.sup.OnVoiceEvent(ev)
	}
	h.relay("sometimes")

	want := `utterance="Tell the sheepdog claude that the iOS app voice still have lags. The status switch to mute sometimes"`
	if text := lastCall(t, m); !strings.Contains(text, want) {
		t.Errorf("the brain was not asked the whole sentence:\n%s", text)
	}
	if p := payload(h.log.ofType("supervisor.relay")[0]); p["inputs"] != 2 {
		t.Errorf("supervisor.relay = %v, want 2 inputs", p)
	}
}

// Small talk the voice answered itself is not glued onto the next
// utterance.
func TestAnsweredSmallTalkIsNotGlued(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth is testing.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	h.hear("thanks")
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "You're welcome."})
	h.hear("what's auth doing")
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "On it."}) // said before the call
	h.relay("what is auth doing")

	if text := lastCall(t, m); !strings.Contains(text, `utterance="what's auth doing"`) {
		t.Errorf("the brain was not asked just the question:\n%s", text)
	}
}

// A recorded input with no words (it cannot happen through heard or
// ShepherdInput today) falls back to the argument, logged.
func TestEmptyRecordedInputFallsBackToTheArgument(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth is testing.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.mu.Lock()
	h.sup.addInputLocked(&shepherdInput{kind: InputTyped, text: " "})
	h.sup.mu.Unlock()

	h.relay("how is auth")
	if text := lastCall(t, m); !strings.Contains(text, `utterance="how is auth"`) {
		t.Errorf("the brain was not asked the argument:\n%s", text)
	}
	if n := h.log.count("supervisor.relay_fallback"); n != 1 {
		t.Errorf("relay_fallback = %d, want 1", n)
	}
}

// A bare stop the shepherd said is a stop, whatever the voice relayed.
func TestHeardStopIsAStopWhateverTheRelay(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.hear("stop")
	h.relay("stop the tests")
	if ev := h.log.ofType("supervisor.stopped"); len(ev) != 1 || payload(ev[0])["trigger"] != StopTriggerBareWord {
		t.Errorf("supervisor.stopped = %v", ev)
	}
}

// The 2026-09-24 00:16:55 question: heard, and then neither a tool call
// nor a word from the voice. The daemon relays it after relayAfter, and
// the reply is pushed.
func TestDaemonRelaysAnUnansweredUtterance(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Discord Copilot is halfway through the permissions work.", "")})
	h := newBrainHarness(t, m, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "discord", "claude", herdr.StateWorking)

	// Gemini never marks its input transcription final: the watch runs
	// from the last fragment.
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "What's the progress of a Discord Copilot so far?"})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	waitFor(t, func() bool { return h.log.count("supervisor.relay_by_daemon") == 1 }, "the daemon's relay")
	h.sup.replies.Wait()

	if p := payload(h.log.ofType("supervisor.relay_by_daemon")[0]); p["text"] != "What's the progress of a Discord Copilot so far?" || p["waited_ms"] == nil {
		t.Errorf("relay_by_daemon = %v", p)
	}
	if text := lastCall(t, m); !strings.Contains(text, `utterance="What's the progress of a Discord Copilot so far?"`) {
		t.Errorf("model saw:\n%s", text)
	}
	pushes, _ := utterancePushes(h)
	if len(pushes) != 1 || !strings.Contains(pushes[0].Text, "halfway through") || pushes[0].Urgency == voice.UrgencySilent {
		t.Errorf("pushes = %v, want the reply spoken", pushes)
	}
	// The words were taken up: a late relay of them by the voice is its
	// own initiative.
	h.relay("What's the progress?")
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 1 {
		t.Errorf("voice_initiative_ignored = %d, want 1", n)
	}
}

// The watch is cancelled by the voice answering itself, by a tool call
// (the voice's relay, its stop tool, or just the call event), and a
// newer input restarts it; a push being spoken is not an answer.
func TestDaemonRelayWatch(t *testing.T) {
	cases := []struct {
		name  string
		after func(h *harness)
		relay bool
	}{
		{"voice answered", func(h *harness) {
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Hi there."})
		}, false},
		{"tool call event", func(h *harness) {
			h.sup.OnVoiceEvent(voice.EventToolCall{Call: voice.ToolCall{ID: "g1", Name: ToolToSupervisor}})
		}, false},
		{"relayed", func(h *harness) { h.relay("hello") }, false},
		{"stop tool", func(h *harness) { h.call(ToolStop, nil) }, false},
		{"push spoken", func(h *harness) {
			h.sup.OnVoiceEvent(voice.EventPush{Text: "Auth finished.", Urgency: voice.UrgencyIdle})
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Auth finished."})
		}, true},
		{"nothing", func(*harness) {}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, relayAfter(40*time.Millisecond))
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "hello"})
			tc.after(h)
			time.Sleep(150 * time.Millisecond)
			h.sup.replies.Wait()
			if got := h.log.count("supervisor.relay_by_daemon") == 1; got != tc.relay {
				t.Errorf("relay_by_daemon = %d, want relayed=%v", h.log.count("supervisor.relay_by_daemon"), tc.relay)
			}
		})
	}
}

// Back-to-back utterances (2026-09-24 00:41:10 / 00:41:12): the second
// arrives while the first's turn is still queued behind a running turn,
// so they run as one turn over both texts; the queued one delivers
// nothing, and the running one is left to finish (its reply stale).
func TestBackToBackUtterancesCoalesce(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(
		braintest.Step{Block: release, Text: braintest.Reply("Docs is idle.", "")},
		braintest.Step{Text: braintest.Reply("Closed it; two remain, both working.", "")},
	)
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)

	assertPending(t, h.say("what about docs"))
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != nil && h.sup.utterance.started
	}, "the first turn to run")
	first := h.sup.utterance

	h.hear("Okay, I closed the UC summary Codex agent.")
	res := h.relay("I closed the codex agent")
	if res.Scheduling != voice.UrgencySilent || !res.Pending {
		t.Fatalf("queued result = %+v, want pending", res)
	}
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != first && h.sup.utterance.inBrain
	}, "the second turn to queue")

	h.hear("Check the remaining.")
	assertPending(t, h.relay("check the rest"))
	close(release)
	h.sup.replies.Wait()

	ev := h.log.ofType("supervisor.utterance_coalesced")
	if len(ev) != 1 || payload(ev[0])["n"] != 2 {
		t.Fatalf("utterance_coalesced = %v", ev)
	}
	calls := m.Calls()
	var ceo []string
	for _, c := range calls {
		if !c.IsEvent() {
			ceo = append(ceo, c.Text())
		}
	}
	if len(ceo) != 2 {
		t.Fatalf("%d utterance model calls, want 2 (the running turn and one merged turn)", len(ceo))
	}
	if !strings.Contains(ceo[1], `utterance="Okay, I closed the UC summary Codex agent. Check the remaining."`) {
		t.Errorf("merged turn saw:\n%s", ceo[1])
	}
	pushes, _ := utterancePushes(h)
	if len(pushes) != 1 || !strings.Contains(strings.ToLower(pushes[0].Text), "two remain") {
		t.Errorf("pushes = %v, want only the merged reply (the first is stale, the queued one coalesced)", pushes)
	}
	if n := h.log.count("brain.reply_stale"); n != 1 {
		t.Errorf("reply_stale = %d, want 1 (the running turn)", n)
	}
}

// A stop still stops a relayed turn in flight (the slot carries the
// recorded words).
func TestStopWithdrawsTheRecordedWords(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("never", "")})
	m.Default = &braintest.Step{Text: braintest.Reply("Withdrew it.", "")}
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	h.hear("tell auth to add a readme please")
	assertPending(t, h.relay("add readme"))
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != nil && h.sup.utterance.started
	}, "the turn to run")
	h.call(ToolStop, nil)
	h.sup.replies.Wait()
	ev := h.log.ofType("supervisor.stopped")
	if len(ev) != 1 || !strings.Contains(payload(ev[0])["what"].(string), "tell auth to add a readme please") {
		t.Errorf("supervisor.stopped = %v", ev)
	}
}

// Words that follow the daemon's relay of an open input are a new input
// the voice can still relay, not a tail folded into a turn already run.
func TestWordsAfterTheDaemonRelayAreFresh(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Text: braintest.Reply("Auth is testing.", "")},
		braintest.Step{Text: braintest.Reply("Docs is idle.", "")},
	)
	h := newBrainHarness(t, m, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "how is auth"})
	waitFor(t, func() bool { return h.log.count("supervisor.relay_by_daemon") == 1 }, "the daemon's relay")
	h.sup.replies.Wait()

	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: " and what about docs"})
	h.relay("what about docs")
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 0 {
		t.Fatalf("the later words were ignored (%d)", n)
	}
	if text := lastCall(t, m); !strings.Contains(text, `utterance="and what about docs"`) {
		t.Errorf("model saw:\n%s", text)
	}
}

// A barge-in ends a push: what the voice says next answers the shepherd.
func TestBargeInEndsThePushWindow(t *testing.T) {
	h := newHarness(t, relayAfter(40*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventPush{Text: "Auth finished.", Urgency: voice.UrgencyIdle})
	h.sup.OnVoiceEvent(voice.EventInterrupted{})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "thanks"})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "You're welcome."})
	time.Sleep(150 * time.Millisecond)
	h.sup.replies.Wait()
	if n := h.log.count("supervisor.relay_by_daemon"); n != 0 {
		t.Errorf("relay_by_daemon = %d, want 0: the voice answered", n)
	}
}
