package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func replyJSONWithOptions(t *testing.T, spoken string, options ...string) string {
	t.Helper()
	b, err := json.Marshal(brain.Reply{Spoken: spoken, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 9. TypedTurn and NoteReturnedText (--voice=off routeUtterance) mark told only
// on text actually returned to the client; unrendered Options never count.
func TestS4Finding3TypedReplyCountsOnlyReturnedText(t *testing.T) {
	t.Run("unseen_options_do_not_tell", func(t *testing.T) {
		m := braintest.NewModel()
		h := newBrainHarness(t, m)
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, _ := h.pending()
		m.Add(braintest.Step{Text: braintest.Reply(ap.SpokenSummary+" Should it go ahead?", "")})
		h.sup.ShepherdInput(InputTyped, "what is waiting?")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Second)

		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)

		spoken := ap.SpokenSummary + " Should it go ahead?"
		m.Add(braintest.Step{Text: replyJSONWithOptions(t, spoken, "Approve docs' git push origin main", "Approve auth")})
		h.sup.ShepherdInput(InputTyped, "status?")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("Holding off.", "")})
		h.sup.ShepherdInput(InputTyped, "yes")
		tr2, err := h.sup.TypedTurn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("typed yes approved docs from unseen Options (%d key presses)", n)
		}
		if !strings.Contains(tr2.Text, "did not send that") || !strings.Contains(tr2.Text, "git push") {
			t.Fatalf("TypedReply.Text = %q, want synchronous displacement re-ask for git push", tr2.Text)
		}

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("Approved docs.", "")})
		h.sup.ShepherdInput(InputTyped, "yes")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("keys to docs after visible re-ask = %d, want 1", n)
		}
	})

	t.Run("unseen_options_do_not_promote_held_question", func(t *testing.T) {
		m := braintest.NewModel()
		m.EventDefault = braintest.NothingToAdd()
		w := newMuteWorld(t, m, Quiet)
		w.mute(t)
		w.srv.SetScreen(authPane, gitPushPromptScreen)
		w.transition(authPane, herdr.StateWorking, herdr.StateBlocked)
		q := w.store.Questions()[0]

		m.Add(braintest.Step{Text: replyJSONWithOptions(t, "Auth is paused in your muted space.", q.SpokenSummary)})
		w.sup.ShepherdInput(InputTyped, "how are things going?")
		if _, err := w.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !w.sup.heldQuestion(q.ID) {
			t.Fatal("unseen Options in typed turn promoted a held question")
		}
	})

	t.Run("voice_off_returned_text_marks_told_and_unseen_options_do_not", func(t *testing.T) {
		m := braintest.NewModel()
		h := newBrainHarness(t, m)
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
		h.sup.ShepherdInput(InputTyped, "status?")
		h.clock.Advance(100 * time.Millisecond)
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		// Question opened after the typed turn began is not told by NoteReturnedText.
		h.sup.NoteReturnedText("Docs wants to git push origin main.")

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Text: replyJSONWithOptions(t, "Choose which agent to answer.", "Approve docs' git push origin main")})
		h.sup.ShepherdInput(InputTyped, "options?")
		res, err := h.sup.ToSupervisor(context.Background(), "options?")
		if err != nil {
			t.Fatal(err)
		}
		h.sup.NoteReturnedText(res.Spoken)

		h.clock.Advance(time.Second)
		h.sup.ShepherdInput(InputTyped, "yes")
		reask, err := h.sup.ToSupervisor(context.Background(), "yes")
		if err != nil {
			t.Fatal(err)
		}
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("unrendered ToolResult.Options marked docs told (%d keys)", n)
		}
		h.sup.NoteReturnedText(reask.Spoken)

		h.clock.Advance(time.Second)
		h.sup.ShepherdInput(InputTyped, "yes")
		if _, err := h.sup.ToSupervisor(context.Background(), "yes"); err != nil {
			t.Fatal(err)
		}
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("keys after returned re-ask = %d, want 1", n)
		}
	})
}

