package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// speak is the voice sending a spoken push to the model and finishing
// the turn that read it (the --heard-echo order: push, transcript, turn
// complete).
func (h *harness) speak(text string) {
	h.sup.OnVoiceEvent(voice.EventPush{Text: text, Urgency: voice.UrgencyIdle})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: text, Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
}

// lastPushText is the newest spoken push the supervisor handed out.
func (h *harness) lastPushText() string {
	got := h.pushTexts()
	if len(got) == 0 {
		return ""
	}
	return got[len(got)-1]
}

const staleReply = "I told auth to rerun the shop tests, and it is on it now."

// Tier-3 bench 2026-10-03 #9 (t3-codex-report): Codex's approval push
// was followed four seconds later by the stale reply to the CEO's
// previous words; the CEO waited, the prompt stayed open for three
// minutes and nothing raised it again. Now a prompt buried that way is
// re-announced once, at the next quiet moment, tagged with its event.
func TestBuriedPromptIsRemindedOnce(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.hear("Have auth rerun the shop tests and report.")
	h.clock.Advance(5 * time.Second)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	prompt := h.lastPushText()
	if !strings.Contains(prompt, "auth asks to run") {
		t.Fatalf("prompt push = %q", prompt)
	}
	h.speak(prompt)
	h.clock.Advance(4 * time.Second)
	h.speak(staleReply)

	h.clock.Advance(2 * time.Second)
	h.sup.remindBuriedPrompt()
	if n := h.log.count("supervisor.prompt_reminded"); n != 0 {
		t.Fatalf("reminded before a quiet moment: %d", n)
	}
	h.clock.Advance(promptRemindQuiet)
	h.sup.remindBuriedPrompt()
	want := "Still waiting for your answer: " + prompt
	if got := h.lastPushText(); got != want {
		t.Fatalf("reminder = %q, want %q", got, want)
	}
	ev := h.log.ofType("supervisor.prompt_reminded")
	if len(ev) != 1 || payload(ev[0])["reason"] != "buried" {
		t.Fatalf("supervisor.prompt_reminded = %v", ev)
	}
	tags := h.pusher.tagsOf(len(h.pusher.all()) - 1)
	if p, _ := h.store.Pending(); tags[tagPendingID] != p.ID || tags["prompt_reminder"] != true {
		t.Errorf("reminder tags = %v", tags)
	}
	// Once per prompt, even if the reminder is buried too.
	h.speak(want)
	h.speak("docs finished.")
	h.clock.Advance(time.Minute)
	h.sup.remindBuriedPrompt()
	if n := h.log.count("supervisor.prompt_reminded"); n != 1 {
		t.Errorf("reminders = %d, want 1", n)
	}
}

// No reminder when the CEO spoke after the announcement (they had their
// say), when nothing followed it, or once the prompt was answered.
func TestPromptNotRemindedWhenHeard(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h := newHarness(t)
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		prompt := h.lastPushText()
		h.speak(prompt)
		h.clock.Advance(3 * time.Second)
		return h
	}
	t.Run("nothing followed", func(t *testing.T) {
		h := setup(t)
		h.clock.Advance(time.Minute)
		h.sup.remindBuriedPrompt()
		if n := h.log.count("supervisor.prompt_reminded"); n != 0 {
			t.Errorf("reminders = %d", n)
		}
	})
	t.Run("the CEO spoke first", func(t *testing.T) {
		h := setup(t)
		h.hear("What does it want to run?")
		h.clock.Advance(time.Second)
		h.speak(staleReply)
		h.clock.Advance(time.Minute)
		h.sup.remindBuriedPrompt()
		if n := h.log.count("supervisor.prompt_reminded"); n != 0 {
			t.Errorf("reminders = %d", n)
		}
	})
	t.Run("answered", func(t *testing.T) {
		h := setup(t)
		h.speak(staleReply)
		h.say("yes")
		h.clock.Advance(time.Minute)
		h.sup.remindBuriedPrompt()
		if n := h.log.count("supervisor.prompt_reminded"); n != 0 {
			t.Errorf("reminders = %d", n)
		}
	})
}

// A prompt line the voice judged unheard, and not re-queued (its one
// re-queue was cut too, say), is re-announced the same way.
func TestUnheardPromptIsReminded(t *testing.T) {
	h := newHarness(t, func(c *Config) {})
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	prompt := h.lastPushText()
	h.sup.OnVoiceEvent(voice.EventPush{Text: prompt, Urgency: voice.UrgencyIdle})
	h.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{Text: prompt, Reason: voice.UnheardUserTurn}})
	h.clock.Advance(promptRemindQuiet + time.Second)
	h.sup.remindBuriedPrompt()
	if ev := h.log.ofType("supervisor.prompt_reminded"); len(ev) != 1 || payload(ev[0])["reason"] != "unheard" {
		t.Fatalf("supervisor.prompt_reminded = %v", ev)
	}
}

// The brain's next utterance request carries the fact that the prompt is
// open and likely unheard; a request after the CEO heard it does not.
func TestUtteranceRequestSaysThePromptIsUnheard(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Auth wants to run a command.", "idle", "", "", true)
	m.Default = &braintest.Step{Text: braintest.Reply("Auth is waiting on you to approve go test.", "")}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.sup.Brain().Quiesce()
	prompt := h.lastPushText()
	if prompt == "" {
		t.Fatal("no prompt push")
	}
	h.speak(prompt)
	h.clock.Advance(2 * time.Second)
	h.speak(staleReply)
	h.clock.Advance(20 * time.Second)
	h.say("Anything else going on?")
	calls := m.Calls()
	text := calls[len(calls)-1].Text()
	want := "auth's prompt is still open and the CEO has likely not heard it: "
	if !strings.Contains(text, want) || !strings.Contains(text, "another line was spoken right after it") {
		t.Fatalf("request lacks the open-prompt fact:\n%s", text)
	}
	// They have spoken since: the next request has no such fact.
	h.clock.Advance(5 * time.Second)
	h.say("And docs?")
	calls = m.Calls()
	text = calls[len(calls)-1].Text()
	if last := text[strings.LastIndex(text, `utterance="And docs?"`):]; strings.Contains(last, "likely not heard") {
		t.Errorf("fact repeated after the CEO spoke:\n%s", text)
	}
}
