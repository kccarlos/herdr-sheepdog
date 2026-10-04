package supervisor

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// played is the voice session sending the last queued push to the model
// (voice.EventPush): only then is it spoken, as far as the relay gate is
// concerned (inputs.go, markPushed).
func (h *harness) played() {
	h.t.Helper()
	last := h.pusher.last(h.t)
	h.sup.OnVoiceEvent(voice.EventPush{Text: last.Text, Urgency: last.Urgency})
}

// The 2026-10-02 23:54:18 sequence (docs/bench-2026-10-03.md #10): the
// shepherd's instruction arrives while a late reply is queued; the voice
// relays the instruction before that reply is sent to it. The call is
// the shepherd's, so it is accepted; the voice relaying the reply's own
// question back afterwards is refused as a self-relay.
func TestQueuedPushDoesNotHideTheShepherdsWords(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	// 18.277: the shepherd speaks (Gemini never marks it final).
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "然后让 Claude 加一个删除功能。"})
	// 18.746: the voice's own "on it".
	h.clock.Advance(470 * time.Millisecond)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "on it"})
	// 19.220: the previous utterance's reply is queued, not yet spoken.
	h.clock.Advance(470 * time.Millisecond)
	reply := "已发给 Codex:让它先把那个失败的测试跑出来,先不动任何文件。. 要它跑完直接动手修吗?"
	h.sup.push("w5:p1", reply, voice.UrgencyIdle)
	// 19.414: the voice relays the instruction.
	h.clock.Advance(200 * time.Millisecond)
	h.relay("然后让 Claude 加一个删除功能。")
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 0 {
		t.Fatalf("the shepherd's instruction was ignored (%d)", n)
	}
	ev := h.log.ofType("supervisor.relay")
	if len(ev) != 1 || payload(ev[0])["heard"] != "然后让 Claude 加一个删除功能。" {
		t.Fatalf("supervisor.relay = %v", ev)
	}
	// 21.9: the reply is sent to the model and spoken.
	h.clock.Advance(2500 * time.Millisecond)
	h.played()
	// 22.924: the voice relays the reply's question back: no new CEO
	// words, so no brain turn; the voice is told so as a silent fact.
	h.clock.Advance(time.Second)
	res := h.relay("要它跑完直接动手修吗?")
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 1 {
		t.Errorf("voice_initiative_ignored = %d, want 1", n)
	}
	if res.Scheduling != voice.UrgencySilent || res.Spoken == "" {
		t.Errorf("result = %+v, want a silent fact", res)
	}
}

// A briefing sent between the shepherd's words and the voice's relay of
// them no longer hides those words (dual-brain ablation G2): the call
// claims them, with no fuzzy rescue needed; a second call with no new
// words is the voice's own.
func TestBriefingDoesNotMoveTheGate(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.hear("tell claude to add a delete feature")
	h.clock.Advance(time.Second)
	h.sup.push("w5:p1", "Auth finished the tests.", voice.UrgencyIdle)
	h.played()
	h.clock.Advance(time.Second)
	h.relay("Tell Claude to add a delete feature.")
	ev := h.log.ofType("supervisor.relay")
	if len(ev) != 1 || payload(ev[0])["heard"] != "tell claude to add a delete feature" {
		t.Fatalf("relay = %v", ev)
	}
	h.relay("Auth finished the tests.")
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 1 {
		t.Errorf("ignored = %d, want 1", n)
	}
}
