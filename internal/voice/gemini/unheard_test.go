package gemini

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Run 2 (2026-10-03) lines, as logged.
const (
	// 00:48:38.760 push-tmb3kw-377, a kept promise: the CEO spoke 0.5 s
	// later and the model answered them instead (bench #17).
	run2KeptPush = "测试跑完了，结果不好：那个相加数量的问题还是失败的，数量出来是三，应该是五。这跟你之前听到的「全过」不一样。. 要我把这条失败原样念给你听，还是直接让 codex 把它修掉？?"
	run2CEOWords = "你继续解释 claude。"
	run2Answer   = "它加了一个删除操作，删不存在的商品会报错；"
	// 00:48:08.655 push-tmb3k8-369, the answer an approval cut at
	// 00:48:12.247 after its first phrase (bench #18), and that
	// approval, push-tmb3kc-372.
	run2CutAnswer = "claude 改的是删除功能：购物车里把某件商品整件拿掉。它加了一个删除操作，删不存在的商品会报错；这几条它都写了检查并且通过。. 要不要我让 claude 把整套检查再跑一遍，看看它的删除功能和 codex 刚修的那处合在一起是否都干净？?"
	run2Approval  = "codex 停在一步上：它要你允许它运行测试命令。就是你刚让它做的那件事，点一下就能开跑。. 允许它运行吗？?"
	// 00:50:54.318 push-tmb3os-403 and what was voiced of it (bench #19).
	run2Options     = "只有一件：claude 要准备跑整套检查，停在那里等你一句许可。其他的都不用你管，codex 那个失败还挂着，但没在催你。. 这一条测试命令让它跑吗？? Options: 允许，就这一次; 允许，以后这条命令不用再问; 不允许."
	run2OptionsSaid = "这一条测试命令让它跑吗？可以选择：允许，就这一次；允许，以后这条命令不用再问；不允许。"
)

func isUnheard(ev voice.Event) bool { _, ok := ev.(voice.EventPushUnheard); return ok }

func unheardEvents(h *harness) []voice.EventPushUnheard {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	var out []voice.EventPushUnheard
	for _, ev := range h.events {
		if u, ok := ev.(voice.EventPushUnheard); ok {
			out = append(out, u)
		}
	}
	return out
}

// countSent counts the content messages carrying text.
func countSent(c *fakeConn, text string) int {
	n := 0
	for _, m := range c.sentMsgs() {
		if isContent(text)(m) {
			n++
		}
	}
	return n
}

// waitCount polls until f() >= n, for up to within.
func waitCount(t *testing.T, what string, n int, within time.Duration, f func() int) {
	t.Helper()
	deadline := time.Now().Add(within)
	for f() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d, want %d", what, f(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPushLostToTheShepherdsTurnIsRequeued is run 2's 00:48:38: a kept
// promise went out, the CEO spoke 0.5 s later, and the model answered
// them; the line was never voiced and nothing noticed. It is now judged
// unheard (user_turn) once that answer has played, and, being owed,
// re-queued by the session after the shepherd's turn is over — once:
// heard the second time, nothing more is said about it.
func TestPushLostToTheShepherdsTurnIsRequeued(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	tags := map[string]any{"push_id": "push-tmb3kw-377", "event_id": "evt-tmb3ku-376", "agent": "bench-codex", voice.TagKeepsPromiseIDs: []string{"pr-tmb3jq-364"}}
	if err := h.s.PushTagged(run2KeptPush, voice.UrgencyIdle, tags); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "kept promise", isContent(run2KeptPush))
	c.serve(inputTranscriptMsg(run2CEOWords, false))
	c.serve(audioMsg(outAudio(200)))
	c.serve(outputTranscriptMsg(run2Answer))
	h.waitEvent("answer", func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Text == run2Answer })
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	if n := h.countEvents(isUnheard); n != 0 {
		t.Fatalf("judged while the answer was still playing: %d", n)
	}
	h.s.ClientPlayback(voice.PlaybackDrained, 200)

	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if !ev.Requeue || ev.Push.Reason != voice.UnheardUserTurn || ev.Push.Coverage > 0.1 || ev.Push.Text != run2KeptPush {
		t.Fatalf("unheard = %+v", ev)
	}
	p := h.log.payloads("voice.push_unheard")
	if len(p) != 1 || p[0]["push_id"] != "push-tmb3kw-377" || p[0]["event_id"] != "evt-tmb3ku-376" || p[0]["agent"] != "bench-codex" || p[0]["requeue"] != true {
		t.Fatalf("voice.push_unheard = %v", p)
	}

	waitCount(t, "re-queued sends", 2, 6*time.Second, func() int { return countSent(c, run2KeptPush) })
	if rq := h.log.payloads("voice.push_requeued"); len(rq) != 1 || rq[0]["push_id"] != "push-tmb3kw-377" {
		t.Errorf("voice.push_requeued = %v", rq)
	}
	pushes := h.log.payloads("voice.push")
	if len(pushes) != 2 || pushes[1]["requeued"] != true {
		t.Errorf("voice.push = %v", pushes)
	}
	c.serve(outputTranscriptMsg(run2KeptPush))
	c.serve(turnCompleteMsg())
	time.Sleep(300 * time.Millisecond)
	if n := h.countEvents(isUnheard); n != 1 {
		t.Errorf("unheard events = %d, want 1 (the re-queued line was heard)", n)
	}
}

