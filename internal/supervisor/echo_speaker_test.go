package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// echo-instruct r1 (docs/bench/tier2-2026-10-03.md #3): on speakerphone
// the voice model transcribed its own leaked speech, glued and without
// spaces, and the daemon relayed it. Each echo input is dropped, never
// reaches the brain, and leaves the input ledger.
func TestSpeakerEchoIsDropped(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Sent it to bench-claude.", "")})
	m.Default = &braintest.Step{Text: braintest.Reply("should not run", "")}
	h := newBrainHarness(t, m, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateIdle)

	h.say("Tell Bench Claude to run the tests and let me know when it's done.")
	h.sup.replies.Wait()
	calls := len(m.Calls())

	// The voice says the reply; its own speech comes back while it plays.
	for _, f := range []string{"Sent it. ", "I'm waiting ", "for bench-claude, ", "I'll let you know ", "when it's done."} {
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: f})
	}
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Sent it.I'mwaiting"})
	h.sup.OnVoiceEvent(voice.EventInterrupted{})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	waitFor(t, func() bool { return h.log.count("supervisor.relay_by_daemon") == 1 }, "the daemon's relay")
	h.sup.replies.Wait()

	// The voice answers itself ("I'm standing by —"), and that echoes too.
	h.clock.Advance(500 * time.Millisecond)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "I'm standing by until everything is done."})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "I'll let youTheeverything is"})
	h.relay("I'll let you know")
	h.sup.replies.Wait()

	if n := h.log.count("supervisor.echo_dropped"); n != 2 {
		t.Errorf("echo_dropped = %d, want 2: %v", n, h.log.ofType("supervisor.echo_dropped"))
	}
	if n := len(m.Calls()); n != calls {
		t.Errorf("the echo reached the brain: %d model calls after the instruction", n-calls)
	}
	h.sup.mu.Lock()
	for _, in := range h.sup.inputs {
		if in.saidBefore != "" {
			t.Errorf("an echo stayed in the ledger: %q", in.text)
		}
	}
	h.sup.mu.Unlock()
}

// The shepherd's words glued to the echo are kept; a barge-in whose
// words differ from what was played goes through whole; Chinese echo is
// caught letter by letter.
func TestSpeakerEchoGuardCases(t *testing.T) {
	said := "I'm standing by until everything is done. Bench-claude is running the tests now."
	cases := []struct {
		name  string
		said  string
		heard string
		echo  bool
		kept  string // "" with echo: dropped whole
	}{
		{"glued echo then a question", said, "I'm standing by until everythingis anything waiting for me?", true, "anything waiting for me"},
		{"a question then the echo", said, "Is anything waiting for me? Bench-claude is running the tests", true, "Is anything waiting for me"},
		{"barge-in", said, "Stop, stop. Is anything waiting for me?", false, ""},
		{"an instruction naming the agent", said, "Tell bench-claude to also update the docs.", false, ""},
		{"a whole echo", said, "Bench Claude is running the tests now", true, ""},
		{"chinese echo", "好的，已经让 bench-claude 跑测试了，跑完告诉你。", "已经让bench claude跑测试了", true, ""},
		{"chinese barge-in", "好的，已经让 bench-claude 跑测试了，跑完告诉你。", "等一下，先别跑。", false, ""},
		{"bare no", said, "No.", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel()
			m.Default = &braintest.Step{Text: braintest.Reply("ok.", "")}
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateIdle)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: tc.said})
			h.clock.Advance(300 * time.Millisecond)
			h.hear(tc.heard)
			h.relay(tc.heard)
			ev := h.log.ofType("supervisor.echo_dropped")
			if got := len(ev) == 1; got != tc.echo {
				t.Fatalf("echo_dropped = %v, want %v", ev, tc.echo)
			}
			if !tc.echo {
				if tc.heard != "No." && len(m.Calls()) == 0 {
					t.Errorf("the shepherd's words never reached the brain")
				}
				return
			}
			p := payload(ev[0])
			if tc.kept == "" {
				if p["partial"] != nil || len(m.Calls()) != 0 {
					t.Errorf("a whole echo was relayed: %v, %d model calls", p, len(m.Calls()))
				}
				return
			}
			if p["partial"] != true || p["kept"] != tc.kept {
				t.Errorf("echo_dropped = %v, want kept %q", p, tc.kept)
			}
			if text := lastCall(t, m); !strings.Contains(text, `utterance="`+tc.kept+`"`) {
				t.Errorf("model saw:\n%s", text)
			}
		})
	}
}

