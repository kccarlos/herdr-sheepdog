package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The one rail reads the shepherd's real input (inputs.go): these tests
// drive the voice model's to_supervisor call (relay) apart from what the
// shepherd said (hear), typed or tapped.

// A relayed "yes" with no transcript behind it is the voice model's own
// initiative: nothing is sent and nothing is said.
func TestRelayWithoutShepherdInputIsIgnored(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	res := h.relay("yes")
	if !noFreshWords(res) {
		t.Errorf("result = %+v, want the silent no-fresh-words fact", res)
	}
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent: %v", got)
	}
	if _, has := h.pending(); !has {
		t.Error("the prompt must stay open")
	}
	ev := h.log.ofType("supervisor.voice_initiative_ignored")
	if len(ev) != 1 || payload(ev[0])["text"] != "yes" {
		t.Errorf("voice_initiative_ignored = %v", ev)
	}
}

// The shepherd's transcript "yes" authorises the fast path.
func TestTranscriptYesAuthorisesTheFastPath(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.clock.Advance(2 * time.Second)
	res := h.say("Yes.")
	if !strings.HasPrefix(res.Spoken, "Told auth yes") {
		t.Errorf("result = %+v", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 {
		t.Errorf("keys = %v", got)
	}
	if h.log.count("brain.rail_blocked") != 0 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
}

// Fragments of one user turn accumulate into one input, and the fast
// path maps what the shepherd said, not the tool argument: the model
// relaying "yes" for a shepherd who said "yes please run it" is not a
// bare answer.
func TestFastPathReadsTheTranscriptNotTheToolArgument(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: " yes please"})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: " run it"})
	h.relay("yes")
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent on a paraphrase: %v", got)
	}
	if h.log.count("supervisor.fast_path") != 0 {
		t.Error("the fast path ran on the tool argument")
	}
}

// A transcript older than the prompt cannot answer it, even when it gets
// through the relay gate (no push in between: there is no voice session
// at all, so the block's own announce push is skipped rather than
// bumping the relay gate past it — audit B3: with a session attached,
// that announce push would reach it and itself reset the gate).
func TestTranscriptOlderThanThePromptIsRefused(t *testing.T) {
	h := newHarness(t)
	h.sup.pusher = nil // no announce push to reset the gate
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.hear("yes")
	h.clock.Advance(time.Second)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.relay("yes")
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent: %v", got)
	}
	ev := h.log.ofType("brain.rail_blocked")
	if len(ev) != 1 || !strings.Contains(payload(ev[0])["reason"].(string), "since auth's prompt appeared") {
		t.Errorf("rail_blocked = %v", ev)
	}
}

// An input past supervisor.authorise_window_s cannot answer.
func TestInputPastTheAuthoriseWindowIsRefused(t *testing.T) {
	h := newBrainHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("I could not.", "")},
	), func(c *Config) { c.AuthoriseWindow = 10 * time.Second })
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	h.hear("go ahead and approve that")
	h.clock.Advance(11 * time.Second)
	h.relay("approve")
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent: %v", got)
	}
	ev := h.log.ofType("brain.rail_blocked")
	if len(ev) != 1 || !strings.Contains(payload(ev[0])["reason"].(string), "authorise_window_s") {
		t.Errorf("rail_blocked = %v", ev)
	}
}

// Typed text is a shepherd input (--voice=off, or the /voice text box).
func TestTypedYesAuthorises(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	h.sup.ShepherdInput(InputTyped, "yes")
	h.relay("yes")
	if got := h.sendKeysRequests(); len(got) != 1 {
		t.Errorf("keys = %v", got)
	}
}

// One "yes" answers one prompt: a second relay of the same words finds
// no new input.
func TestOneYesAnswersOnePrompt(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	h.say("yes")
	h.clock.Advance(time.Second)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.relay("yes")
	if got := h.sendKeysRequests(); len(got) != 1 {
		t.Errorf("keys = %v, want the first answer only", got)
	}
}

// The brain's send_keys runs on the shepherd's input: a "no"-class input
// never authorises a yes key, whatever the model relayed. (A bare "no"
// never reaches the brain: the fast path declines with it; nor does a
// bare "stop", which is the stop procedure — see
// TestHeardStopIsAStopWhateverTheRelay.)
func TestNoInputCannotAuthoriseYesKeys(t *testing.T) {
	for _, said := range []string{"don't run that", "stop it", "cancel it", "No, wait."} {
		t.Run(said, func(t *testing.T) {
			m := braintest.NewModel(
				braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
				braintest.Step{Text: braintest.Reply("I will not.", "")},
			)
			h := newBrainHarness(t, m)
			blockedAuth(t, h)
			h.clock.Advance(time.Second)
			h.hear(said)
			h.relay("yes")
			if got := h.sendKeysRequests(); len(got) != 0 {
				t.Errorf("keys sent: %v", got)
			}
			ev := h.log.ofType("brain.rail_blocked")
			if len(ev) != 1 || !strings.Contains(payload(ev[0])["reason"].(string), "cannot approve") {
				t.Errorf("rail_blocked = %v", ev)
			}
		})
	}
}

