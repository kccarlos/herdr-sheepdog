package supervisor

import (
	"context"
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

// gitPushPromptScreen is a Codex approval screen for git push origin main.
const gitPushPromptScreen = `• Running git push origin main

  Would you like to run the following command?

  Environment: local

  Reason: Push commits to origin main

  $ git push origin main

› 1. Yes, proceed (y)
  2. Yes, and don't ask again for commands that start with ` + "`git push`" + ` (p)
  3. No, and tell Codex what to do differently (esc)

  Press enter to confirm or esc to cancel`

type noReportPusher struct{ *fakePusher }

func (p *noReportPusher) PlaybackReports() bool { return false }

// 1. A question becomes told only after confirmed playback (EventPlayed),
// never on EventPush, EventTranscript, EventTurnComplete, or ToSupervisor return.
func TestRailV2ToldOnlyAfterConfirmedPlayed(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()

	// Push sent, transcribed, and turn-completed, but client has NOT drained it.
	h.sup.OnVoiceEvent(voice.EventPush{Text: ap.SpokenSummary, Urgency: voice.UrgencyIdle, PendingID: ap.ID})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: ap.SpokenSummary, Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})

	// Inline ToSupervisor reply also returned, still unplayed.
	m.Add(braintest.Step{Text: braintest.Reply(ap.SpokenSummary+" Should it go ahead?", "")})
	res := h.say("What is waiting?")
	if !strings.Contains(res.Spoken, ap.SpokenSummary) {
		t.Fatalf("reply = %q", res.Spoken)
	}

	h.clock.Advance(time.Second)
	reask := h.say("Yes.")
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("bare yes before EventPlayed sent %d keys", n)
	}
	if !strings.Contains(reask.Spoken, "hadn't finished reading") {
		t.Fatalf("reask = %q, want untold re-ask", reask.Spoken)
	}

	// Once the client reports playback drained, the next bare yes succeeds.
	h.playText(reask.Spoken)
	h.clock.Advance(time.Second)
	h.say("Yes.")
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys after EventPlayed = %d, want 1", n)
	}
}

// 2. Cut before the prompt text leaves it untold; cut after the prompt text marks it told.
func TestRailV2CutBeforeVsAfterPromptText(t *testing.T) {
	h := toldAuth(t)
	h.srv.SetScreen(docsPane, gitPushPromptScreen)
	h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
	dp, _ := h.pending()

	// Cut before docs' prompt text: EventPlayed carries only the heard prefix.
	h.playText("Auth is still waiting on printf.")
	h.clock.Advance(500 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("cut before docs' prompt text sent %d keys", n)
	}

	// Cut after docs' prompt text played: EventPlayed carries dp.SpokenSummary.
	h.playText(dp.SpokenSummary)
	h.clock.Advance(500 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("cut after docs' prompt text sent %d keys, want 1", n)
	}
}

// 3 & 4. Model paraphrase, omission, or status prose ("Docs needs more tests for git push")
// without played key content for git push origin main leaves the question untold.
func TestRailV2ParaphraseOmissionAndStatusProseDoNotTell(t *testing.T) {
	for _, played := range []string{
		"Docs wants your permission to run a command.",
		"Docs asks whether to push your commits.",
		"Docs needs more tests for git push.",
		"Docs finished updating git documentation, and auth asks to run printf.",
		"auth-v2 asks to run git push origin main.",
	} {
		t.Run(played, func(t *testing.T) {
			m := braintest.NewModel()
			h := newBrainHarness(t, m)
			h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
			h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
			h.addAgent(shopPane, "auth-v2", "codex", herdr.StateWorking)

			h.block(authPane, "codex-approval-prompt.screen.txt")
			ap, _ := h.pending()
			h.playText(ap.SpokenSummary)
			h.clock.Advance(time.Second)

			h.srv.SetScreen(docsPane, gitPushPromptScreen)
			h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
			h.playText(played)
			h.clock.Advance(500 * time.Millisecond)

			m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
			m.Add(braintest.Step{Text: braintest.Reply("Holding off.", "")})
			h.say("Yes.")
			if n := h.keysTo(docsPane); n != 0 {
				t.Fatalf("played %q falsely marked docs told (%d keys)", played, n)
			}
		})
	}
}

// 5. told after speech began (acoustic onset before EventPlayed.At) blocks bare answer.
func TestRailV2ToldAfterSpeechOnsetBlocksBareAnswer(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()

	// User starts speaking at t0; playback drains at t0 + 150ms; ASR arrives at t0 + 300ms.
	h.pusher.mu.Lock()
	h.pusher.onset = h.clock.Now()
	h.pusher.mu.Unlock()
	h.clock.Advance(150 * time.Millisecond)
	h.playText(ap.SpokenSummary)
	h.clock.Advance(150 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("bare yes whose speech began before EventPlayed sent %d keys", n)
	}
}