// Input that began before the voice spoke is never an echo, even when it
// repeats it (the shepherd talked first).
func TestInputBeforeTheVoiceIsNotEcho(t *testing.T) {
	m := braintest.NewModel()
	m.Default = &braintest.Step{Text: braintest.Reply("ok.", "")}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateIdle)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Bench Claude is running the tests now"})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Bench-claude is running the tests now."})
	h.relay("Bench Claude is running the tests now")
	if n := h.log.count("supervisor.echo_dropped"); n != 0 {
		t.Errorf("echo_dropped = %d", n)
	}
}

func TestStripEcho(t *testing.T) {
	said := "Sent it. I'm waiting for bench-claude, I'll let you know when it's done."
	for _, tc := range []struct {
		heard, rest string
		echo        bool
	}{
		{"Sent it.I'mwaiting", "", true},
		{"I'll let youknow when", "", true},
		{"waiting for bench claude", "", true},
		{"what is bench claude doing", "what is bench claude doing", false},
		{"I'm waiting for bench-claude, I'llwhat about the docs agent?", "what about the docs agent", true},
		// echo-barge-in r2 (re-run): the transcript said "On ", the audio
		// "On it".
		{"On it.", "", true},
		{"Checking.", "", true},
		{"On it, and tell docs too.", "On it, and tell docs too.", false},
	} {
		s := said
		if tc.heard == "On it." || tc.heard == "Checking." || strings.HasPrefix(tc.heard, "On it,") {
			s = "On "
		}
		rest, echo := stripEcho(tc.heard, s)
		if echo != tc.echo || rest != tc.rest {
			t.Errorf("stripEcho(%q) = %q, %v; want %q, %v", tc.heard, rest, echo, tc.rest, tc.echo)
		}
	}
}

// echo-barge-in r3: the voice was cutting itself off on its own echo
// ("Shall I have it " / "fix those "), the speaker's "fix those" came
// back 1.5 s after the flush as "Fix the", the voice relayed "Fix the
// two links" and the brain sent bench-claude an instruction nobody gave.
// The echo began while the phone played (the client's detector opened
// then), so it is dropped. The shepherd saying the same words after the
// voice had stopped (the detector opening after playback ended) is an
// answer, and goes through.
func TestEchoOfAnOfferVersusAcceptingIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		onsetAt time.Duration // after the flush; negative: during playback
		heard   string
		echo    bool
	}{
		{"echo begun during playback", -200 * time.Millisecond, "Fix the", true},
		{"echo, two edits off", -200 * time.Millisecond, "Fix the two links.", true},
		{"the shepherd accepts after playback", 900 * time.Millisecond, "Fix those two links.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel()
			m.Default = &braintest.Step{Text: braintest.Reply("ok.", "")}
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateIdle)
			for _, run := range []string{"Shall I have it ", "fix those two links? "} {
				h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: run})
				h.sup.OnVoiceEvent(voice.EventInterrupted{})
				h.sup.OnVoiceEvent(voice.EventTurnComplete{})
				h.clock.Advance(1200 * time.Millisecond)
			}
			flushed := h.clock.Now().Add(-1100 * time.Millisecond)
			h.pusher.setPlayback(voice.PlaybackReport{State: voice.PlaybackFlushed, At: flushed})
			h.pusher.setOnset(flushed.Add(tc.onsetAt))
			h.clock.Advance(400 * time.Millisecond)
			h.hear(tc.heard)
			h.relay(tc.heard)
			if got := h.log.count("supervisor.echo_dropped") == 1; got != tc.echo {
				t.Errorf("echo_dropped = %v, want %v", h.log.ofType("supervisor.echo_dropped"), tc.echo)
			}
			if !tc.echo && len(m.Calls()) == 0 {
				t.Error("the shepherd's answer never reached the brain")
			}
		})
	}
}

// The shepherd's "yes" to an approval while the push still plays, then
// the push's own echo as a second input, both claimed by the daemon's
// relay: the echo is dropped and the "yes" answers the prompt by the
// fast path — the rail reads the "yes", not the dropped echo.
func TestYesThenEchoOfThePushAnswersByFastPath(t *testing.T) {
	h := newHarness(t, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	push := "auth wants to create hello.txt in the workspace. Approve it?"
	h.sup.OnVoiceEvent(voice.EventPush{Text: push, Urgency: voice.UrgencyIdle})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "auth wants to create "})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes."})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "hello.txt in the workspace. Approve it?"})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "hello.txt in the workspace"})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	waitFor(t, func() bool { return h.log.count("supervisor.relay_by_daemon") == 1 }, "the daemon's relay")
	h.sup.replies.Wait()
	if n := h.log.count("supervisor.echo_dropped"); n != 1 {
		t.Errorf("echo_dropped = %d, want 1", n)
	}
	if n := h.log.count("brain.rail_blocked"); n != 0 {
		t.Errorf("rail_blocked = %v", h.log.ofType("brain.rail_blocked"))
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "y"} {
		t.Errorf("keys = %v, want y by the fast path", got)
	}
}
