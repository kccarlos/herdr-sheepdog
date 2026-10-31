package web

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// speakTurn emits fragments of a model turn that is still running and
// waits for the hub to have recorded them.
func speakTurn(t *testing.T, h *harness, frags ...string) {
	t.Helper()
	var all string
	for _, f := range frags {
		h.session.emit(voice.EventTranscript{Dir: voice.DirOut, Text: f})
		all += f
	}
	waitFor(t, time.Second, func() bool {
		h.srv.hub.mu.Lock()
		defer h.srv.hub.mu.Unlock()
		return strings.HasSuffix(h.srv.hub.turnOut, all)
	}, "hub recorded the turn's transcript")
}

// handoffPushes returns the texts of the pushes tagged as a handoff.
func handoffPushes(h *harness) []string {
	var out []string
	texts := h.session.pushed()
	for i, tags := range h.session.taggedPushes() {
		if tags[voice.TagHandoff] == true {
			out = append(out, texts[i])
		}
	}
	return out
}

func TestHandoffReplaceMidTurnHoldsUnheardText(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)
	speakTurn(t, h, "The tests passed ", "and the branch is clean.")

	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)
	first.expect(wsproto.TypeStatus) // replaced

	waitFor(t, time.Second, func() bool { return len(handoffPushes(h)) == 1 }, "one handoff push")
	got := handoffPushes(h)[0]
	if !strings.HasSuffix(got, "The tests passed and the branch is clean.") {
		t.Fatalf("handoff push = %q", got)
	}
	if u := h.session.pushUrgencies(); !reflect.DeepEqual(u, []voice.Urgency{voice.UrgencyIdle}) {
		t.Fatalf("handoff urgency = %v, want idle", u)
	}
	if !subsequence(h.types(), []string{"web.turn_handoff", "web.client.attach"}) {
		t.Fatalf("events = %v", h.types())
	}
}

func TestHandoffOnlyAfterTheAcknowledgedPosition(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)
	speakTurn(t, h, "First sentence. ")
	first.send(wsproto.Playback(voice.PlaybackDrained, 1200))
	waitFor(t, time.Second, func() bool { return len(h.session.playbackReports()) == 1 }, "drained reported")
	speakTurn(t, h, "Second sentence.")

	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)
	waitFor(t, time.Second, func() bool { return len(handoffPushes(h)) == 1 }, "one handoff push")
	got := handoffPushes(h)[0]
	if !strings.HasSuffix(got, "Second sentence.") || strings.Contains(got, "First") {
		t.Fatalf("handoff push = %q, want only the part after the drained report", got)
	}
}

func TestHandoffFinishedTurnLeavesNone(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)
	speakTurn(t, h, "All done.")
	h.session.emit(voice.EventTurnComplete{})
	waitFor(t, time.Second, func() bool {
		h.srv.hub.mu.Lock()
		defer h.srv.hub.mu.Unlock()
		return h.srv.hub.turnOut == ""
	}, "turn ended")

	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)
	first.expect(wsproto.TypeStatus) // replaced
	_ = second.conn.CloseNow()
	waitFor(t, 2*time.Second, func() bool { return !h.session.isAttached() }, "detached")
	if got := h.session.pushed(); len(got) != 0 {
		t.Fatalf("pushes after a finished turn = %q", got)
	}
}

func TestHandoffNoTurnNoPush(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)
	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)
	first.expect(wsproto.TypeStatus) // replaced
	if got := h.session.pushed(); len(got) != 0 {
		t.Fatalf("pushes with no turn running = %q", got)
	}
}

func TestHandoffDetachMidTurn(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	speakTurn(t, h, "I was saying something long.")

	_ = c.conn.CloseNow()
	waitFor(t, 2*time.Second, func() bool { return len(handoffPushes(h)) == 1 }, "one handoff push")
	if !strings.HasSuffix(handoffPushes(h)[0], "I was saying something long.") {
		t.Fatalf("handoff push = %q", handoffPushes(h))
	}
	if h.session.isAttached() {
		t.Fatal("the push must follow the detach, so the session holds it")
	}

	// What the model generates for nobody after the detach is handed off
	// when its turn ends.
	speakTurn(t, h, " And then more.")
	h.session.emit(voice.EventTurnComplete{})
	waitFor(t, time.Second, func() bool { return len(handoffPushes(h)) == 2 }, "tail handed off")
	if got := handoffPushes(h)[1]; !strings.HasSuffix(got, "And then more.") || strings.Contains(got, "long") {
		t.Fatalf("tail push = %q", got)
	}
}

func TestHandoffSameTurnReplacedTwiceHandsOffOnce(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)
	speakTurn(t, h, "A single reply.")

	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)
	first.expect(wsproto.TypeStatus) // replaced
	waitFor(t, time.Second, func() bool { return len(handoffPushes(h)) == 1 }, "first handoff")
	third := dialWS(t, h)
	third.expect(wsproto.TypeStatus)
	second.expect(wsproto.TypeStatus) // replaced
	waitFor(t, time.Second, func() bool {
		n := 0
		for _, ty := range h.types() {
			if ty == "web.client.attach" {
				n++
			}
		}
		return n == 2
	}, "second replacement")
	if got := handoffPushes(h); len(got) != 1 {
		t.Fatalf("handoff pushes = %q, want one", got)
	}
}

// The chat item keeps the full text: the handoff push is not a chat item.
func TestHandoffKeepsTheChatItem(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	h.log.Log("voice", "voice.transcript.out", "", map[string]any{"text": "The whole reply.", "final": false})
	h.log.Log("voice", "voice.push", "", map[string]any{"text": "say the rest", "urgency": "when_idle", voice.TagHandoff: true})
	var resp ConversationResponse
	if code := h.getJSON("/api/conversation", &resp); code != 200 {
		t.Fatalf("conversation = %d", code)
	}
	if len(resp.Items) != 1 || resp.Items[0].Kind != "dog" || resp.Items[0].Text != "The whole reply." {
		t.Fatalf("items = %+v", resp.Items)
	}
}
