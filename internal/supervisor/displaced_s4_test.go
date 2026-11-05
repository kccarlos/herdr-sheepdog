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

// Finding 3: TypedTurn returns only Spoken (plus any synchronous daemon re-ask),
// never Options, so options that the typed user never saw must neither mark an
// unheard prompt told nor promote a held question.
func TestS4Finding3TypedReplyCountsOnlyReturnedText(t *testing.T) {
	t.Run("unseen_options_do_not_tell", func(t *testing.T) {
		m := braintest.NewModel()
		h := newBrainHarness(t, m)
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)

		// Auth blocks on printf and the user sees it in a typed reply.
		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, _ := h.pending()
		m.Add(braintest.Step{Text: braintest.Reply(ap.SpokenSummary+" Should it go ahead?", "")})
		h.sup.ShepherdInput(InputTyped, "what is waiting?")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Second)

		// Docs blocks on git push origin main, displacing Auth without being seen.
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dp, ok := h.pending()
		if !ok || dp.PaneID != docsPane {
			t.Fatalf("pending = %+v, want docs", dp)
		}

		// Orchestrator reply puts Docs' command only in Options; Spoken only
		// mentions Docs waiting and asks Auth's question.
		spoken := "Docs is waiting on you. " + ap.SpokenSummary + " Should it go ahead?"
		m.Add(braintest.Step{Text: replyJSONWithOptions(t, spoken, "Approve docs' git push origin main", "Approve auth")})
		h.sup.ShepherdInput(InputTyped, "status?")
		tr1, err := h.sup.TypedTurn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(tr1.Text), "git push") {
			t.Fatalf("TypedReply.Text unexpectedly rendered git push: %q", tr1.Text)
		}

		// Even if an unrelated voice cut arrives, typed state must still refuse
		// a bare "yes" aimed at Docs because TypedReply.Text never showed it.
		h.sup.OnVoiceEvent(voice.EventTranscript{
			Dir:   voice.DirOut,
			Text:  "Okay",
			Final: true,
			Cut:   &voice.TranscriptCut{Full: "Okay, checking.", HeardMs: 200},
		})

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

		// Once TypedReply.Text actually showed the re-ask, the next typed yes passes.
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
		qs := w.store.Questions()
		if len(qs) != 1 || !w.sup.heldQuestion(qs[0].ID) {
			t.Fatalf("questions = %+v, want one held question", qs)
		}
		q := qs[0]

		m.Add(braintest.Step{Text: replyJSONWithOptions(t, "Auth is paused in your muted space.", q.SpokenSummary)})
		w.sup.ShepherdInput(InputTyped, "how are things going?")
		tr, err := w.sup.TypedTurn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(tr.Text), "git push") {
			t.Fatalf("TypedReply.Text unexpectedly contained summary: %q", tr.Text)
		}
		if !w.sup.heldQuestion(q.ID) {
			t.Fatal("unseen Options in typed turn promoted a held question")
		}

		w.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("Holding off.", "")})
		w.sup.ShepherdInput(InputTyped, "yes, approve auth")
		if _, err := w.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := w.keysTo(authPane); n != 0 {
			t.Fatalf("sent %d keys to held question promoted by unseen Options", n)
		}
	})

	t.Run("spoken_inline_reply_still_counts_options", func(t *testing.T) {
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

		m.Add(braintest.Step{Text: replyJSONWithOptions(t, "Choose which agent to answer.", "Approve docs' git push origin main")})
		res := h.say("Options?")
		if len(res.Options) != 1 {
			t.Fatalf("Options = %v, want 1 option", res.Options)
		}

		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}})
		m.Add(braintest.Step{Text: braintest.Reply("Approved docs.", "")})
		h.say("Yes.")
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("spoken inline reply with options sent %d keys, want 1", n)
		}
	})
}

func newDocsMutedWorld(t *testing.T, m *braintest.Model) (*harness, *attention.Mutes) {
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
	return h, mutes
}

