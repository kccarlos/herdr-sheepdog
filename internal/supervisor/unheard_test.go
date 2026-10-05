package supervisor

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestUnheardPushReachesTheBrainAsAFact is docs/history/bench-2026-10-03.md
// #17/#18: a push the CEO did not hear (run 2, 00:48:12: an approval cut
// the answer after its first phrase) becomes a transcript line the
// brain sees on its next turn, with the part that was heard; it decides
// whether to mention it. A line the voice re-queued is not reported.
func TestUnheardPushReachesTheBrainAsAFact(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Claude added a delete operation.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateIdle)
	h.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{
		Text: "claude 改的是删除功能：购物车里把某件商品整件拿掉。", Heard: "claude 改的是删除功能：购物车里把",
		Reason: voice.UnheardInterruptedByPush, Tags: map[string]any{"push_id": "push-tmb3k8-369"},
	}})
	h.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{Text: "测试跑完了，数量出来是三，应该是五。"}, Requeue: true})
	h.say("你继续解释 claude。")
	calls := m.Calls()
	if len(calls) == 0 {
		t.Fatal("no brain turn")
	}
	text := calls[len(calls)-1].Text()
	if !strings.Contains(text, `the user did not hear: "claude 改的是删除功能：购物车里把某件商品整件拿掉。" (only "claude 改的是删除功能：购物车里把" reached them)`) {
		t.Errorf("the brain was not told:\n%s", text)
	}
	if strings.Contains(text, "数量出来是三") {
		t.Errorf("a re-queued line was reported to the brain:\n%s", text)
	}
}

// TestOpenCheckFollowsThePendingPrompt: the voice layer's open check
// says a block's announcement is still open while its prompt is the
// pending one, and not once the CEO answered it; a kept promise is
// always owed.
func TestOpenCheckFollowsThePendingPrompt(t *testing.T) {
	m := braintest.NewModel() // event ids come with the brain
	m.EventDefault = withAsk(announceReply("Auth wants to write hello.txt.", "interrupt", "", "", true), "Approve?")
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	pushes := h.pusher.all()
	if len(pushes) == 0 {
		t.Fatal("no block announcement")
	}
	tags := h.pusher.tagsOf(len(pushes) - 1)
	if tags[tagEventID] == nil {
		t.Fatalf("announcement tags = %v, want an event id", tags)
	}
	if !h.sup.OpenCheck(nil) || !h.sup.OpenCheck(tags) {
		t.Fatal("an open prompt reported closed")
	}
	if h.sup.OpenCheck(map[string]any{tagEventID: "evt-other"}) {
		t.Error("another event's line reported open")
	}
	h.say("yes")
	if _, has := h.pending(); has {
		t.Fatal("the prompt is still pending after yes")
	}
	if h.sup.OpenCheck(nil) || h.sup.OpenCheck(tags) {
		t.Error("an answered prompt reported open")
	}
	if !h.sup.OpenCheck(map[string]any{voice.TagKeepsPromiseIDs: []string{"pr-1"}}) {
		t.Error("a kept promise reported closed")
	}
}

// TestOpenCheckIgnoresAnInstructionAwaitingConfirmation: only a worker's
// prompt counts as a question waiting for the CEO (CEO, 2026-10-03); an
// instruction awaiting confirmation does not hold a session refresh.
func TestOpenCheckIgnoresAnInstructionAwaitingConfirmation(t *testing.T) {
	h := newHarness(t)
	if err := h.store.SetPending(state.PendingAction{ID: "si-1", PaneID: "w5:p1", Kind: state.PendingSendInstruction, PayloadText: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	if h.sup.OpenCheck(nil) {
		t.Error("a send_instruction pending counted as an open question")
	}
}