// A "no" still answers "no": the rail only stops a "no" from approving.
func TestNoInputCanDecline(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"esc"}})}},
		braintest.Step{Text: braintest.Reply("Told auth no.", "")},
	)
	h := newBrainHarness(t, m)
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	h.hear("no, don't run that")
	h.relay("no")
	if got := h.sendKeysRequests(); len(got) != 1 || got[0][1] != "esc" {
		t.Errorf("keys = %v", got)
	}
}

// A muted client sends no audio, so no transcript arrives: nothing the
// voice model relays can answer a prompt, by fast path or by the brain.
func TestMutedClientAuthorisesNothing(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Told auth yes.", "")},
	)
	h := newBrainHarness(t, m)
	blockedAuth(t, h)
	for _, text := range []string{"yes", "approve it please", "option one"} {
		h.clock.Advance(time.Second)
		h.relay(text)
	}
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent: %v", got)
	}
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 3 {
		t.Errorf("voice_initiative_ignored = %d, want 3", n)
	}
	if n := len(m.Calls()); n != 1 { // the block's announce turn only
		t.Errorf("model calls = %d, want no utterance turn", n)
	}
}

// The regression of 2026-09-23 00:52: the shepherd muted at 00:52:16
// after a question; the block was pushed, the voice model relayed the
// push back, then the brain's late line was pushed and the model said
// "yes" to it on its own. Nothing reaches the pane: nothing was said
// since each push, so the relay gate (voice_initiative_ignored) stops
// all three relays.
func TestRegressionVoiceSaysYesToAPushWhileMuted(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.say("what's everyone doing") // 00:52:1x, then the mic is muted
	h.clock.Advance(18 * time.Second)
	h.block("w5:p1", "codex-approval-prompt.screen.txt") // 00:52:34
	announce := h.pusher.last(t).Text
	h.relay(announce)
	h.clock.Advance(2 * time.Second) // 00:52:36
	h.sup.push("w5:p1", "Auth needs one more yes to run the diff; I'd approve it.", voice.UrgencyIdle)
	h.relay("yes")
	h.sup.push("w5:p1", "Auth wants to run the diff; it looks harmless.", voice.UrgencyIdle)
	h.relay("go ahead")
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Fatalf("keys sent: %v", got)
	}
	if _, has := h.pending(); !has {
		t.Error("the prompt must stay open")
	}
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 3 {
		t.Errorf("voice_initiative_ignored = %d, want 3", n)
	}
}

// An input that arrives after the push but before the relay still
// counts; one that arrives only after the grace wait began is waited
// for.
func TestRelayWaitsForATrailingTranscript(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.inputGrace = 2 * time.Second })
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.hear("yes")
	}()
	res := h.relay("yes")
	if !strings.HasPrefix(res.Spoken, "Told auth yes") {
		t.Errorf("result = %+v", res)
	}
}

// A tap is a shepherd input: its option's keys go through the same
// executor, logged pending.confirmed {via: tap, option}, and the voice
// hears about it silently.
func TestTapAnswersThePendingQuestion(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	p, _ := h.pending()
	h.clock.Advance(time.Second)
	res, err := h.sup.AnswerTap(context.Background(), p.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Spoken, "Told auth ") {
		t.Errorf("result = %+v", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0][1] != strings.Join(p.Options[1].Keys, " ") {
		t.Errorf("keys = %v, want option 2's %v", got, p.Options[1].Keys)
	}
	ev := h.log.ofType("pending.confirmed")
	if len(ev) != 1 || payload(ev[0])["via"] != "tap" || payload(ev[0])["option"] != 2 {
		t.Errorf("pending.confirmed = %v", ev)
	}
	last := h.pusher.last(t)
	if last.Urgency != voice.UrgencySilent || !strings.Contains(last.Text, "The user tapped") || !strings.Contains(last.Text, "auth's question") {
		t.Errorf("last push = %+v", last)
	}
	// The same id again: the question is gone.
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); !errors.Is(err, ErrPendingGone) {
		t.Errorf("second tap err = %v, want ErrPendingGone", err)
	}
}

// A tap is answered at once: the voice relaying the silent "tapped" line
// back finds no fresh input.
func TestTapIsNotFreshInputForTheRelay(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	blockedAuth(t, h)
	p, _ := h.pending()
	calls := len(m.Calls())
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatal(err)
	}
	h.relay(h.pusher.last(t).Text)
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 1 {
		t.Errorf("voice_initiative_ignored = %d, want 1", n)
	}
	if len(m.Calls()) != calls {
		t.Errorf("a brain turn ran on the relayed tap line")
	}
}

