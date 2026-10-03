package voice

import (
	"testing"
	"time"
)

var rqT0 = time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC)

func rqAt(ms int) time.Time { return rqT0.Add(time.Duration(ms) * time.Millisecond) }

func answerPush(text, utt string, sentMs int) UnheardPush {
	return UnheardPush{Text: text, Tags: map[string]any{TagAnswersUtterance: utt, TagPushID: "push-" + utt}, Reason: UnheardBodySkipped, SentAt: rqAt(sentMs)}
}

// TestRequeueOwesAnswers is campaign D N3: an answer to the CEO's
// question is owed like a decision or a kept promise; a re-queue never
// is; a plain news line only when the open check says so.
func TestRequeueOwesAnswers(t *testing.T) {
	var r Requeue
	if !r.Owed(answerPush("TestAdd fails: 2 plus 3 gives 3.", "utt-1", 11010), false) {
		t.Error("answer not owed")
	}
	re := answerPush("bench-codex asks to run go test.", "utt-2", 40999)
	re.Requeued = true
	if r.Owed(re, true) {
		t.Error("a re-queue is owed again")
	}
	news := UnheardPush{Text: "docs is idle.", Tags: map[string]any{TagPushID: "p"}, SentAt: rqAt(1)}
	if r.Owed(news, false) || !r.Owed(news, true) {
		t.Error("news line: owed only when open")
	}
	if !r.Owed(UnheardPush{Text: "x", Tags: map[string]any{"needs_user": true}}, false) {
		t.Error("needs_user not owed")
	}
	if OpenCheckDecides(map[string]any{TagAnswersUtterance: "utt-1"}) || OpenCheckDecides(map[string]any{TagKeepsPromiseIDs: []string{"pr-1"}}) ||
		!OpenCheckDecides(map[string]any{"event_id": "evt-1"}) {
		t.Error("OpenCheckDecides")
	}
}

// TestRequeueStaleAnswer: an answer is stale when a newer answer went
// out after it was sent, or the CEO handed on new words (to_supervisor,
// stop) after it was sent; the voice relaying the answer's own text
// back (lang-mixed-codeswitch r1, supervisor.self_relay) is not new
// words. A stale answer is dropped and the brain told; a fresh one goes
// back in the queue.
func TestRequeueStaleAnswer(t *testing.T) {
	const ans = "Bench Codex 说 test 还没过。 TestAdd 失败了，加 2 和 3 得到的是 3，不是 5。 要让它把这个 bug 修掉吗？"
	cases := []struct {
		name string
		do   func(r *Requeue)
		drop string
	}{
		{"nothing since", func(r *Requeue) {}, ""},
		{"self relay", func(r *Requeue) { r.Asked(rqAt(12193), ans) }, ""},
		{"own send", func(r *Requeue) { r.Pushed(rqAt(11010), map[string]any{TagAnswersUtterance: "utt-1"}) }, ""},
		{"news push since", func(r *Requeue) { r.Pushed(rqAt(15000), map[string]any{"agent": "bench-claude"}) }, ""},
		{"newer answer", func(r *Requeue) { r.Pushed(rqAt(15000), map[string]any{TagAnswersUtterance: "utt-1"}) }, DropSuperseded},
		{"answer to a newer question", func(r *Requeue) { r.Pushed(rqAt(15000), map[string]any{TagAnswersUtterance: "utt-2"}) }, DropSuperseded},
		{"new question", func(r *Requeue) { r.Asked(rqAt(17675), "Okay, okay. Just tell me if it needs anything.") }, DropAskedSince},
		{"stop", func(r *Requeue) { r.Asked(rqAt(15017), "") }, DropAskedSince},
		{"asked before it was sent", func(r *Requeue) { r.Asked(rqAt(9000), "is the test passing?") }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r Requeue
			v := answerPush(ans, "utt-1", 11010)
			if !r.Owed(v, false) {
				t.Fatal("not owed")
			}
			r.Add(v)
			tc.do(&r)
			rel := r.Take()
			if len(rel) != 1 || rel[0].Drop != tc.drop || rel[0].Tell != (tc.drop != "") {
				t.Fatalf("Take = %+v, want drop %q", rel, tc.drop)
			}
			if r.Len() != 0 {
				t.Error("Take left lines")
			}
		})
	}
	// A line already stale at the verdict is not owed at all.
	var r Requeue
	r.Pushed(rqAt(15000), map[string]any{TagAnswersUtterance: "utt-2"})
	if r.Owed(answerPush(ans, "utt-1", 11010), false) {
		t.Error("stale answer owed")
	}
}

// TestRequeueStaleOnlyForAnswers: a decision or a kept promise the CEO
// talked over is still owed after they ask something else (#108's
// collision case); only answers go stale.
func TestRequeueStaleOnlyForAnswers(t *testing.T) {
	var r Requeue
	v := UnheardPush{Text: "bench-codex wants to force push to main. Approve?", Tags: map[string]any{"needs_user": true}, Reason: UnheardUserTurn, SentAt: rqAt(1000)}
	r.Add(v)
	r.Asked(rqAt(3000), "what is bench-claude doing?")
	r.Pushed(rqAt(5000), map[string]any{TagAnswersUtterance: "utt-3"})
	if rel := r.Take(); len(rel) != 1 || rel[0].Drop != "" {
		t.Fatalf("Take = %+v", rel)
	}
}
