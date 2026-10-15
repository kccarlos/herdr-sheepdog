package voice

import (
	"testing"
	"time"
)

// Push texts and output transcripts from run 2 (2026-10-03), as logged.
const (
	// 00:50:54.318 push-tmb3os-403: read as the question and the
	// options; the body was skipped (docs/dev/history/bench-2026-10-03.md #19).
	run2OptionsPush = "只有一件：claude 要准备跑整套检查，停在那里等你一句许可。其他的都不用你管，codex 那个失败还挂着，但没在催你。. 这一条测试命令让它跑吗？? Options: 允许，就这一次; 允许，以后这条命令不用再问; 不允许."
	run2OptionsSaid = "这一条测试命令让它跑吗？可以选择：允许，就这一次；允许，以后这条命令不用再问；不允许。"
	// 00:51:17.082 push-tmb3ot-404, voiced the same way.
	run2AnswerPush = "已经替你把「只这一次」点了，claude 正在收尾写 README 那步。结果一出来我就告诉你。."
	run2AnswerSaid = "已经替你把「只这一次」点了，claude 正在收尾写 README 那步。结果一出来我就告诉你。"
)

// TestPushCoverageOnRun2Lines: coverage counts Chinese per character
// and leaves an options list aside, so a body read whole is ~1 and the
// logged body-skipped line (#19) is well under the threshold.
func TestPushCoverageOnRun2Lines(t *testing.T) {
	if c := PushCoverage(run2AnswerPush, run2AnswerSaid); c < 0.99 {
		t.Errorf("a line read whole: coverage %.2f", c)
	}
	if c := PushCoverage(run2OptionsPush, run2OptionsSaid); c >= HeardMinCoverage {
		t.Errorf("the body-skipped line: coverage %.2f, want < %.2f", c, HeardMinCoverage)
	}
	if c := PushCoverage("bench-claude is asking do you want to proceed.", "bench-claude is asking do you want to proceed."); c != 1 {
		t.Errorf("an English line read whole: coverage %.2f", c)
	}
	if c := PushCoverage(run2AnswerPush, "它加了一个删除操作，删不存在的商品会报错；"); c > 0.1 {
		t.Errorf("an unrelated answer: coverage %.2f", c)
	}
}

// TestHeardTrackerIgnoresTheInterruptionItsOwnPushCauses: Gemini reports
// "interrupted" right after an interrupting push and sends a stray
// turnComplete (run 2, 00:48:12.310 and .417) before it voices the push;
// neither may judge the push that has not started yet.
func TestHeardTrackerIgnoresTheInterruptionItsOwnPushCauses(t *testing.T) {
	var h HeardTracker
	now := time.Now()
	h.Pushed(now, "codex asks to run the tests. Allow it?", nil, false)
	if got := h.Interrupted(now, "model", false); len(got) != 0 {
		t.Fatalf("judged on its own interruption: %+v", got)
	}
	h.TurnComplete()
	if got := h.Settle(now, false); len(got) != 0 {
		t.Fatalf("judged on a stray turnComplete: %+v", got)
	}
	h.Said("codex asks to run the tests. Allow it?")
	h.TurnComplete()
	if got := h.Settle(now, false); len(got) != 0 {
		t.Fatalf("a heard line judged unheard: %+v", got)
	}
}

// TestHeardTrackerTimesOutAPushNeverVoiced: nothing else closes a watch
// the model silently dropped; it is judged not_voiced after the timeout.
func TestHeardTrackerTimesOutAPushNeverVoiced(t *testing.T) {
	var h HeardTracker
	now := time.Now()
	h.Pushed(now, "docs finished the readme.", map[string]any{"push_id": "p1"}, false)
	if got := h.Settle(now.Add(heardTimeout-time.Second), false); len(got) != 0 {
		t.Fatalf("judged early: %+v", got)
	}
	got := h.Settle(now.Add(heardTimeout+time.Second), false)
	if len(got) != 1 || got[0].Reason != UnheardNotVoiced || got[0].Coverage != 0 {
		t.Fatalf("verdicts = %+v", got)
	}
	if p := got[0].Payload(); p["push_id"] != "p1" || p["coverage"] != 0.0 || p["reason"] != UnheardNotVoiced {
		t.Errorf("payload = %v", p)
	}
}

// RequeueHold on collision-finish-push r1's timeline (seconds from the
// call start): the detector opened at 30.0 with the push playing, the
// first fragment at 30.3, the detector closed at 32.7 and its release
// ran out at 33.0 — where the old rule re-queued — the words came at
// 33.4, the reply turn ended at 35.0.
func TestRequeueHoldCollisionTimeline(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	var sp ClientSpeech
	sp.Set(at(30.0), true)
	f := RequeueFacts{LastInput: at(30.3), ReplyDoneAt: at(29.0), Speech: sp}
	if got := RequeueHold(at(31), f); got != "speaking" {
		t.Errorf("31.0: %q, want speaking", got)
	}
	f.Speech.Set(at(32.7), false)
	if got := RequeueHold(at(33.0), f); got != "speech_settle" {
		t.Errorf("33.0: %q, want speech_settle", got)
	}
	f.LastInput = at(33.4)
	if got := RequeueHold(at(34.8), f); got != "awaiting_reply" {
		t.Errorf("34.8: %q, want awaiting_reply", got)
	}
	f.ReplyDoneAt = at(35.0)
	if got := RequeueHold(at(35.1), f); got != "" {
		t.Errorf("35.1: %q, want free", got)
	}
	// Speech the detector heard after the last words waits for its
	// transcript, but not forever.
	f.Speech.Set(at(36.0), true)
	f.Speech.Set(at(37.0), false)
	if got := RequeueHold(at(39.5), f); got != "awaiting_transcript" {
		t.Errorf("39.5: %q, want awaiting_transcript", got)
	}
	if got := RequeueHold(at(41.5), f); got != "" {
		t.Errorf("41.5: %q, want free", got)
	}
	// A reply that never comes holds only RequeueReplyWait.
	g := RequeueFacts{LastInput: at(50), ReplyDoneAt: at(40)}
	if got := RequeueHold(at(61), g); got != "" {
		t.Errorf("61: %q, want free", got)
	}
}