// Input transcription that trails the accepted call belongs to it: a
// second, model-initiated relay after it is ignored.
func TestTrailingFragmentIsNotFreshInput(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: " what is"})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: " auth doing"})
	h.relay("what is auth doing")
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: " right now"})
	h.relay("yes")
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 1 {
		t.Errorf("voice_initiative_ignored = %d, want 1", n)
	}
	// The next user turn (after the dog spoke) is fresh again.
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Auth is working."})
	h.hear("thanks")
	h.relay("thanks")
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 1 {
		t.Errorf("voice_initiative_ignored = %d after a new turn, want 1", n)
	}
}

// A prompt answered in the terminal and shown again later appeared then:
// words spoken in between cannot answer it.
func TestPromptSeenAtResetsWhenThePaneLeavesItsPrompt(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking) // answered at the terminal
	h.hear("yes")
	h.clock.Advance(time.Second)
	h.sup.SetVoiceConnected(false) // no announce push between
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.relay("yes")
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent: %v", got)
	}
}

func TestTapRejectsStaleIDsAndBadOptions(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	p, _ := h.pending()
	if _, err := h.sup.AnswerTap(context.Background(), "ap-nope", 1); !errors.Is(err, ErrPendingGone) {
		t.Errorf("unknown id err = %v", err)
	}
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 9); !errors.Is(err, ErrNoSuchOption) {
		t.Errorf("bad option err = %v", err)
	}
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent: %v", got)
	}
}

// A spoken "yes" to a prompt minutes old still is after the prompt
// appeared: the rail measures from when it was first seen (inputs.go's
// promptSinceLocked); within the fast-path window it goes straight to the
// keys.
func TestYesWithinTheFastWindowAuthorises(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.clock.Advance(50 * time.Second)
	h.say("yes")
	if got := h.sendKeysRequests(); len(got) != 1 {
		t.Errorf("keys = %v", got)
	}
	if h.log.count("brain.rail_blocked") != 0 || h.log.count("supervisor.fast_path_skipped") != 0 {
		t.Errorf("rail_blocked=%d fast_path_skipped=%d", h.log.count("brain.rail_blocked"), h.log.count("supervisor.fast_path_skipped"))
	}
}

// The regression of 2026-09-23 06:30: send_keys revived auth's prompt
// (it had never been recorded this run, though the pane had been
// blocked at it for a while) and the rail measured "the prompt
// appeared" from the revival itself, so the shepherd's "yes, please" —
// said before the revival, after the pane actually went blocked — read
// as too old. newAnswerPrompt now keeps the pane's row.StateSince (the
// real blocked-since time) for a prompt recorded for the first time
// while the pane was already blocked, not the moment of recording.
// Sequence: pane blocked at t-30s (never announced this run) -> input at
// t -> revival at t+4s -> must be allowed.
func TestRevivedPromptKeepsWhenThePaneActuallyBlocked(t *testing.T) {
	h := newHarness(t)
	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "auth", Label: "auth"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "w5:p1", WorkspaceID: "auth", TabID: "auth:t1", Agent: "codex", Status: herdr.StateBlocked, Cwd: "/work/w5:p1"})
	h.srv.SetScreen("w5:p1", fixture(t, "codex-approval-prompt.screen.txt"))
	// The pane is already blocked as far as the store is concerned, but
	// nothing went through the Supervisor's onBlocked (a restart with the
	// pane already at this prompt, say): no pending action, no
	// promptSeenAt, this run's first sight of it. state_since is now.
	if _, _, err := h.store.Apply(herdr.Transition{PaneID: "w5:p1", To: herdr.StateBlocked, At: h.clock.Now(), Agent: "codex", WorkspaceID: "auth", WorkspaceLabel: "auth"}); err != nil {
		t.Fatal(err)
	}
	if _, has := h.pending(); has {
		t.Fatal("nothing should be pending yet: onBlocked never ran")
	}

	h.clock.Advance(30 * time.Second) // t: the pane has been blocked 30s
	h.sup.ShepherdInput(InputTyped, "yes please")

	h.clock.Advance(4 * time.Second) // t+4s: send_keys revives the prompt
	res, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "w5:p1", []string{"y"})
	if err != nil {
		t.Fatalf("AnswerPrompt: %v", err)
	}
	if !strings.HasPrefix(res, "Told auth") {
		t.Errorf("result = %q", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0][1] != "y" {
		t.Errorf("keys = %v", got)
	}
	if h.log.count("pending.revived") != 1 {
		t.Errorf("pending.revived = %d", h.log.count("pending.revived"))
	}
	if h.log.count("brain.rail_blocked") != 0 {
		t.Errorf("rail_blocked = %d, want 0", h.log.count("brain.rail_blocked"))
	}
}

func TestNegativeInput(t *testing.T) {
	for text, want := range map[string]bool{
		"no": true, "No.": true, "nope": true, "don't": true, "don't run it": true, "do not do that": true,
		"stop": true, "cancel that": true, "no, wait": true,
		"yes": false, "go ahead": false, "notice that": false, "nobody asked": false, "option two": false, "": false,
	} {
		if got := negativeInput(text); got != want {
			t.Errorf("negativeInput(%q) = %v, want %v", text, got, want)
		}
	}
}
