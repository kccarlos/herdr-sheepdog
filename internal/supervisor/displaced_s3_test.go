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

// Option B whole-run evidence: push ordering, mid-run append, cut vs tolerance,
// agent naming, "force push to main" + Chinese, drainReportLag both sides,
// two agents in one run, and declines obeying the same rail.
func TestRailV2WholeRunEvidence(t *testing.T) {
	forcePushScreen := strings.ReplaceAll(gitPushPromptScreen, "git push origin main", "git push --force origin main")

	t.Run("stale_audio_and_mid_run_append_without_drain", func(t *testing.T) {
		for _, newTurn := range []bool{false, true} {
			h := toldAuth(t)
			var pt voice.PlaybackTracker
			t0 := h.clock.Now()
			pt.Report(t0, voice.PlaybackStarted, 0)
			pt.Sent(t0, 48*1000)
			pt.Said("docs finished earlier.")
			if newTurn {
				pt.TurnComplete()
			}
			h.clock.Advance(200 * time.Millisecond)
			blockDocs(t, h)
			dp, _ := h.pending()
			if !newTurn {
				h.sup.OnVoiceEvent(voice.EventPlayed{Text: "docs asks to run printf.", Start: h.clock.Now(), End: h.clock.Now().Add(time.Second)})
				if h.sup.toldOpenLocked(dp) {
					t.Fatal("played run before pending_id push marked docs told")
				}
			}
			h.sup.OnVoiceEvent(voice.EventPush{Text: dp.SpokenSummary, Urgency: voice.UrgencyInterrupt, PendingID: dp.ID})
			h.clock.Advance(50 * time.Millisecond)
			pt.Sent(h.clock.Now(), 48*1000)
			pt.Said(" docs asks to run printf.")
			pt.TurnComplete()
			h.clock.Advance(2 * time.Second)
			for _, ev := range pt.Report(h.clock.Now(), voice.PlaybackDrained, 2000).Played {
				h.sup.OnVoiceEvent(ev)
			}
			h.clock.Advance(500 * time.Millisecond)
			h.say("Yes.")
			want := 0
			if newTurn {
				want = 1
			}
			if n := h.keysTo(docsPane); n != want {
				t.Fatalf("newTurn=%v: keys to docs = %d, want %d", newTurn, n, want)
			}
		}
	})

	t.Run("cut_tolerance_unnamed_force_push_chinese_and_decline", func(t *testing.T) {
		flushRun := func(h *harness, text string, playedMs int) {
			var pt voice.PlaybackTracker
			t0 := h.clock.Now()
			pt.Sent(t0, 48*2000)
			pt.Said(text)
			for _, ev := range pt.Report(t0.Add(time.Duration(playedMs)*time.Millisecond), voice.PlaybackFlushed, playedMs).Played {
				h.sup.OnVoiceEvent(ev)
			}
		}
		for _, wording := range []string{"docs wants to force push to main.", "docs请求强制推送到main分支，要批准吗？"} {
			h := newHarness(t)
			h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
			h.addAgent(shopPane, "docs-v2", "codex", herdr.StateWorking)
			h.srv.SetScreen(docsPane, forcePushScreen)
			h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
			dp, _ := h.pending()
			h.sup.OnVoiceEvent(voice.EventPush{Text: dp.SpokenSummary, Urgency: voice.UrgencyIdle, PendingID: dp.ID})

			t0 := h.clock.Now()
			flushRun(h, wording, 1000)
			h.sup.OnVoiceEvent(voice.EventPlayed{Text: "A command needs approval.", Start: t0, End: t0.Add(time.Second)})
			h.sup.OnVoiceEvent(voice.EventPlayed{Text: "docs-v2-codex wants to force push.", Start: t0, End: t0.Add(time.Second)})
			h.clock.Advance(2 * time.Second)
			if h.say("No."); h.keysTo(docsPane) != 0 {
				t.Fatalf("decline on untold question sent %d keys", h.keysTo(docsPane))
			}

			flushRun(h, wording, 1900)
			h.clock.Advance(2 * time.Second)
			h.say("No.")
			if got := h.sendKeysRequests(); len(got) != 1 || got[0][1] != "esc" {
				t.Fatalf("decline after told (%q) = %v, want esc", wording, got)
			}
		}
	})

	t.Run("drain_report_lag_both_sides_and_two_agents_in_one_run", func(t *testing.T) {
		for _, tc := range []struct {
			lag      time.Duration
			wantKeys int
		}{{350 * time.Millisecond, 0}, {250 * time.Millisecond, 1}} {
			h := newHarness(t)
			h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
			h.block(authPane, "codex-approval-prompt.screen.txt")
			ap, _ := h.pending()
			t0 := h.clock.Now()
			h.sup.OnVoiceEvent(voice.EventPush{Text: ap.SpokenSummary, Urgency: voice.UrgencyIdle, PendingID: ap.ID})
			h.clock.Advance(2300*time.Millisecond - tc.lag)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes.", Final: true})
			h.clock.Advance(tc.lag)
			h.sup.OnVoiceEvent(voice.EventPlayed{Text: ap.SpokenSummary, Start: t0.Add(100 * time.Millisecond), End: t0.Add(2300 * time.Millisecond)})
			h.relay("Yes.")
			if got := h.keysTo(authPane); got != tc.wantKeys {
				t.Fatalf("lag=%v: keys=%d, want %d", tc.lag, got, tc.wantKeys)
			}
		}

		// Two agents named in one run: it tells only the newest hand-over
		// (docs); auth needs naming or a re-ask.
		h := newHarness(t)
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, _ := h.store.Question(firstQuestionOf(h, authPane))
		blockDocs(t, h)
		dp, _ := h.pending()
		t0 := h.clock.Now()
		h.sup.OnVoiceEvent(voice.EventPush{Text: ap.SpokenSummary, Urgency: voice.UrgencyIdle, PendingID: ap.ID})
		h.sup.OnVoiceEvent(voice.EventPush{Text: dp.SpokenSummary, Urgency: voice.UrgencyIdle, PendingID: dp.ID})
		h.sup.OnVoiceEvent(voice.EventPlayed{Text: "Both auth and docs need approval.", Start: t0, End: t0.Add(time.Second)})
		if h.sup.toldOpenLocked(ap) || !h.sup.toldOpenLocked(dp) {
			t.Fatal("run naming both agents did not tell only docs, the newest hand-over")
		}
		h.clock.Advance(2 * time.Second)
		h.hear("Yes.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err == nil {
			t.Fatal("bare yes after two-agent run approved auth")
		}
		h.clock.Advance(time.Second)
		h.hear("Yes, approve auth.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err != nil {
			t.Fatalf("naming auth after two-agent run: %v", err)
		}
	})
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
