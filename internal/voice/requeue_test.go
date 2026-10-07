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

// TestRequeueOwesAnswers is campaign D N3: an answer to the user's
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
// out after it was sent, or the user handed on new words (to_supervisor,
// stop) after it was sent; the voice relaying the answer's own text
// back (lang-mixed-codeswitch r1, supervisor.self_relay) is not new
// words. A stale answer is dropped and the orchestrator told; a fresh one goes
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
		// The voice calls again with the question the answer answers
		// (A/B 2026-10-03, barge-in-long-answer r3: "On it.", the same
		// words again, the body never read): not new words.
		{"same question again", func(r *Requeue) {
			r.Asked(rqAt(9000), "Is the test passing?")
			r.Asked(rqAt(12000), "is the test passing? ")
		}, ""},
		{"same question, then a new one", func(r *Requeue) {
			r.Asked(rqAt(9000), "is the test passing?")
			r.Asked(rqAt(12000), "is the test passing?")
			r.Asked(rqAt(17675), "Okay, okay. Just tell me if it needs anything.")
		}, DropAskedSince},
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

// TestRequeueStaleOnlyForAnswers: a decision or a kept promise the user
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

// TestRequeueAlreadyHeard replays the re-queued lines of the bench logs
// (#111's collision-finish-push arms, campaign D, unheard-approval-requeue
// and collision-block-while-talking): what the user heard of the line,
// then everything the voice said until the re-queue. A line whose
// content the voice retold since is dropped; one it did not is re-queued.
func TestRequeueAlreadyHeard(t *testing.T) {
	keeps := map[string]any{TagKeepsPromiseIDs: []string{"pr-1"}, "event_id": "evt-1", "agent": "bench-claude"}
	needs := map[string]any{"needs_user": true, "event_id": "evt-2", "agent": "bench-codex"}
	cases := []struct {
		name, text, heard, since string
		tags                     map[string]any
		drop                     bool
	}{
		{"annfix-a r1", "bench-claude is done. It added the changelog note about the fix to the first argument. Shall I have it run the tests to confirm the fix holds?",
			"Bench-claude is done. It added the changelog note about", "Yes, it added the changelog note about the fix to the first argument.", keeps, true},
		{"annfix-a r2", "BenchClaude is done. It added the changelog note about the fix you asked for, saying the add function had been ignoring its first number.",
			"BenchClaude is done. It added the changelog note", "Yes, it added a note to the changelog explaining the bug fix for the add function.", keeps, true},
		{"annfix-a r3", "BenchClad is done. It added a changelog note saying the addition bug ignored its first number.",
			"BenchClad is done. It added a changelog", "Yes, it added a note to the changelog explaining the bug with the addition fix.", keeps, true},
		{"annfix-b r1", "BenchClad is done. It added the changelog note for the fix, the one you asked it for. That's the last thing it was asked to do. Want me to have it run the tests to confirm?",
			"BenchClad is done. It added the changelog note for the", "Yes, it added the changelog note for the fix as you asked.", keeps, true},
		{"campaign D r1", "BenchClaude is done. It added the changelog entry you asked for, saying the fix stops the adder from ignoring its first number.",
			"BenchClaude is done. It added", "Yes, it added the changelog entry you asked for, saying the fix stops the adder from ignoring its first number.", keeps, true},
		{"approval retold", "bench-codex has finished moving the parser. It wants to run the tests to confirm the change kept them green, and it is waiting on you to approve that.",
			"it.bench-codex has finished moving the parser.", "Yes, bench-codex is waiting for approval to run the tests. Options are: Yes, proceed; or No.", needs, true},
		{"nothing said since", "Bench Clad is done. It added the changelog note for the fix you asked about, and it is waiting to know what is next. Shall I have it run the tests?",
			"Bench Claude is done. It added", "", keeps, false},
		{"reply about something else", "bench-codex squashed the last four commits and now wants to force push them over the remote main branch. Force push over main?",
			"Checking on bench-claude's progress with the pull", "Checking on the readme timeline for you.About bench-claude: No finish time is on record. The README itself is already written, bench-claude is just checking the links inside it, a minute or so in.", needs, false},
		{"only an ack since", "Codex wants to run the full test suite on the parser refactor before it finishes. It is waiting on a yes or no from you. Approve the test run?",
			"Codex wants to run", "On it.", needs, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r Requeue
			r.Add(UnheardPush{Text: tc.text, Tags: tc.tags, Heard: tc.heard, Reason: UnheardUserTurn, SentAt: rqAt(1000)})
			r.Said(tc.since)
			rel := r.Take()
			if got := rel[0].Drop == DropAlreadyHeard; got != tc.drop || rel[0].Tell {
				t.Errorf("Take = %+v (coverage %.2f → %.2f), want already_heard %v", rel[0],
					ContentCoverage(tc.text, tc.heard), ContentCoverage(tc.text, tc.heard+" "+tc.since), tc.drop)
			}
		})
	}
}

// TestContentCoverage: content words, any order, options aside.
func TestContentCoverage(t *testing.T) {
	if c := ContentCoverage("It added the changelog note.", "a note to the changelog was added"); c != 1 {
		t.Errorf("retold = %.2f", c)
	}
	if c := ContentCoverage("Approve the test run? Options: yes; restart.", "the test run"); c < 0.66 || c > 0.67 {
		t.Errorf("options aside = %.2f", c)
	}
	if c := ContentCoverage("it is the one", "nothing"); c != 1 {
		t.Errorf("no content words = %.2f", c)
	}
	if c := ContentCoverage("测试跑完了", "测试还没跑"); c < 0.5 || c >= 1 {
		t.Errorf("han = %.2f", c)
	}
}

// TestRequeueSaidAcrossAdds: words said between two verdicts stay with
// the first line, and the slice growing does not break it (the
// 2026-10-03 bench crashed on a strings.Builder copied by append).
func TestRequeueSaidAcrossAdds(t *testing.T) {
	var r Requeue
	line := "bench-claude is done. It added the changelog note."
	r.Add(UnheardPush{Text: line, Tags: map[string]any{"needs_user": true}, SentAt: rqAt(1)})
	r.Said("Yes, it added ")
	for i := 0; i < 5; i++ {
		r.Add(UnheardPush{Text: "docs is idle.", Tags: map[string]any{"needs_user": true}, SentAt: rqAt(2)})
		r.Said("the changelog note. ")
	}
	rel := r.Take()
	if len(rel) != 6 || rel[0].Drop != DropAlreadyHeard {
		t.Fatalf("Take = %+v", rel)
	}
}

// TestRequeueStaleBeforeAlreadyHeard is the fix arm's
// barge-in-long-answer r1: the user cut an answer off, asked something
// else, and a later answer repeated its words. The cut answer is stale
// (the orchestrator is told), not "already heard".
func TestRequeueStaleBeforeAlreadyHeard(t *testing.T) {
	var r Requeue
	v := answerPush("It wrote the README you asked for, covering what the project does. It also spotted two broken links in the contributing guide.", "utt-3", 11904)
	v.Heard = "It wrote the README you asked for, covering what the project does"
	r.Add(v)
	r.Asked(rqAt(20074), "Okay, okay. Just tell me if it needs anything from me.")
	r.Said("It listed three points. What changed: new README. What needs a decision: the two broken links in the contributing guide.")
	if rel := r.Take(); rel[0].Drop != DropAskedSince || !rel[0].Tell {
		t.Fatalf("Take = %+v", rel)
	}
}
