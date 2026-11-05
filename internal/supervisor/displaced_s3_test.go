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

// Finding 1a: an inline voice reply (or fast-path re-ask) whose tool call is
// cancelled before the voice speaks it must not leave the question marked told.
func TestS3Finding1CancelledToolResultRevokesInlineTold(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()
	m.Add(braintest.Step{Text: braintest.Reply(ap.SpokenSummary+" Should it go ahead?", "")})
	h.say("What is waiting?")
	h.clock.Advance(time.Second)

	// Docs blocks, displacing auth without being announced yet.
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	dp, ok := h.pending()
	if !ok || dp.PaneID != docsPane {
		t.Fatalf("pending = %+v, want docs", dp)
	}

	// The user asks what is going on; the handler returns an inline reply
	// stating docs' question, then the voice session cancels the tool response
	// before speaking it.
	m.Add(braintest.Step{Text: braintest.Reply(dp.SpokenSummary+" Should it go ahead?", "")})
	h.hear("Status?")
	call := voice.ToolCall{ID: "call-cancel-1", Name: ToolToSupervisor, Args: map[string]any{"text": "Status?"}}
	if _, err := h.sup.Handler()(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	h.sup.OnVoiceEvent(voice.EventToolResult{Call: call, Err: context.Canceled, Cancelled: true})

	// The user never heard docs' question: a bare yes meant for auth must be
	// refused.
	h.clock.Advance(time.Second)
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
	m.Add(braintest.Step{Text: braintest.Reply("I did not send that.", "")})
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("cancelled inline reply left docs marked told: sent %d key presses", n)
	}
}

// Finding 1b: an inline reply or spoken push cut during playback before it
// reaches question B must revoke B's telling, while keeping a question that
// was already stated in the heard prefix.
func TestS3Finding1PlaybackCutRevokesUnheardQuestion(t *testing.T) {
	t.Run("inline_reply_cut_before_question", func(t *testing.T) {
		m := braintest.NewModel()
		h := newBrainHarness(t, m)
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, _ := h.pending()
		m.Add(braintest.Step{Text: braintest.Reply(ap.SpokenSummary+" Should it go ahead?", "")})
		h.say("What is waiting?")
		h.clock.Advance(time.Second)

		h.block(docsPane, "codex-approval-prompt.screen.txt")
		dp, _ := h.pending()

		full := "Auth is still waiting on printf. " + dp.SpokenSummary + " Should it go ahead?"
		m.Add(braintest.Step{Text: braintest.Reply(full, "")})
		res := h.say("Any update?")
		if res.UtteranceID == "" {
			t.Fatalf("missing UtteranceID on inline reply: %+v", res)
		}

		// Client cuts playback before docs' question is reached.
		heardPrefix := "Auth is still waiting on printf."
		h.sup.OnVoiceEvent(voice.EventInterrupted{})
		h.sup.OnVoiceEvent(voice.EventTranscript{
			Dir:   voice.DirOut,
			Text:  heardPrefix,
			Final: true,
			Cut:   &voice.TranscriptCut{Full: res.Spoken, HeardMs: 1200, UtteranceID: res.UtteranceID},
		})

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("I did not send that.", "")})
		h.say("Yes.")
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("cut reply before docs' question still authorised docs: sent %d key presses", n)
		}
	})

	t.Run("push_cut_before_question", func(t *testing.T) {
		h := toldAuth(t)
		docsLine := blockDocs(t, h)
		dp, _ := h.pending()

		h.sup.OnVoiceEvent(voice.EventPush{Text: docsLine, Urgency: voice.UrgencyInterrupt, PendingID: dp.ID})
		h.sup.OnVoiceEvent(voice.EventInterrupted{})
		h.sup.OnVoiceEvent(voice.EventTranscript{
			Dir:   voice.DirOut,
			Text:  "Dropping auth;",
			Final: true,
			Cut:   &voice.TranscriptCut{Full: docsLine, HeardMs: 400},
		})

		h.clock.Advance(time.Second)
		h.say("Yes.")
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("cut push before docs' question still authorised docs: sent %d key presses", n)
		}
	})
}