// TestInterruptPushCutsAnAnswerAndIsReported is run 2's 00:48:12: an
// approval (needs_user, interrupt) cut a spoken answer after its first
// phrase. The answer is judged unheard (interrupted_by_push) on the
// client's flushed report, with the part that was heard; it is not a
// line the voice re-queues, so it goes to the supervisor. Gemini's own
// "interrupted" for the approval and the stray turnComplete before it is
// voiced do not count against the approval, which is then heard whole.
func TestInterruptPushCutsAnAnswerAndIsReported(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged(run2CutAnswer, voice.UrgencyIdle, map[string]any{"push_id": "push-tmb3k8-369", voice.TagAnswersUtterance: "utt-tmb3k5-368"}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "answer", isContent("claude 改的是删除功能"))
	speakReply(h, c, []string{"claude 改的是删除功能：", "购物车里把", "某件商品整件"}, 1000)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)

	if err := h.s.PushTagged(run2Approval, voice.UrgencyInterrupt, map[string]any{"push_id": "push-tmb3kc-372", "event_id": "evt-tmb3ka-370", "needs_user": true}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "approval", isContent(run2Approval))
	h.s.ClientPlayback(voice.PlaybackFlushed, 2000)

	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if ev.Requeue || ev.Push.Reason != voice.UnheardInterruptedByPush || voice.PushID(ev.Push.Tags) != "push-tmb3k8-369" {
		t.Fatalf("unheard = %+v", ev)
	}
	if heard := strings.ReplaceAll(ev.Push.Heard, " ", ""); heard != "claude改的是删除功能：购物车里把" {
		t.Errorf("heard = %q, want only the played part", ev.Push.Heard)
	}

	// Gemini's echo of the interruption, the client's empty flush and
	// the stray turnComplete, then the approval voiced whole.
	c.serve(interruptedMsg())
	h.waitEvent("model interrupted", func(voice.Event) bool { return len(h.log.payloads("voice.interrupted")) >= 2 })
	h.s.ClientPlayback(voice.PlaybackFlushed, 0)
	c.serve(turnCompleteMsg())
	speakReply(h, c, []string{run2Approval}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(turnCompleteMsg())
	h.s.ClientPlayback(voice.PlaybackDrained, 300)
	time.Sleep(200 * time.Millisecond)
	if got := unheardEvents(h); len(got) != 1 {
		t.Errorf("unheard events = %+v, want only the cut answer", got)
	}
}

// TestBodySkippedIsReported is run 2's 00:50:54: a line with options
// read as the question and the options alone (#19); unheard,
// body_skipped, not re-queued.
func TestBodySkippedIsReported(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged(run2Options, voice.UrgencyIdle, map[string]any{"push_id": "push-tmb3os-403", voice.TagAnswersUtterance: "utt-tmb3oo-401"}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "options line", isContent("只有一件"))
	c.serve(outputTranscriptMsg(run2OptionsSaid))
	c.serve(turnCompleteMsg())
	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if ev.Requeue || ev.Push.Reason != voice.UnheardBodySkipped || ev.Push.Coverage >= voice.HeardMinCoverage {
		t.Fatalf("unheard = %+v", ev)
	}
}

// TestRequeueDroppedWhenNoLongerOpen: an announced prompt (the code
// sentence carries only its event id; the supervisor's open check says
// it is the open one) missed while the CEO spoke is re-queued only if
// it is still open when the moment comes; answered meanwhile, it is
// dropped with voice.push_dropped {no_longer_open}.
func TestRequeueDroppedWhenNoLongerOpen(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	open := make(chan bool, 1)
	open <- true
	h.s.SetOpenCheck(func(tags map[string]any) bool {
		v := <-open
		open <- v
		return v && tags != nil && tags["event_id"] == "evt-tmb3ol-398"
	})
	line := "bench-claude is asking do you want to proceed."
	if err := h.s.PushTagged(line, voice.UrgencyIdle, map[string]any{"push_id": "push-tmb3on-400", "event_id": "evt-tmb3ol-398"}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "block line", isContent(line))
	c.serve(inputTranscriptMsg("anything waiting for me?", false))
	c.serve(outputTranscriptMsg("Checking."))
	c.serve(turnCompleteMsg())
	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if !ev.Requeue || ev.Push.Reason != voice.UnheardUserTurn {
		t.Fatalf("unheard = %+v", ev)
	}
	<-open
	open <- false // the CEO answered it meanwhile
	waitCount(t, "dropped", 1, 6*time.Second, func() int { return len(h.log.payloads("voice.push_dropped")) })
	if p := h.log.payloads("voice.push_dropped"); p[0]["reason"] != "no_longer_open" || p[0]["push_id"] != "push-tmb3on-400" {
		t.Errorf("voice.push_dropped = %v", p)
	}
	if n := countSent(c, line); n != 1 {
		t.Errorf("sent %d times, want once", n)
	}
}
