package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// echoSaid is the line the iOS speaker played on 2026-09-24 18:23:50,
// as the voice's output transcript fragments came in.
var echoSaid = []string{"No change, all ", "four are idle", " and nothing is broken.", " The question I asked", " a minute ago still"}

// speak feeds the voice's output transcript fragments, then the model
// interrupting itself on the echo (voice.interrupted by:model).
func speak(h *harness, fragments []string) {
	for _, f := range fragments {
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: f})
	}
	h.sup.OnVoiceEvent(voice.EventInterrupted{})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
}

// TestEchoOfTheAssistantIsNotRelayed is the logged pair: 1.7 s after the
// voice's last output the input transcription was "new change" (the
// ASR's "new" for "no") and the voice relayed it. It is dropped: no
// brain turn, nothing said, supervisor.echo_dropped with both texts.
func TestEchoOfTheAssistantIsNotRelayed(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Still no change.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	speak(h, echoSaid)
	h.clock.Advance(1700 * time.Millisecond)
	res := h.say("new change")
	if res.Spoken != "" || res.Scheduling != voice.UrgencySilent {
		t.Errorf("an echo was answered: %+v", res)
	}
	if n := len(m.Calls()); n != 0 {
		t.Errorf("the echo reached the brain: %d model calls", n)
	}
	ev := h.log.ofType("supervisor.echo_dropped")
	if len(ev) != 1 || payload(ev[0])["heard"] != "new change" || payload(ev[0])["said"] != "No change, all four are idle and nothing is broken. The question I asked a minute ago still" {
		t.Errorf("supervisor.echo_dropped = %v", ev)
	}
}

// TestEchoGuardLetsTheShepherdThrough: the negative cases. A bare "no"
// right after the line is a fast-path word and still answers; words said
// after the window are relayed, even if they start the same; words in
// the window that do not line up with the line are relayed; and the
// voice saying "Checking." between the echo and its tool call does not
// hide the echo.
func TestEchoGuardLetsTheShepherdThrough(t *testing.T) {
	cases := []struct {
		name  string
		after time.Duration
		heard string
		echo  bool
	}{
		{"the logged echo", 1700 * time.Millisecond, "new change", true},
		{"a prefix, heard exactly", 500 * time.Millisecond, "No change, all four", true},
		{"a middle run", time.Second, "four are idle and nothing is broken", true},
		{"a bare no", 500 * time.Millisecond, "no", false},
		{"a bare stop word", 500 * time.Millisecond, "hold on", false},
		{"an instruction five seconds later", 5 * time.Second, "new change to auth please", false},
		{"an instruction in the window", time.Second, "new change to auth please", false},
		{"a question in the window", time.Second, "what changed in auth", false},
		{"the echo, but after the window", 3 * time.Second, "new change", false},
		{"two differing words", time.Second, "new chance", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel(braintest.Step{Text: braintest.Reply("ok.", "")})
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
			speak(h, echoSaid)
			h.clock.Advance(tc.after)
			h.hear(tc.heard)
			// The voice's filler lands before its tool call.
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Checking."})
			h.relay(tc.heard)
			if got := h.log.count("supervisor.echo_dropped") == 1; got != tc.echo {
				t.Errorf("echo_dropped = %v, want %v", got, tc.echo)
			}
		})
	}
}

// TestEchoGuardOnTheDaemonRelay: the daemon's own relay of an unanswered
// utterance (relay.go) drops an echo too.
func TestEchoGuardOnTheDaemonRelay(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("should not run", "")})
	h := newBrainHarness(t, m, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	speak(h, echoSaid)
	h.clock.Advance(time.Second)
	h.hear("new change")
	waitFor(t, func() bool { return h.log.count("supervisor.relay_by_daemon") == 1 }, "the daemon's relay")
	h.sup.replies.Wait()
	if h.log.count("supervisor.echo_dropped") != 1 || len(m.Calls()) != 0 || len(h.pusher.all()) != 0 {
		t.Errorf("echo_dropped = %d, model calls = %d, pushes = %v", h.log.count("supervisor.echo_dropped"), len(m.Calls()), h.pusher.all())
	}
}

func TestEchoOf(t *testing.T) {
	said := "No change, all four are idle and nothing is broken."
	for _, tc := range []struct {
		heard string
		want  bool
	}{
		{"new change", true},
		{"No change.", true},
		{"all four are idle", true},
		{"nothing is broken", true},
		{"change", false}, // one word is never an echo
		{"no", false},
		{"new change to auth please", false},
		{"all four are busy", true}, // one short word differs
		{"all five are running", false},
		{"is broken now then", false},
	} {
		if got := echoOf(tc.heard, said); got != tc.want {
			t.Errorf("echoOf(%q) = %v, want %v", tc.heard, got, tc.want)
		}
	}
}