// Finding 2: a status sentence that names an agent and shares a single word
// ("git") with its unheard approval ("git push origin main") must not mark that
// approval told.
func TestS3Finding2StatusSentenceDoesNotTellUnheardQuestion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"status_plus_other_question", "Docs finished updating git documentation, and auth asks to run printf. Should it go ahead?"},
		{"status_period_other_git_push", "Docs finished updating git documentation. Auth asks to run git push; should it go ahead?"},
		{"different_git_subcommand", "Docs asks whether to check git history."},
		{"past_tense_completed_command", "Docs already ran git push earlier and finished."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel()
			h := newBrainHarness(t, m)
			h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
			h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

			// Auth blocks on printf and the user hears it.
			h.block(authPane, "codex-approval-prompt.screen.txt")
			ap, _ := h.pending()
			m.Add(braintest.Step{Text: braintest.Reply(ap.SpokenSummary+" Should it go ahead?", "")})
			h.say("What is waiting?")
			h.clock.Advance(time.Second)

			// Docs blocks on git push origin main, unheard.
			h.srv.SetScreen(docsPane, gitPushPromptScreen)
			h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
			dp, ok := h.pending()
			if !ok || dp.PaneID != docsPane {
				t.Fatalf("pending = %+v, want docs", dp)
			}

			m.Add(braintest.Step{Text: braintest.Reply(tc.reply, "")})
			h.say("Status?")
			h.clock.Advance(500 * time.Millisecond)

			m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
			m.Add(braintest.Step{Text: braintest.Reply("Holding off.", "")})
			h.say("Yes, approve it.")
			if n := h.keysTo(docsPane); n != 0 {
				t.Fatalf("reply %q falsely marked docs' git push told (%d key presses)", tc.reply, n)
			}
		})
	}
}

// Finding 3: restarting the daemon after B displaced heard A must not erase the
// evidence needed to reject a bare "yes" meant for A.
func TestS3Finding3RestartPreservesDisplacementProtection(t *testing.T) {
	h := toldAuth(t)
	blockDocs(t, h)
	h.clock.Advance(500 * time.Millisecond)

	store2, err := state.Open(h.store.Path(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{
		PendingTTL:     120 * time.Second,
		Now:            h.clock.Now,
		DataDir:        h.dir,
		inputGrace:     -1,
		relayAfter:     -1,
		interruptCheck: 40 * time.Millisecond,
		bareAnswers:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sup2.shutdown()
	sup2.SetVoiceConnected(true)
	sup2.reconcileReprompts(context.Background())

	// User says "yes" meant for auth, which they heard right before restart.
	sup2.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes.", Final: true})
	res, err := sup2.Handler()(context.Background(), voice.ToolCall{ID: "c1", Name: ToolToSupervisor, Args: map[string]any{"text": "Yes."}})
	if err != nil {
		t.Fatal(err)
	}
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("after restart, bare yes meant for auth approved unheard docs (%d key presses)", n)
	}
	if !strings.Contains(res.Spoken, "did not send that") || !strings.Contains(res.Spoken, "docs") {
		t.Fatalf("refusal after restart = %+v, want explanation and re-ask of docs", res)
	}

	// After hearing the re-ask, the user's next "yes" approves docs.
	h.clock.Advance(time.Second)
	sup2.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes.", Final: true})
	if _, err := sup2.Handler()(context.Background(), voice.ToolCall{ID: "c2", Name: ToolToSupervisor, Args: map[string]any{"text": "Yes."}}); err != nil {
		t.Fatal(err)
	}
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs after re-ask = %d, want 1", n)
	}
}

// Finding 4: when Auth's heard A1 is replaced on screen by A2 while Docs owns
// the pending slot, AnswerPrompt revives A2 and refuses it with a re-ask. That
// re-ask's pending_id must resolve so speaking the re-ask marks A2 told and the
// user's follow-up approval succeeds, without replacing Docs as Pending on the
// refusal.
func TestS3Finding4RevivedDisplacedReaskIDResolves(t *testing.T) {
	h := toldAuth(t)
	h.speak(blockDocs(t, h))
	docsPending, ok := h.pending()
	if !ok || docsPending.PaneID != docsPane {
		t.Fatalf("pending = %+v, want docs", docsPending)
	}

	// Auth's screen moves from A1 (printf) to A2 (create claude.txt) before
	// reconcile runs, while Docs still owns Pending.
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

	// Find the pushed re-ask and deliver it as the voice session does, carrying
	// its pending_id tag.
	all := h.pusher.all()
	reaskIdx := -1
	for i := from; i < len(all); i++ {
		if all[i].Urgency != voice.UrgencySilent && strings.Contains(all[i].Text, "did not send that") && strings.Contains(all[i].Text, "auth") {
			reaskIdx = i
			break
		}
	}
	if reaskIdx < 0 {
		t.Fatalf("no re-ask pushed for auth; pushes = %v", h.pushTexts()[from:])
	}
	tags := h.pusher.tagsOf(reaskIdx)
	pid, _ := tags[tagPendingID].(string)
	h.sup.OnVoiceEvent(voice.EventPush{Text: all[reaskIdx].Text, Urgency: all[reaskIdx].Urgency, PendingID: pid})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: all[reaskIdx].Text, Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})

	// Now that the user heard A2's re-ask, "Yes, auth can go ahead" must pass.
	h.clock.Advance(time.Second)
	h.hear("Yes, auth can go ahead.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, []string{"1"}); err != nil {
		t.Fatalf("second AnswerPrompt after speaking A2's re-ask failed: %v", err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to auth = %d, want 1", n)
	}
}

