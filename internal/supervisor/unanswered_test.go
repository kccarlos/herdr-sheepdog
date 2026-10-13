package supervisor

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// unansweredIDs are the ids the Unanswered section of what the model
// saw lists ("" text: none).
var unansweredLine = regexp.MustCompile(`(?m)^- (utt-[^,]+), `)

func unansweredIn(text string) []string {
	_, sec, ok := strings.Cut(text, "## Unanswered")
	if !ok {
		return nil
	}
	if i := strings.Index(sec, "\n## "); i >= 0 {
		sec = sec[:i]
	}
	var ids []string
	for _, m := range unansweredLine.FindAllStringSubmatch(sec, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// settleEarlier is a model step that answers and names every unanswered
// question it was shown in answers_earlier.
func settleEarlier(spoken string) braintest.Step {
	return braintest.Step{Fn: func(c braintest.Call) (*genai.GenerateContentResponse, error) {
		b, _ := json.Marshal(map[string]any{"spoken": spoken, "detail": "", "answers_earlier": unansweredIn(c.Text())})
		return braintest.Response(braintest.Step{Text: string(b)}), nil
	}}
}

func recordedIDs(h *harness) []string {
	var ids []string
	for _, e := range h.log.ofType("supervisor.unanswered_recorded") {
		ids = append(ids, payload(e)["utterance_id"].(string))
	}
	return ids
}

func clearedBy(h *harness) map[string]string {
	out := map[string]string{}
	for _, e := range h.log.ofType("supervisor.unanswered_cleared") {
		p := payload(e)
		out[p["utterance_id"].(string)] = p["by"].(string)
	}
	return out
}

// (a) A turn that falls back leaves the user's words unanswered: the
// next turn's state block lists them with what happened, and they stay
// until a reply names them in answers_earlier.
func TestFallbackIsRecordedUntilTheOrchestratorSettlesIt(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Err: braintest.ErrBoom},
		braintest.Step{Text: braintest.Reply("Docs is idle.", "")},
		settleEarlier("About your earlier question: the tests failed on a timeout."),
		braintest.Step{Text: braintest.Reply("Nothing new.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)

	if res := h.say("why did auth's tests fail?"); res.Spoken != brainFailedLine {
		t.Fatalf("result = %+v, want the fallback", res)
	}
	ids := recordedIDs(h)
	if len(ids) != 1 {
		t.Fatalf("recorded = %v", ids)
	}
	if p := payload(h.log.ofType("supervisor.unanswered_recorded")[0]); p["case"] != "fallback" || p["text"] != "why did auth's tests fail?" {
		t.Errorf("record = %v", p)
	}
	h.clock.Advance(40 * time.Second)
	h.say("what is docs doing?")
	h.clock.Advance(10 * time.Second)
	h.say("anything else?")
	h.clock.Advance(10 * time.Second)
	h.say("ok")

	calls := m.Calls()
	if len(calls) != 4 {
		t.Fatalf("model calls = %d", len(calls))
	}
	for i, want := range [][]string{nil, ids, ids, nil} {
		if got := unansweredIn(calls[i].Text()); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("call %d listed %v, want %v", i, got, want)
		}
	}
	second := calls[1].Text()
	// The age is rendered with the orchestrator's clock (brain tests).
	if !strings.Contains(second, `"why did auth's tests fail?" (your turn on it failed: `) {
		t.Errorf("the section lacks the words or the reason:\n%s", second)
	}
	if by := clearedBy(h); by[ids[0]] != "model" {
		t.Errorf("cleared = %v, want by the model", by)
	}
}

// A retry of the same words that answers (rerelay.go) is the answer:
// nothing stays recorded.
func TestRetryThatAnswersClearsTheRecord(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Err: braintest.ErrBoom},
		braintest.Step{Text: braintest.Reply("Keep your own adapter.", "")},
		braintest.Step{Text: braintest.Reply("Nothing new.", "")},
	)
	h := newBrainHarness(t, m)
	h.say(incidentWords)
	h.clock.Advance(time.Second)
	if res := h.relay(incidentWords); res.Spoken != "Keep your own adapter." {
		t.Fatalf("retry = %+v", res)
	}
	h.clock.Advance(time.Second)
	h.say("thanks")
	ids := recordedIDs(h)
	if len(ids) != 1 || clearedBy(h)[ids[0]] != "retry" {
		t.Errorf("recorded %v, cleared %v", ids, clearedBy(h))
	}
	if got := unansweredIn(m.Calls()[2].Text()); len(got) != 0 {
		t.Errorf("listed after the retry answered: %v", got)
	}
}

// The runaway rails: a record expires unansweredMaxAge after it was
// asked, and at most unansweredKeep are kept (the oldest goes).
func TestUnansweredExpiresAndIsCapped(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Err: braintest.ErrBoom},
		braintest.Step{Text: braintest.Reply("Docs is idle.", "")},
	)
	m.Default = &braintest.Step{Err: braintest.ErrBoom}
	h := newBrainHarness(t, m)
	h.say("first question?")
	h.clock.Advance(unansweredMaxAge + time.Second)
	h.say("what is docs doing?")
	if got := unansweredIn(m.Calls()[1].Text()); len(got) != 0 {
		t.Errorf("an expired question was listed: %v", got)
	}
	if by := clearedBy(h); by[recordedIDs(h)[0]] != "expiry" {
		t.Errorf("cleared = %v", by)
	}
	for _, q := range []string{"a?", "b?", "c?", "d?"} {
		h.clock.Advance(5 * time.Second)
		h.say(q)
	}
	if got := h.sup.unansweredNow(); len(got) != unansweredKeep || got[0].text != "b?" || got[2].text != "d?" {
		t.Errorf("kept = %+v", got)
	}
}

