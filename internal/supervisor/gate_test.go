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
	// 22.924: the voice relays the reply's question back.
	h.clock.Advance(time.Second)
	h.relay("要它跑完直接动手修吗?")
	if n := h.log.count("supervisor.self_relay"); n != 1 {
		t.Errorf("self_relay = %d, want 1", n)
	}
	if n := h.log.count("supervisor.voice_initiative_ignored") + h.log.count("supervisor.relay_rescued"); n != 0 {
		t.Errorf("ignored+rescued = %d, want 0", n)
	}
}

// The safety net: a push really was sent between the shepherd's words
// and the voice's relay of them, so the gate refuses the call; its text
// is the shepherd's own words of a moment ago, so it is accepted
// (supervisor.relay_rescued). Other text, a bare "yes", or words past
// the rescue window stay refused.
func TestGatedRelayOfRecentShepherdWordsIsRescued(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.hear("tell claude to add a delete feature")
	h.clock.Advance(time.Second)
	h.sup.push("w5:p1", "Auth finished the tests.", voice.UrgencyIdle)
	h.played()
	h.clock.Advance(time.Second)
	h.relay("Tell Claude to add a delete feature.")
	ev := h.log.ofType("supervisor.relay_rescued")
	if len(ev) != 1 || payload(ev[0])["heard"] != "tell claude to add a delete feature" {
		t.Fatalf("relay_rescued = %v", ev)
	}
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 0 {
		t.Errorf("ignored = %d", n)
	}
	// Claimed now: the same relay again is the model's own.
	h.relay("Tell Claude to add a delete feature.")
	if n := h.log.count("supervisor.relay_rescued"); n != 1 {
		t.Errorf("rescued twice: %d", n)
	}

	// Different words: refused.
	h.hear("what is docs doing")
	h.sup.push("w5:p1", "Auth is idle.", voice.UrgencyIdle)
	h.played()
	h.relay("Approve the deploy.")
	// A bare yes: refused even though the shepherd said it.
	h.hear("yes")
	h.sup.push("w5:p1", "Auth asks to run the diff.", voice.UrgencyIdle)
	h.played()
	h.relay("yes")
	// Past the window: refused.
	h.hear("run the migration on staging")
	h.sup.push("w5:p1", "Docs finished.", voice.UrgencyIdle)
	h.played()
	h.clock.Advance(11 * time.Second)
	h.relay("run the migration on staging")
	if n := h.log.count("supervisor.relay_rescued"); n != 1 {
		t.Errorf("rescued = %d, want 1", n)
	}
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 4 {
		t.Errorf("ignored = %d, want 4", n)
	}
}

func TestSameWordsAndPartOf(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"然后让 Claude 加一个删除功能。", "然后让Claude加一个删除功能", true},
		{"Tell Claude to add a delete feature.", "tell claude to add a delete feature", true},
		{"tell claude to add a delete feature please", "tell claude to add a delete feature", true},
		{"Approve the deploy.", "what is docs doing", false},
		{"好", "好", false},
	} {
		if got := sameWords(c.a, c.b); got != c.want {
			t.Errorf("sameWords(%q, %q) = %v", c.a, c.b, got)
		}
	}
	push := "Claude 的删除功能写好了,一直在等你准它动手。. 先准 Claude 动手吗? Options: 准 Claude 动手; 让 Codex 也去修; 先都不动"
	if !partOf("先准 Claude 动手吗? 准 Claude 动手; 让 Codex 也去修; 先都不动.", push) {
		t.Error("the push's question and options were not recognised")
	}
	if partOf("然后让 Claude 加一个删除功能。", push) {
		t.Error("the shepherd's own instruction was taken for the push")
	}
}