// 10 & 11. No-report client (PlaybackReports() == false): bare yes never sends keys,
// re-ask tells user to name the agent or tap (EN & ZH), naming works, and tap works
// (including right after a daemon restart when told is empty).
func TestRailV2NoReportClientAndRestartFailClosedWithTap(t *testing.T) {
	h := newHarness(t)
	h.sup.pusher = &noReportPusher{fakePusher: h.pusher}
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")

	res := h.say("Yes.")
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("no-report client sent %d keys on bare yes", n)
	}
	if !strings.Contains(res.Spoken, "can't confirm audio playback") || !strings.Contains(res.Spoken, "Say 'yes, auth'") {
		t.Fatalf("EN no-report reask = %q", res.Spoken)
	}

	// Chinese user hears the Chinese no-report instruction.
	h.clock.Advance(time.Second)
	h.hear("好的")
	p, _ := h.pending()
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, p.YesKeys); err == nil {
		t.Fatal("expected refusal on ZH bare yes without playback reports")
	}
	if got := h.lastPushText(); !strings.Contains(got, "不支持播放确认") || !strings.Contains(got, "批准auth") {
		t.Fatalf("ZH no-report reask = %q", got)
	}

	// Naming the agent authorises on the no-report client.
	h.clock.Advance(time.Second)
	h.hear("Yes, approve auth.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, p.YesKeys); err != nil {
		t.Fatalf("naming agent on no-report client failed: %v", err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to auth = %d, want 1", n)
	}
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)

	// Restart starts with empty told: bare yes fails closed, AnswerTap works.
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.speak(blockDocs(t, h))
	store2, err := state.Open(h.store.Path(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{
		PendingTTL: 120 * time.Second, Now: h.clock.Now, DataDir: h.dir,
		inputGrace: -1, relayAfter: -1, interruptCheck: 40 * time.Millisecond, bareAnswers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sup2.shutdown()
	sup2.SetVoiceConnected(true)

	sup2.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes.", Final: true})
	if _, err := sup2.Handler()(context.Background(), voice.ToolCall{ID: "c1", Name: ToolToSupervisor, Args: map[string]any{"text": "Yes."}}); err != nil {
		t.Fatal(err)
	}
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("post-restart bare yes sent %d keys with no persisted told", n)
	}
	dp, _ := store2.Pending()
	if _, err := sup2.AnswerTap(context.Background(), dp.ID, 1); err != nil {
		t.Fatalf("AnswerTap after restart: %v", err)
	}
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs after tap = %d, want 1", n)
	}
}

// Revived same-pane replacement re-ask resolves and does not overwrite Pending on refusal.
func TestS3Finding4RevivedDisplacedReaskIDResolves(t *testing.T) {
	h := toldAuth(t)
	h.speak(blockDocs(t, h))
	docsPending, _ := h.pending()

	h.srv.SetScreen(authPane, fixture(t, "claude-permission-prompt.screen.txt"))
	h.clock.Advance(500 * time.Millisecond)

	from := len(h.pusher.all())
	h.hear("Yes, auth can go ahead.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, []string{"1"}); err == nil {
		t.Fatal("first AnswerPrompt to auth's unheard replacement prompt succeeded")
	}
	if after, _ := h.pending(); after.ID != docsPending.ID {
		t.Fatalf("refused revival changed pending from %s to %s", docsPending.ID, after.ID)
	}

	line, ok := h.askedAgain(from, "auth")
	if !ok {
		t.Fatalf("no re-ask pushed for auth; pushes = %v", h.pushTexts()[from:])
	}
	h.speak(line)

	h.clock.Advance(time.Second)
	h.hear("Yes, auth can go ahead.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, []string{"1"}); err != nil {
		t.Fatalf("second AnswerPrompt after playing A2's re-ask failed: %v", err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to auth = %d, want 1", n)
	}
}

// TypedTurn returns a synchronous re-ask in TypedReply.Text and records it as told.
func TestS3Finding5TypedRefusalReturnsReaskInTypedReply(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)

	h.block(authPane, "codex-approval-prompt.screen.txt")
	a1, _ := h.pending()
	m.Add(braintest.Step{Text: braintest.Reply(a1.SpokenSummary+" Should it go ahead?", "")})
	h.sup.ShepherdInput(InputTyped, "what is waiting?")
	if _, err := h.sup.TypedTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Second)

	h.srv.SetScreen(authPane, "working")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.clock.Advance(500 * time.Millisecond)
	h.block(authPane, "claude-permission-prompt.screen.txt")
	h.clock.Advance(500 * time.Millisecond)

	from := len(h.pusher.all())
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"1"}})}})
	m.Add(braintest.Step{Text: braintest.Reply("Holding off.", "")})
	h.sup.ShepherdInput(InputTyped, "yes, approve auth")
	reply1, err := h.sup.TypedTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("first typed turn sent %d key presses to unheard prompt", n)
	}
	if !strings.Contains(reply1.Text, "did not send that") || !strings.Contains(reply1.Text, "claude.txt") {
		t.Fatalf("TypedReply.Text = %q, want synchronous re-ask stating why and claude.txt", reply1.Text)
	}
	for i := from; i < len(h.pusher.all()); i++ {
		if p := h.pusher.all()[i]; p.Urgency != voice.UrgencySilent {
			t.Fatalf("TypedTurn queued spoken push %+v instead of keeping typed delivery text-only", p)
		}
	}

	h.clock.Advance(time.Second)
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"1"}})}})
	m.Add(braintest.Step{Text: braintest.Reply("Approved auth.", "")})
	h.sup.ShepherdInput(InputTyped, "yes, approve auth")
	if _, err := h.sup.TypedTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("second typed turn after synchronous typed re-ask sent %d key presses, want 1", n)
	}
}