// Finding 5: when a typed turn is refused with a voice session configured and
// no listener speaking pushes, TypedTurn must return the re-ask synchronously
// in TypedReply.Text and record that delivery so the next typed answer passes.
func TestS3Finding5TypedRefusalReturnsReaskInTypedReply(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)

	// User hears Auth's A1.
	h.block(authPane, "codex-approval-prompt.screen.txt")
	a1, _ := h.pending()
	m.Add(braintest.Step{Text: braintest.Reply(a1.SpokenSummary+" Should it go ahead?", "")})
	h.say("What is waiting?")
	h.clock.Advance(time.Second)

	// Auth's prompt changes to A2 (same-pane displacement: even naming "auth"
	// cannot bypass it), and no voice listener speaks A2's announcement.
	h.srv.SetScreen(authPane, "working")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.clock.Advance(500 * time.Millisecond)
	h.block(authPane, "claude-permission-prompt.screen.txt")
	h.clock.Advance(500 * time.Millisecond)

	// User submits a typed approval via TypedTurn while no voice listener plays
	// spoken pushes.
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

	// Follow-up typed approval must succeed without any voice EventPush.
	h.clock.Advance(time.Second)
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"1"}})}})
	m.Add(braintest.Step{Text: braintest.Reply("Approved auth.", "")})
	h.sup.ShepherdInput(InputTyped, "yes, approve auth")
	reply2, err := h.sup.TypedTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("second typed turn after synchronous typed re-ask sent %d key presses, want 1 (reply=%+v)", n, reply2)
	}
}

