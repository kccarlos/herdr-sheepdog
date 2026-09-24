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
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Answer within brain.reply_wait_s, else a silent pending result and the
// reply pushed later (utterance.go's awaitReply / deliverLate).

// replyWait sets Config.ReplyWait.
func replyWait(d time.Duration) func(*Config) {
	return func(c *Config) { c.ReplyWait = d }
}

// utterancePushes returns the pushes tagged answers_utterance, with
// their tags.
func utterancePushes(h *harness) ([]voice.EventPush, []map[string]any) {
	var pushes []voice.EventPush
	var tags []map[string]any
	for i, p := range h.pusher.all() {
		if tg := h.pusher.tagsOf(i); tg["answers_utterance"] != nil {
			pushes = append(pushes, p)
			tags = append(tags, tg)
		}
	}
	return pushes, tags
}

// assertPending checks the silent pending result: no speech of its own.
func assertPending(t *testing.T, res voice.ToolResult) {
	t.Helper()
	if !res.Pending || res.Spoken != "" || res.Scheduling != voice.UrgencySilent || res.Detail != replyPendingDetail {
		t.Fatalf("result = %+v, want a silent pending result", res)
	}
}

// A reply inside the wait is the tool result, exactly as before; nothing
// is pushed.
func TestReplyInsideWaitIsInline(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("auth is working on the tests.", "")})
	h := newBrainHarness(t, m, replyWait(5*time.Second))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	res := h.say("how is auth doing")
	if res.Spoken != "auth is working on the tests." || res.Pending {
		t.Fatalf("result = %+v", res)
	}
	h.sup.replies.Wait()
	if pushes, _ := utterancePushes(h); len(pushes) != 0 {
		t.Errorf("pushes = %v, want none", pushes)
	}
	if n := h.log.count("brain.reply_pending") + h.log.count("brain.reply_delivered"); n != 0 {
		t.Errorf("%d reply events, want none", n)
	}
	h.sup.mu.Lock()
	defer h.sup.mu.Unlock()
	if h.sup.utterance != nil {
		t.Error("the slot outlived an inline reply")
	}
}

// A slow reply: the call returns a silent pending result at once, and the
// reply is pushed once at idle when it lands, tagged and with its subject
// in front.
func TestSlowReplyIsPushedLater(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("The tests are green.", "")})
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	assertPending(t, h.say("how is auth doing"))
	pending := h.log.ofType("brain.reply_pending")
	if len(pending) != 1 {
		t.Fatalf("brain.reply_pending = %d", len(pending))
	}
	id := payload(pending[0])["id"]
	if payload(pending[0])["call_id"] != "c" {
		t.Errorf("reply_pending = %v, want the voice's call id", payload(pending[0]))
	}
	if pushes, _ := utterancePushes(h); len(pushes) != 0 {
		t.Fatalf("pushed before the reply landed: %v", pushes)
	}
	close(release)
	h.sup.replies.Wait()

	pushes, tags := utterancePushes(h)
	if len(pushes) != 1 {
		t.Fatalf("pushes = %v, want exactly one", pushes)
	}
	if pushes[0].Text != "About auth: The tests are green." || pushes[0].Urgency != voice.UrgencyIdle {
		t.Errorf("push = %+v", pushes[0])
	}
	if tags[0]["answers_utterance"] != id || tags[0]["agent"] != "auth" {
		t.Errorf("tags = %v, want answers_utterance %v", tags[0], id)
	}
	ev := h.log.ofType("brain.reply_delivered")
	if len(ev) != 1 || payload(ev[0])["id"] != id || payload(ev[0])["urgency"] != "idle" || payload(ev[0])["acted"] != false {
		t.Errorf("brain.reply_delivered = %v", ev)
	}
	h.sup.mu.Lock()
	defer h.sup.mu.Unlock()
	if h.sup.utterance != nil {
		t.Error("the slot outlived the late reply")
	}
}

