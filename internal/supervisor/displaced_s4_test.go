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

		spoken := "Docs is waiting on you. " + ap.SpokenSummary + " Should it go ahead?"
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
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)

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