func newDocsMutedWorld(t *testing.T, m *braintest.Model) *harness {
	t.Helper()
	var h *harness
	mutes, err := attention.OpenMutes("", attention.MutesOptions{Now: func() time.Time { return h.clock.Now() }})
	if err != nil {
		t.Fatal(err)
	}
	opts := []func(*Config){func(c *Config) { c.Chattiness = string(Quiet); c.Mutes = mutes }}
	if m != nil {
		h = newBrainHarness(t, m, opts...)
	} else {
		h = newHarness(t, opts...)
	}
	authDir := t.TempDir()
	docsDir := t.TempDir()
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.srv.Handle("server.agent_manifests", func(json.RawMessage) (any, error) {
		return map[string]any{"type": "agent_manifest_status", "manifests": []map[string]any{}}, nil
	})
	h.srv.SetPane(herdr.PaneInfo{PaneID: authPane, WorkspaceID: "auth", Cwd: authDir})
	h.srv.SetPane(herdr.PaneInfo{PaneID: docsPane, WorkspaceID: "docs", Cwd: docsDir})
	if _, err := h.sup.SetSpaceMute(context.Background(), "docs", true, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestS4Finding4MutedPromotedQuestionStaysAnswerableAfterDisplacement(t *testing.T) {
	t.Run("kept_promise_then_another_pane_takes_pending", func(t *testing.T) {
		h := newDocsMutedWorld(t, nil)

		if _, err := h.sup.createPromise(docsPane, brain.PromiseRequest{Text: "tell me when docs asks", Event: "blocked"}); err != nil {
			t.Fatal(err)
		}
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dp, _ := h.pending()
		h.speak(h.lastPushText())

		h.clock.Advance(time.Second)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		h.speak(h.lastPushText())
		ap, _ := h.pending()

		h.clock.Advance(time.Second)
		h.hear("Yes, docs can go ahead.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, dp.YesKeys); err != nil {
			t.Fatalf("AnswerPrompt to promoted, already-told docs failed: %v", err)
		}
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("keys to docs = %d, want 1", n)
		}
		if after, ok := h.pending(); !ok || after.ID != ap.ID {
			t.Fatalf("pending after answering docs = %+v, want auth (%s)", after, ap.ID)
		}
	})

	t.Run("requested_explanation_then_another_pane_takes_pending", func(t *testing.T) {
		m := braintest.NewModel()
		m.EventDefault = braintest.NothingToAdd()
		h := newDocsMutedWorld(t, m)

		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dq := h.store.Questions()[0]

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Text: braintest.Reply(dq.SpokenSummary+" Should it go ahead?", "")})
		h.sup.ShepherdInput(InputTyped, "what is docs asking?")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if h.sup.heldQuestion(dq.ID) {
			t.Fatal("docs remained held after full summary was read out")
		}

		h.clock.Advance(time.Second)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, _ := h.pending()

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("Approved docs.", "")})
		h.sup.ShepherdInput(InputTyped, "yes, approve docs")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("keys to docs = %d, want 1", n)
		}
		if after, ok := h.pending(); !ok || after.ID != ap.ID {
			t.Fatalf("pending after answering docs = %+v, want auth (%s)", after, ap.ID)
		}
	})

	t.Run("held_remuted_and_unheard_revival_checks_stay", func(t *testing.T) {
		h := newDocsMutedWorld(t, nil)

		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dq := h.store.Questions()[0]
		h.block(authPane, "codex-approval-prompt.screen.txt")
		h.speak(h.lastPushText())
		h.clock.Advance(time.Second)
		h.hear("Yes, approve docs.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, dq.YesKeys); err == nil {
			t.Fatal("held question in muted space was answered")
		}

		h.sup.NoteReturnedText(dq.SpokenSummary + " Should it go ahead?")
		if _, err := h.sup.SetSpaceMute(context.Background(), "docs", false, nil, ViaPhone); err != nil {
			t.Fatal(err)
		}
		if _, err := h.sup.SetSpaceMute(context.Background(), "docs", true, nil, ViaPhone); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Second)
		h.hear("Yes, approve docs.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, dq.YesKeys); err == nil {
			t.Fatal("re-muted question was answered")
		}

		h.sup.mu.Lock()
		h.sup.dropQuestionLocked(dq)
		h.sup.mu.Unlock()
		h.srv.SetScreen(docsPane, fixture(t, "claude-permission-prompt.screen.txt"))
		h.srv.SetStatus(docsPane, herdr.StateBlocked)
		if _, err := h.store.Update(docsPane, func(a *state.Agent) { a.State = herdr.StateBlocked }); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Second)
		h.hear("Yes, approve docs.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, []string{"1"}); err == nil {
			t.Fatal("true revival of unheard prompt in muted space was answered")
		}
	})

	t.Run("promoted_but_unplayed_stays_refused", func(t *testing.T) {
		h := newDocsMutedWorld(t, nil)
		if _, err := h.sup.createPromise(docsPane, brain.PromiseRequest{Text: "tell me when docs asks", Event: "blocked"}); err != nil {
			t.Fatal(err)
		}
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dp, _ := h.pending()
		h.sup.OnVoiceEvent(voice.EventPush{Text: h.lastPushText(), Urgency: voice.UrgencyIdle, PendingID: dp.ID})

		h.clock.Advance(time.Second)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		h.speak(h.lastPushText())
		h.clock.Advance(time.Second)
		h.hear("Yes, docs can go ahead.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, dp.YesKeys); err == nil {
			t.Fatal("promoted question whose push never played was answered")
		}
	})
}