// A reply that already opens with the agent's name gets no prefix, and
// its options are read as a choice.
func TestLateLineKeepsAliasAndOptions(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: `{"spoken": "Auth can rerun or skip.", "detail": "", "options": ["rerun", "skip"]}`})
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	assertPending(t, h.say("what now"))
	close(release)
	h.sup.replies.Wait()
	pushes, _ := utterancePushes(h)
	if len(pushes) != 1 || pushes[0].Text != "Auth can rerun or skip. Options: rerun; skip." {
		t.Errorf("pushes = %v", pushes)
	}
}

// A late reply that says the shepherd must decide on an open prompt asks
// for the floor, but without the needs_user tag: it is an addition to a
// block already spoken, so the voice still finishes its own sentence.
func TestLateNeedsUserOnOpenPromptInterrupts(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: `{"spoken": "Auth wants to create hello.txt; allow it?", "detail": "", "needs_user": true}`})
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	blockedAuth(t, h)

	assertPending(t, h.say("what does auth want"))
	close(release)
	h.sup.replies.Wait()
	pushes, tags := utterancePushes(h)
	if len(pushes) != 1 || pushes[0].Urgency != voice.UrgencyInterrupt || tags[0]["needs_user"] != nil {
		t.Errorf("pushes = %v %v, want one interrupt push without the needs_user tag", pushes, tags)
	}
}

// The voice cancels the handler's context as soon as the handler returns
// (gemini/toolexec.go); the detached turn must not be a child of it.
func TestToolContextCancelDoesNotCancelTurn(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("The tests are green.", "")})
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	h.hear("how is auth doing")
	ctx, cancel := context.WithCancel(context.Background())
	res, err := h.sup.Handler()(ctx, voice.ToolCall{ID: "c1", Name: ToolToSupervisor, Args: map[string]any{"text": "how is auth doing"}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	assertPending(t, res)
	close(release)
	h.sup.replies.Wait()
	pushes, _ := utterancePushes(h)
	if len(pushes) != 1 || pushes[0].Text != "About auth: The tests are green." {
		t.Errorf("pushes = %v, want the brain's reply", pushes)
	}
	if h.log.count("brain.fallback") != 0 {
		t.Error("the turn was cancelled with the tool call")
	}
}

// A stop while the detached turn runs cancels it through the slot, and
// nothing is pushed afterwards.
func TestStopCancelsDetachedTurn(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	m := braintest.NewModel(braintest.Step{
		Block: release,
		Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "add a readme"})},
	})
	m.Default = &braintest.Step{Text: braintest.Reply("Withdrew your request before anything was sent.", "")}
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	assertPending(t, h.say("tell auth to add a readme"))
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != nil && h.sup.utterance.inBrain
	}, "the detached turn to hold the slot")

	stop := h.call(ToolStop, nil)
	h.sup.replies.Wait()
	if !strings.Contains(stop.Spoken, "Withdrew") {
		t.Errorf("stop = %+v", stop)
	}
	assertStopped(t, h, stopPhaseCancelTurn, StopTriggerTool)
	if pushes, _ := utterancePushes(h); len(pushes) != 0 {
		t.Errorf("a stopped turn delivered %v", pushes)
	}
	if len(h.promptRequests()) != 0 {
		t.Errorf("an instruction was sent after the stop: %v", h.promptRequests())
	}
	if n := h.log.count("brain.reply_delivered") + h.log.count("brain.reply_stale"); n != 0 {
		t.Errorf("%d reply events for a stopped turn", n)
	}
}

