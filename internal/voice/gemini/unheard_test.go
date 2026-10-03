package gemini

import (
	"context"
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
// client's flushed report, with the part that was heard; an answer to
// the shepherd's question is owed (campaign D N3), so it waits to be
// re-queued once the approval is over. Gemini's own
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
	if !ev.Requeue || ev.Push.Reason != voice.UnheardInterruptedByPush || voice.PushID(ev.Push.Tags) != "push-tmb3k8-369" {
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
// body_skipped. It answers the shepherd's question, so it is owed and
// waits to be re-queued (campaign D N3).
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
	if !ev.Requeue || ev.Push.Reason != voice.UnheardBodySkipped || ev.Push.Coverage >= voice.HeardMinCoverage {
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

// TestRequeueWaitsForTheShepherdsWholeTurn is collision-finish-push r1
// (docs/bench/tier2-2026-10-03.md #5): the CEO talked over a finish
// push; it was cut and re-queued the moment the client's detector had
// closed and released, before the CEO's input transcript and the
// voice's reply to it, and the re-sent push was cut again. It now waits
// for the detector to settle, the words, and the reply to play out.
func TestRequeueWaitsForTheShepherdsWholeTurn(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	line := "Bench-claude is done. It added the changelog entry you asked for, fixing the parser ignoring its first argument."
	if err := h.s.PushTagged(line, voice.UrgencyIdle, map[string]any{"push_id": "push-finish", "needs_user": true}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "finish push", isContent(line))
	speakReply(h, c, []string{"Bench-claude", "is", "done."}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)

	// The CEO starts talking over it: the detector opens, a first
	// fragment arrives, the model yields and the client flushes.
	h.s.ClientSpeaking(true)
	c.serve(inputTranscriptMsg("Also,", false))
	c.serve(interruptedMsg())
	h.waitEvent("model interrupted", func(voice.Event) bool { return len(h.log.payloads("voice.interrupted")) >= 1 })
	h.s.ClientPlayback(voice.PlaybackFlushed, 600)
	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if !ev.Requeue || ev.Push.Reason != voice.UnheardUserTurn {
		t.Fatalf("unheard = %+v", ev)
	}
	c.serve(turnCompleteMsg())

	// The CEO keeps talking for a while, then the detector closes.
	time.Sleep(2200 * time.Millisecond)
	h.s.ClientSpeaking(false)
	// Past the old rule (detector release, 2 s since the first fragment
	// and since the flush): still held — the rest of the words and the
	// reply are to come.
	time.Sleep(1200 * time.Millisecond)
	if n := countSent(c, line); n != 1 {
		t.Fatalf("re-sent %d times while the CEO's turn was still open", n-1)
	}
	c.serve(inputTranscriptMsg(" did it touch the changelog?", false))
	time.Sleep(1200 * time.Millisecond)
	if n := countSent(c, line); n != 1 {
		t.Fatalf("re-sent before the reply to the CEO's words")
	}
	speakReply(h, c, []string{"Checking."}, 400)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(turnCompleteMsg())
	h.s.ClientPlayback(voice.PlaybackDrained, 400)
	waitCount(t, "re-queued sends", 2, 6*time.Second, func() int { return countSent(c, line) })
}

// Campaign D lang-mixed-codeswitch r1 (docs/eval/campaign-d-2026-10-03.md
// N3), as logged.
const n3Answer = "Bench Codex 说 test 还没过。 TestAdd 失败了，加 2 和 3 得到的是 3，不是 5，它自己说之前那句全过其实只跑了另一部分。 要让它把这个 bug 修掉，然后再跑一次 test 吗？"

// selfRelayRefused answers a to_supervisor call the way the supervisor
// refuses a self relay: nothing to say.
func selfRelayRefused(context.Context, voice.ToolCall) (voice.ToolResult, error) {
	time.Sleep(50 * time.Millisecond)
	return voice.ToolResult{Scheduling: voice.UrgencySilent}, nil
}

// TestUnheardAnswerIsRequeued is lang-mixed-codeswitch r1: the answer to
// the CEO's question went out at 11010 ms, the voice said "Checking."
// and relayed the answer's own text back with to_supervisor (refused as
// supervisor.self_relay). The answer was judged unheard (body_skipped)
// and, not being "owed", dropped: 50 s of silence followed. An answer is
// now owed: re-queued once the moment is quiet, and the voice relaying
// its own text does not count as the CEO asking something else.
func TestUnheardAnswerIsRequeued(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{Handler: selfRelayRefused}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	tags := map[string]any{"push_id": "push-tmc27d-4", voice.TagAnswersUtterance: "utt-tmc279-3", "call_id": "call_135658"}
	if err := h.s.PushTagged(n3Answer, voice.UrgencyIdle, tags); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "answer", isContent(n3Answer))
	speakReply(h, c, []string{"Checking."}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(toolCallMsg("call_135666", voice.ToolToSupervisor, map[string]any{"text": n3Answer}))
	c.serve(turnCompleteMsg())
	h.s.ClientPlayback(voice.PlaybackDrained, 300)

	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if !ev.Requeue || ev.Push.Reason != voice.UnheardBodySkipped || ev.Push.Heard != "Checking." {
		t.Fatalf("unheard = %+v", ev)
	}
	waitCount(t, "re-queued sends", 2, 6*time.Second, func() int { return countSent(c, n3Answer) })
	if rq := h.log.payloads("voice.push_requeued"); len(rq) != 1 || rq[0]["push_id"] != "push-tmc27d-4" || rq[0]["reason"] != voice.UnheardBodySkipped {
		t.Errorf("voice.push_requeued = %v", rq)
	}
	if d := h.log.payloads("voice.push_dropped"); len(d) != 0 {
		t.Errorf("voice.push_dropped = %v", d)
	}
}

// TestStaleAnswerGoesToTheBrain: an answer cut by the CEO is owed, but
// when the CEO goes on to ask something else (barge-in-long-answer r1:
// "Okay, okay. Just tell me if it needs anything", relayed), it is not
// replayed after the reply to that: it is dropped (asked_since) and the
// supervisor is told it was not heard (EventPushUnheard, Requeue false).
func TestStaleAnswerGoesToTheBrain(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{Handler: selfRelayRefused}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	answer := "Bench Claude wrote the README and is checking its links now. It found two broken links, one to the setup page and one to the API reference."
	if err := h.s.PushTagged(answer, voice.UrgencyIdle, map[string]any{"push_id": "push-a", voice.TagAnswersUtterance: "utt-1"}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "answer", isContent(answer))
	speakReply(h, c, []string{"Bench Claude wrote the README"}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(inputTranscriptMsg("Okay, okay. Just tell me if it needs anything.", false))
	c.serve(interruptedMsg())
	h.waitEvent("model interrupted", func(voice.Event) bool { return len(h.log.payloads("voice.interrupted")) >= 1 })
	h.s.ClientPlayback(voice.PlaybackFlushed, 300)
	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if !ev.Requeue || ev.Push.Reason != voice.UnheardUserTurn {
		t.Fatalf("unheard = %+v", ev)
	}
	c.serve(toolCallMsg("call-2", voice.ToolToSupervisor, map[string]any{"text": "Okay, okay. Just tell me if it needs anything."}))
	speakReply(h, c, []string{"Checking."}, 300)
	c.serve(turnCompleteMsg())

	waitCount(t, "dropped", 1, 6*time.Second, func() int { return len(h.log.payloads("voice.push_dropped")) })
	if d := h.log.payloads("voice.push_dropped"); d[0]["reason"] != voice.DropAskedSince || d[0]["push_id"] != "push-a" {
		t.Errorf("voice.push_dropped = %v", d)
	}
	got := unheardEvents(h)
	if len(got) != 2 || got[1].Requeue || got[1].Push.Text != answer {
		t.Errorf("unheard events = %+v, want the drop told as unheard", got)
	}
	if n := countSent(c, answer); n != 1 {
		t.Errorf("answer sent %d times, want once", n)
	}
}

// TestRequeuedLineAlreadyHeardIsDropped is #111's collision-finish-push
// arm A r1: the kept-promise finish line went out at 19035 ms, the CEO
// talked over it after "It added the changelog note about", asked "did
// it touch the changelog?", and the voice answered from what it had
// voiced: "Yes, it added the changelog note about the fix to the first
// argument." At 31000 ms the re-queued line replayed the same news. It
// is now dropped (voice.push_dropped {reason: already_heard}).
func TestRequeuedLineAlreadyHeardIsDropped(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	line := "bench-claude is done. It added the changelog note about the fix to the first argument. Shall I have it run the tests to confirm the fix holds?"
	tags := map[string]any{"push_id": "push-tmc1yd-8", "event_id": "evt-tmc1yb-7", "agent": "bench-claude", voice.TagKeepsPromiseIDs: []string{"pr-tmc1y1-4"}}
	if err := h.s.PushTagged(line, voice.UrgencyIdle, tags); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "finish push", isContent(line))
	speakReply(h, c, []string{"Bench-claude is done.", "It added the changelog note about"}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	h.s.ClientSpeaking(true)
	c.serve(interruptedMsg())
	h.waitEvent("model interrupted", func(voice.Event) bool { return len(h.log.payloads("voice.interrupted")) >= 1 })
	h.s.ClientPlayback(voice.PlaybackFlushed, 600)
	ev := h.waitEvent("unheard", isUnheard).(voice.EventPushUnheard)
	if !ev.Requeue || ev.Push.Reason != voice.UnheardUserTurn {
		t.Fatalf("unheard = %+v", ev)
	}
	c.serve(turnCompleteMsg())
	h.s.ClientSpeaking(false)
	c.serve(inputTranscriptMsg("Also, did it touch the changelog?", false))
	speakReply(h, c, []string{"Yes, it added the changelog note", "about the fix to the first argument."}, 400)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(turnCompleteMsg())
	h.s.ClientPlayback(voice.PlaybackDrained, 800)

	waitCount(t, "dropped", 1, 8*time.Second, func() int { return len(h.log.payloads("voice.push_dropped")) })
	if d := h.log.payloads("voice.push_dropped"); d[0]["reason"] != voice.DropAlreadyHeard || d[0]["push_id"] != "push-tmc1yd-8" {
		t.Errorf("voice.push_dropped = %v", d)
	}
	if n := countSent(c, line); n != 1 {
		t.Errorf("finish line sent %d times, want once", n)
	}
	if got := unheardEvents(h); len(got) != 1 {
		t.Errorf("unheard events = %d, want 1 (already heard is not news for the brain)", len(got))
	}
}