// (b) The user talked over the answer before most of it played: the
// cut run, tagged with the utterance it answered, records the question
// with what they heard. A cut near the end, an untagged run (another
// line) or a flush for an urgent push records nothing.
func TestCutAnswerIsRecorded(t *testing.T) {
	// See also TestCutCountsOnceAndByText.
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth's tests failed on a timeout in the payment step.", "")})
	m.Default = &braintest.Step{Text: braintest.Reply("ok", "")}
	h := newBrainHarness(t, m)
	res := h.say("why did auth's tests fail?")
	id := res.UtteranceID
	if id == "" {
		t.Fatal("no utterance id on the result")
	}
	cut := func(c voice.TranscriptCut, heard string) {
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: heard, Final: true, Cut: &c})
	}
	// A cut near the end, a run that played another line, or a flush
	// for an urgent push: nothing.
	cut(voice.TranscriptCut{UtteranceID: "", HeardMs: 200, RunMs: 4000}, "")
	cut(voice.TranscriptCut{UtteranceID: id, HeardMs: 200, RunMs: 4000, ByPush: true}, "")
	if n := len(recordedIDs(h)); n != 0 {
		t.Fatalf("recorded %d for cuts that do not count", n)
	}
	// The run's length says nothing (the voice streams): the share of
	// the answer's text heard does.
	cut(voice.TranscriptCut{UtteranceID: id, HeardMs: 1000, RunMs: 1100}, "Auth's tests")
	if ids := recordedIDs(h); len(ids) != 1 || ids[0] != id {
		t.Fatalf("recorded = %v, want %s", ids, id)
	}
	h.say("and docs?")
	text := m.Calls()[1].Text()
	if !strings.Contains(text, `"why did auth's tests fail?" (the user talked over its answer after 1 s; they heard only "Auth's tests")`) {
		t.Errorf("section:\n%s", text)
	}
}

// (c) A late answer the voice layer judged unheard and did not re-queue
// is recorded with the user's words; a re-queued one is not (it will be
// said), nor is a news line.
func TestUnheardAnswerIsRecorded(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	h.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{Text: "Auth is done.", Tags: map[string]any{"event_id": "evt-1"}, Reason: voice.UnheardNotVoiced}})
	h.sup.OnVoiceEvent(voice.EventPushUnheard{Requeue: true, Push: voice.UnheardPush{Text: "x", Tags: map[string]any{"answers_utterance": "utt-1", "answers_text": "is auth done?"}}})
	if n := len(recordedIDs(h)); n != 0 {
		t.Fatalf("recorded %d", n)
	}
	h.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{Text: "x", Tags: map[string]any{"answers_utterance": "utt-1", "answers_text": "is auth done?"}, Reason: voice.UnheardNotVoiced}})
	got := h.sup.unansweredNow()
	if len(got) != 1 || got[0].id != "utt-1" || got[0].text != "is auth done?" || got[0].why != whyNeverVoiced {
		t.Errorf("records = %+v", got)
	}
}

// (c) A late reply dropped unsaid because the user asked something else
// first leaves the older question unanswered.
func TestStaleDroppedReplyIsRecorded(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(
		braintest.Step{Block: release, Text: braintest.Reply("The tests are green.", "")},
		braintest.Step{Text: braintest.Reply("Docs is idle.", "")},
	)
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	assertPending(t, h.say("how is auth doing"))
	assertPending(t, h.say("what about docs"))
	close(release)
	h.sup.replies.Wait()
	got := h.sup.unansweredNow()
	if len(got) != 1 || got[0].text != "how is auth doing" || got[0].why != whyStale {
		t.Errorf("records = %+v", got)
	}
}

// "New conversation" forgets every record.
func TestResetForgetsUnanswered(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Err: braintest.ErrBoom})
	h := newBrainHarness(t, m)
	h.say("why did it fail?")
	if len(h.sup.unansweredNow()) != 1 {
		t.Fatal("not recorded")
	}
	if _, _, err := h.sup.ResetConversation(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if got := h.sup.unansweredNow(); len(got) != 0 {
		t.Errorf("after reset: %+v", got)
	}
}

// Only the first cut of an answer counts (the tail of the same barge-in
// flushes as a second, tiny run), and an answer heard for at least half
// its text is not recorded.
func TestCutCountsOnceAndByText(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth's tests failed on a timeout in the payment step.", "")})
	m.Default = &braintest.Step{Text: braintest.Reply("ok", "")}
	h := newBrainHarness(t, m)
	id := h.say("why did auth's tests fail?").UtteranceID
	cut := func(heard string) {
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: heard, Final: true, Cut: &voice.TranscriptCut{UtteranceID: id, HeardMs: 2000, RunMs: 2100}})
	}
	cut("Auth's tests failed on a timeout in the")
	cut("")
	if n := len(recordedIDs(h)); n != 0 {
		t.Errorf("recorded %d: most of the answer was heard, and the tail cut is the same barge-in", n)
	}
}