func TestClosedToldEvidenceNotRevived(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)

	// A1 is told, then the pane leaves blocked so its told entry closes.
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()
	h.speak(h.lastPushText())
	h.clock.Advance(time.Second)
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)

	// Within the window the pane blocks again on identical prompt A2, but the
	// initial screen read fails so no question or pending_id push is created.
	h.clock.Advance(time.Second)
	base := h.sup.herdr
	failRead := true
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onRead: func(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
			if failRead {
				return herdr.ReadResult{}, context.DeadlineExceeded
			}
			return base.Read(ctx, paneID, source, lines)
		},
	}
	h.srv.SetScreen(authPane, fixture(t, "codex-approval-prompt.screen.txt"))
	h.transition(authPane, herdr.StateWorking, herdr.StateBlocked)
	failRead = false

	// A later bare "yes" or send_keys revives A2 from the screen; A1's closed
	// evidence must not satisfy pTold for A2.
	h.clock.Advance(time.Second)
	h.hear("Yes.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err == nil {
		t.Fatal("AnswerPrompt approved revived A2 using A1's closed told evidence")
	}
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("keys to auth = %d, want 0", n)
	}
}

func TestAnswerShapedAliasDoesNotBypassRail(t *testing.T) {
	h := toldAuth(t)
	if _, err := h.store.Update(docsPane, func(a *state.Agent) { a.Alias = "ok"; a.Spoken = "ok" }); err != nil {
		t.Fatal(err)
	}
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	p, _ := h.pending()

	// The user says "OK" meaning auth; it must not count as naming the untold
	// agent aliased "ok" on either the fast path or send_keys.
	h.clock.Advance(500 * time.Millisecond)
	h.say("OK")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("fast path sent %d keys to untold 'ok' agent on bare 'OK'", n)
	}
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys); err == nil {
		t.Fatal("AnswerPrompt approved untold 'ok' agent on bare 'OK'")
	}

	// A real naming utterance still authorises the short alias.
	h.clock.Advance(time.Second)
	h.hear("tell ok yes")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys); err != nil {
		t.Fatalf("AnswerPrompt on 'tell ok yes' failed: %v", err)
	}
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to 'ok' after 'tell ok yes' = %d, want 1", n)
	}
}

func TestReplayedQuestionUpdatesOrder(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

	// B (docs) told order 1, A (auth) told order 2; both open, A pending.
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	dp, _ := h.pending()
	h.speak(h.lastPushText())
	h.clock.Advance(time.Second)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()
	h.speak(h.lastPushText())

	// A later whole run re-asks B (docs), handed to the voice again.
	h.clock.Advance(time.Second)
	h.sup.noteHanded(dp.ID)
	tReask := h.clock.Now()
	h.sup.OnVoiceEvent(voice.EventPlayed{Text: "docs is still asking to run printf.", Start: tReask, End: tReask.Add(time.Second)})

	// A bare yes meant for B must not approve pending A (auth).
	h.clock.Advance(2 * time.Second)
	h.say("Yes.")
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("bare yes after re-asking docs approved auth (%d keys)", n)
	}
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err == nil {
		t.Fatal("AnswerPrompt approved auth on bare yes after docs was re-asked")
	}
}

// A run about one question that happens to contain another agent's name does
// not make the other question newer, so its bare answer still lands.
func TestRunMentioningAnotherAgentKeepsItsOrder(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	h.speak(h.lastPushText())
	h.clock.Advance(time.Second)
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	dp, _ := h.pending()
	h.sup.noteHanded(dp.ID)
	h.clock.Advance(time.Second)
	start := h.clock.Now()
	h.sup.OnVoiceEvent(voice.EventPlayed{Text: "docs asks to edit auth.go.", Start: start, End: start.Add(time.Second)})
	h.clock.Advance(2 * time.Second)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs = %d, want 1", n)
	}
}

// A newer told question that closes just before the bare answer leaves the
// answer unclaimed: it must not fall to the older open question.
func TestBareYesDoesNotFallToOlderQuestionWhenNewerCloses(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	h.speak(h.lastPushText())
	h.clock.Advance(time.Second)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	h.speak(h.lastPushText())
	h.clock.Advance(time.Second)
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.clock.Advance(100 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("keys to docs = %d, want 0", n)
	}
}