func TestS3EdgeCases(t *testing.T) {
	t.Run("cut_reply_keeps_stated_prefix_and_revokes_suffix", func(t *testing.T) {
		m := braintest.NewModel()
		h := newBrainHarness(t, m)
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, _ := h.pending()
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dp, _ := h.pending()

		full := ap.SpokenSummary + " Also, " + dp.SpokenSummary + " Should they go ahead?"
		m.Add(braintest.Step{Text: braintest.Reply(full, "")})
		res := h.say("What is waiting?")

		// Client hears auth's question, then cuts before docs' question.
		h.sup.OnVoiceEvent(voice.EventInterrupted{})
		h.sup.OnVoiceEvent(voice.EventTranscript{
			Dir:   voice.DirOut,
			Text:  ap.SpokenSummary,
			Final: true,
			Cut:   &voice.TranscriptCut{Full: res.Spoken, HeardMs: 1100, UtteranceID: res.UtteranceID},
		})

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("Holding off.", "")})
		h.say("Yes, approve it.")
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("cut before docs still authorised docs (%d key presses)", n)
		}
	})

	t.Run("typed_delivery_not_revoked_by_unrelated_voice_cut", func(t *testing.T) {
		m := braintest.NewModel()
		h := newBrainHarness(t, m)
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, _ := h.pending()
		m.Add(braintest.Step{Text: braintest.Reply(ap.SpokenSummary+" Should it go ahead?", "")})
		h.say("What is waiting?")
		h.clock.Advance(time.Second)

		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dp, _ := h.pending()

		m.Add(braintest.Step{Text: braintest.Reply(dp.SpokenSummary+" Should it go ahead?", "")})
		h.sup.ShepherdInput(InputTyped, "what is docs asking?")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}

		// An unrelated voice cut arrives afterwards; typed telling must remain.
		h.sup.OnVoiceEvent(voice.EventTranscript{
			Dir:   voice.DirOut,
			Text:  "Sure",
			Final: true,
			Cut:   &voice.TranscriptCut{Full: "Sure, let me check.", HeardMs: 300},
		})

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("Approved docs.", "")})
		h.sup.ShepherdInput(InputTyped, "yes, go ahead")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("typed telling lost on voice cut: keys to docs = %d, want 1", n)
		}
	})

	t.Run("comma_less_conjunction_and_superstring_alias", func(t *testing.T) {
		h := newHarness(t)
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.addAgent(docsPane, "auth-v2", "codex", herdr.StateWorking)
		if _, err := h.store.Update(docsPane, func(a *state.Agent) { a.Alias = "auth-v2"; a.Spoken = "auth v2" }); err != nil {
			t.Fatal(err)
		}
		p := state.PendingAction{PaneID: authPane, Command: "git push origin main"}
		h.sup.mu.Lock()
		bySuper := h.sup.statesLocked("auth-v2 asks to run git push; should it go ahead?", p)
		byConj := h.sup.statesLocked("auth finished updating git documentation and auth-v2 asks to run git push?", p)
		byReal := h.sup.statesLocked("auth asks whether to run git push on main.", p)
		h.sup.mu.Unlock()
		if bySuper {
			t.Fatal("superstring alias auth-v2 falsely stated auth's prompt")
		}
		if byConj {
			t.Fatal("comma-less conjunction bled auth-v2's question into auth's status clause")
		}
		if !byReal {
			t.Fatal("natural paraphrase of auth's git push did not count as stated")
		}
	})

	t.Run("same_pane_and_closed_window_across_restart_plus_tap", func(t *testing.T) {
		h := toldAuth(t)
		// Auth closes right before restart; docs blocks right after restart.
		h.srv.SetScreen(authPane, "running")
		h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
		h.clock.Advance(time.Second)

		store2, err := state.Open(h.store.Path(), state.Options{})
		if err != nil {
			t.Fatal(err)
		}
		sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{
			PendingTTL:     120 * time.Second,
			Now:            h.clock.Now,
			DataDir:        h.dir,
			inputGrace:     -1,
			relayAfter:     -1,
			interruptCheck: 40 * time.Millisecond,
			bareAnswers:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sup2.shutdown()
		sup2.SetVoiceConnected(true)

		h.srv.SetScreen(docsPane, fixture(t, "codex-approval-prompt.screen.txt"))
		h.srv.SetStatus(docsPane, herdr.StateBlocked)
		row, _ := store2.Get(docsPane)
		sup2.HandleTransition(context.Background(), herdr.Transition{PaneID: docsPane, From: herdr.StateWorking, To: herdr.StateBlocked, At: h.clock.Now(), Agent: row.Kind, WorkspaceID: row.Workspace, WorkspaceLabel: row.WorkspaceLabel})
		dp, ok := store2.Pending()
		if !ok || dp.PaneID != docsPane {
			t.Fatalf("pending = %+v, want docs", dp)
		}

		// Bare "yes" within window of auth's pre-restart question is refused.
		sup2.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes.", Final: true})
		if _, err := sup2.Handler()(context.Background(), voice.ToolCall{ID: "c1", Name: ToolToSupervisor, Args: map[string]any{"text": "Yes."}}); err != nil {
			t.Fatal(err)
		}
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("post-restart bare yes within closed window sent %d keys to docs", n)
		}

		// Phone tap by ID always works without waiting for voice delivery.
		if _, err := sup2.AnswerTap(context.Background(), dp.ID, 1); err != nil {
			t.Fatalf("AnswerTap after restart: %v", err)
		}
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("keys to docs after tap = %d, want 1", n)
		}
	})
}