// Finding 4: when a question in a muted space was explicitly read out and
// promoted (via a kept promise or a requested explanation) and another pane
// subsequently takes Pending, reusing that already-told open question must not
// be refused as revival of an unheard question, while held-question and true
// unheard-revival checks remain in place.
func TestS4Finding4MutedPromotedQuestionStaysAnswerableAfterDisplacement(t *testing.T) {
	t.Run("kept_promise_then_another_pane_takes_pending", func(t *testing.T) {
		h, _ := newDocsMutedWorld(t, nil)

		if _, err := h.sup.createPromise(docsPane, brain.PromiseRequest{Text: "tell me when docs asks", Event: "blocked"}); err != nil {
			t.Fatal(err)
		}
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		if got := h.pushTexts(); len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "As you asked:") {
			t.Fatalf("pushes = %v, want kept-promise announcement for docs", got)
		}
		dp, ok := h.pending()
		if !ok || dp.PaneID != docsPane {
			t.Fatalf("pending = %+v, want promoted docs", dp)
		}
		// Deliver the kept-promise push with its pending_id as the voice session does.
		lastIdx := len(h.pusher.all()) - 1
		pid, _ := h.pusher.tagsOf(lastIdx)[tagPendingID].(string)
		h.sup.OnVoiceEvent(voice.EventPush{Text: h.pusher.all()[lastIdx].Text, Urgency: h.pusher.all()[lastIdx].Urgency, PendingID: pid})
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: h.pusher.all()[lastIdx].Text, Final: true})
		h.sup.OnVoiceEvent(voice.EventTurnComplete{})

		// Auth (unmuted) blocks next and takes Pending.
		h.clock.Advance(time.Second)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		h.speak(h.lastPushText())
		ap, ok := h.pending()
		if !ok || ap.PaneID != authPane {
			t.Fatalf("pending = %+v, want auth", ap)
		}

		// User explicitly answers Docs; reusing Docs' promoted, already-told
		// open question must succeed while leaving Auth on Pending.
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
		h, _ := newDocsMutedWorld(t, m)

		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		qs := h.store.Questions()
		if len(qs) != 1 || !h.sup.heldQuestion(qs[0].ID) {
			t.Fatalf("questions = %+v, want docs held", qs)
		}
		dq := qs[0]

		// User asks about Docs; orchestrator reads out its summary in a typed reply.
		h.clock.Advance(time.Second)
		m.Add(braintest.Step{Text: braintest.Reply(dq.SpokenSummary+" Should it go ahead?", "")})
		h.sup.ShepherdInput(InputTyped, "what is docs asking?")
		if _, err := h.sup.TypedTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if h.sup.heldQuestion(dq.ID) {
			t.Fatal("docs remained held after full summary was read out")
		}

		// Auth blocks next and takes Pending.
		h.clock.Advance(time.Second)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, ok := h.pending()
		if !ok || ap.PaneID != authPane {
			t.Fatalf("pending = %+v, want auth", ap)
		}

		// User types "yes, approve docs" -> must succeed!
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
		h, _ := newDocsMutedWorld(t, nil)

		// 1. Unpromoted held question in muted space stays refused even when
		// another pane owns Pending and the user names docs.
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
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("keys to held docs = %d", n)
		}

		// 2. Promote and tell docs, then mute docs' space again: re-muting must
		// hold and untell it so answering docs is refused again.
		h.sup.toldInReply(dq.SpokenSummary + " Should it go ahead?")
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

		// 3. Unrecorded screen prompt in muted space (true revival, no open
		// question) stays refused.
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
		if n := h.keysTo(docsPane); n != 0 {
			t.Fatalf("keys to docs = %d, want 0", n)
		}
	})

	t.Run("promoted_but_unspoken_or_replaced_stays_refused_and_restart_keeps_promoted", func(t *testing.T) {
		h, mutes := newDocsMutedWorld(t, nil)

		if _, err := h.sup.createPromise(docsPane, brain.PromiseRequest{Text: "tell me when docs asks", Event: "blocked"}); err != nil {
			t.Fatal(err)
		}
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.transition(docsPane, herdr.StateWorking, herdr.StateBlocked)
		dp, ok := h.pending()
		if !ok || dp.PaneID != docsPane {
			t.Fatalf("pending = %+v, want docs", dp)
		}

		// Auth blocks before the kept-promise push for docs was ever spoken:
		// docs is promoted, yet not told, so answering docs must be refused.
		h.clock.Advance(time.Second)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		h.speak(h.lastPushText())
		h.clock.Advance(time.Second)
		h.hear("Yes, docs can go ahead.")
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, dp.YesKeys); err == nil {
			t.Fatal("promoted question whose push was never spoken was answered")
		}

		// Now speak docs' kept-promise push, then restart the daemon: the
		// promoted, already-told open question remains answerable after restart
		// unless docs' screen moved to a different prompt.
		for i, p := range h.pusher.all() {
			if strings.HasPrefix(p.Text, "As you asked:") {
				pid, _ := h.pusher.tagsOf(i)[tagPendingID].(string)
				h.sup.OnVoiceEvent(voice.EventPush{Text: p.Text, Urgency: p.Urgency, PendingID: pid})
				h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: p.Text, Final: true})
				h.sup.OnVoiceEvent(voice.EventTurnComplete{})
			}
		}

		store2, err := state.Open(h.store.Path(), state.Options{})
		if err != nil {
			t.Fatal(err)
		}
		sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{
			PendingTTL:     120 * time.Second,
			Now:            h.clock.Now,
			DataDir:        h.dir,
			Mutes:          mutes,
			Chattiness:     string(Quiet),
			inputGrace:     -1,
			relayAfter:     -1,
			interruptCheck: 40 * time.Millisecond,
			bareAnswers:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sup2.shutdown()

		// If docs' screen changed to an unheard replacement prompt in the muted
		// space before reconcile ran, reviving that new prompt must fail.
		h.srv.SetScreen(docsPane, fixture(t, "claude-permission-prompt.screen.txt"))
		h.clock.Advance(time.Second)
		sup2.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes, docs can go ahead.", Final: true})
		if _, err := (brainHost{sup2}).AnswerPrompt(context.Background(), docsPane, []string{"1"}); err == nil {
			t.Fatal("unheard replacement prompt on screen in muted space was answered")
		}

		// With the original heard prompt still on screen, reusing the promoted
		// open question after restart succeeds.
		h.srv.SetScreen(docsPane, gitPushPromptScreen)
		h.clock.Advance(time.Second)
		sup2.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes, docs can go ahead.", Final: true})
		if _, err := (brainHost{sup2}).AnswerPrompt(context.Background(), docsPane, dp.YesKeys); err != nil {
			t.Fatalf("post-restart AnswerPrompt to promoted, already-told docs failed: %v", err)
		}
		if n := h.keysTo(docsPane); n != 1 {
			t.Fatalf("keys to docs = %d, want 1", n)
		}
	})
}