// TestEchoLeavesTheBrainWindow: a dropped echo is not left in the brain's
// window as the shepherd's words — the next turn must not read `CEO
// said: "new change"` and act on it. Both ways the words reach the brain
// first: a final transcript (ingested at once), and an open one the
// voice's "Checking." flushes before its tool call (Gemini never marks
// its input final). The transcript on disk keeps the words as heard and
// a note that they were an echo.
func TestEchoLeavesTheBrainWindow(t *testing.T) {
	for _, final := range []bool{true, false} {
		t.Run(map[bool]string{true: "final", false: "open"}[final], func(t *testing.T) {
			m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth is idle.", "")})
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
			speak(h, echoSaid)
			h.clock.Advance(1700 * time.Millisecond)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "new change", Final: final})
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Checking."})
			h.sup.OnVoiceEvent(voice.EventTurnComplete{})
			h.relay("new change")
			if h.log.count("supervisor.echo_dropped") != 1 {
				t.Fatalf("echo_dropped = %d", h.log.count("supervisor.echo_dropped"))
			}

			h.clock.Advance(10 * time.Second)
			h.say("what is auth doing?")
			calls := m.Calls()
			if len(calls) != 1 {
				t.Fatalf("model calls = %d", len(calls))
			}
			prompt := calls[0].Text()
			if strings.Contains(prompt, `CEO said: "new change"`) {
				t.Errorf("the echo reached the next turn as the shepherd's words:\n%s", prompt)
			}
			if !strings.Contains(prompt, `CEO said: "what is auth doing?"`) || !strings.Contains(prompt, "Echo ignored") {
				t.Errorf("model saw:\n%s", prompt)
			}
			msgs, err := brain.ReadTranscript(h.sup.Brain().TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			notes := 0
			for _, msg := range msgs {
				if msg.Kind == brain.KindSystem && strings.Contains(msg.Content, `"new change"`) {
					notes++
				}
			}
			if notes != 1 {
				t.Errorf("echo notes in the transcript = %d, want 1", notes)
			}
		})
	}
}

// TestEchoWindowAnchoredToPlaybackEnd is docs/timelines.md C5: the
// window used to open when the voice stopped generating, but Gemini
// generates ~4x real time and the phone plays a long reply's tail
// seconds later, so an echo of that tail could pass. With playback
// reports the window runs from the client's drained (or flushed); while
// the client says it is still playing, the voice counts as speaking.
// Without reports the old anchor holds (TestEchoGuardLetsTheShepherdThrough,
// "the echo, but after the window").
func TestEchoWindowAnchoredToPlaybackEnd(t *testing.T) {
	cases := []struct {
		name     string
		report   string        // "" = the client sends no reports
		reportAt time.Duration // after the voice stopped generating
		after    time.Duration // when the echo is heard
		echo     bool
	}{
		{"drained 3 s after turnComplete, echo 0.5 s after drained", voice.PlaybackDrained, 3 * time.Second, 3500 * time.Millisecond, true},
		{"flushed late, echo right after", voice.PlaybackFlushed, 4 * time.Second, 5 * time.Second, true},
		{"still playing, no drained yet", voice.PlaybackStarted, 100 * time.Millisecond, 6 * time.Second, true},
		{"started before the first transcript fragment", voice.PlaybackStarted, -100 * time.Millisecond, 6 * time.Second, true},
		{"drained, echo past the window after it", voice.PlaybackDrained, time.Second, 4 * time.Second, false},
		{"no reports: turnComplete anchors", "", 0, 3500 * time.Millisecond, false},
		{"a report from before this reply is ignored", voice.PlaybackDrained, -10 * time.Second, 3500 * time.Millisecond, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel(braintest.Step{Text: braintest.Reply("ok.", "")})
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
			speak(h, echoSaid)
			stopped := h.clock.Now()
			if tc.report != "" {
				h.pusher.setPlayback(voice.PlaybackReport{State: tc.report, At: stopped.Add(tc.reportAt)})
			}
			h.clock.Advance(tc.after)
			h.hear("new change")
			h.relay("new change")
			if got := h.log.count("supervisor.echo_dropped") == 1; got != tc.echo {
				t.Errorf("echo_dropped = %v, want %v", got, tc.echo)
			}
		})
	}
}