// The shepherd asked something else before the reply landed: a turn that
// did nothing is dropped (brain.reply_stale), the newer one answered.
func TestStaleReplyWithoutActionIsDropped(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(
		braintest.Step{Block: release, Text: braintest.Reply("The tests are green.", "")},
		braintest.Step{Text: braintest.Reply("Docs is idle.", "")},
	)
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)

	assertPending(t, h.say("how is auth doing"))
	first := payload(h.log.ofType("brain.reply_pending")[0])["id"]
	// The newer request queues behind the first on the brain's turn lock,
	// so it too is pending.
	assertPending(t, h.say("what about docs"))
	close(release)
	h.sup.replies.Wait()

	stale := h.log.ofType("brain.reply_stale")
	if len(stale) != 1 || payload(stale[0])["id"] != first || payload(stale[0])["acted"] != false {
		t.Fatalf("brain.reply_stale = %v", stale)
	}
	pushes, tags := utterancePushes(h)
	if len(pushes) != 1 || pushes[0].Text != "Docs is idle." || tags[0]["answers_utterance"] == first {
		t.Errorf("pushes = %v %v, want only the newer reply", pushes, tags)
	}
}

// A stale reply of a turn that acted is still pushed, silent: the voice
// must know what was sent, but the conversation has moved on.
func TestStaleReplyThatActedIsSilent(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "add a readme"})}},
		braintest.Step{Block: release, Text: braintest.Reply("Telling auth to add a readme. Sent.", "")},
		braintest.Step{Text: braintest.Reply("Docs is idle.", "")},
	)
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)

	assertPending(t, h.say("tell auth to add a readme"))
	waitFor(t, func() bool { return len(h.promptRequests()) == 1 }, "the instruction to be sent")
	first := payload(h.log.ofType("brain.reply_pending")[0])["id"]
	assertPending(t, h.say("what about docs"))
	close(release)
	h.sup.replies.Wait()

	stale := h.log.ofType("brain.reply_stale")
	if len(stale) != 1 || payload(stale[0])["id"] != first || payload(stale[0])["acted"] != true {
		t.Fatalf("brain.reply_stale = %v", stale)
	}
	pushes, tags := utterancePushes(h)
	if len(pushes) != 2 {
		t.Fatalf("pushes = %v, want the silent one and the newer reply", pushes)
	}
	for i, p := range pushes {
		switch tags[i]["answers_utterance"] {
		case first:
			if p.Urgency != voice.UrgencySilent || !strings.Contains(p.Text, "Telling auth to add a readme") {
				t.Errorf("stale push = %+v", p)
			}
		default:
			if p.Urgency != voice.UrgencyIdle || p.Text != "Docs is idle." {
				t.Errorf("newer push = %+v", p)
			}
		}
	}
}

// ReplyWait negative is the synchronous behaviour: the call waits for the
// whole turn and nothing is pushed.
func TestNegativeReplyWaitIsSynchronous(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("The tests are green.", "")})
	h := newBrainHarness(t, m, replyWait(-1))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	done := make(chan voice.ToolResult, 1)
	go func() { done <- h.say("how is auth doing") }()
	select {
	case res := <-done:
		t.Fatalf("returned before the turn finished: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	res := <-done
	if res.Spoken != "The tests are green." || res.Pending {
		t.Errorf("result = %+v", res)
	}
	h.sup.replies.Wait()
	if pushes, _ := utterancePushes(h); len(pushes) != 0 {
		t.Errorf("pushes = %v", pushes)
	}
	if h.log.count("brain.reply_pending") != 0 {
		t.Error("brain.reply_pending on the synchronous path")
	}
}

// A brain that fails after the wait falls back to the line that says
// so, pushed rather than returned.
func TestLateBrainErrorPushesFallback(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Err: braintest.ErrBoom})
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	assertPending(t, h.say("how is auth doing"))
	close(release)
	h.sup.replies.Wait()
	pushes, _ := utterancePushes(h)
	// The same line as the inline fallback: no roster, no "no change".
	if len(pushes) != 1 || !strings.HasSuffix(pushes[0].Text, brainFailedLine) || pushes[0].Urgency != voice.UrgencyIdle {
		t.Errorf("pushes = %v, want the failed-brain fallback", pushes)
	}
	ev := h.log.ofType("brain.reply_delivered")
	if len(ev) != 1 || payload(ev[0])["fallback"] != true {
		t.Errorf("brain.reply_delivered = %v", ev)
	}
}
