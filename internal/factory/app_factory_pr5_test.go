package factory

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// Pins app-factory.md §6.1, §6.2 & §6.3: owner_say wakes go first together
// before queued task wakes (even past the 20-wake batch cap), typed Say uses
// single-line typed wording with a 1,000-char cap, stopped projects hold Say
// until resume, and Say never answers an open gated ask or mints a proof.
func TestOwnerSayWakesFirstTogetherAndCapped(t *testing.T) {
	t.Parallel()

	t.Run("before_queued_task_wakes_across_batch_cap", func(t *testing.T) {
		t.Parallel()
		h := newProjectHarness(t, "Owner wakes first")
		h.pass() // J7 starts coord1
		coord1 := h.state().Project.RunID
		h.setCoordWorking(coord1)
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
			for i := 1; i <= 22; i++ {
				s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: fmt.Sprintf("worker_failed@%d", i), Task: fmt.Sprintf("T%d", i), Why: "N23: worker failed", Facts: fmt.Sprintf("title %q", fmt.Sprintf("task %d", i)), At: h.now})
			}
		})
		for _, msg := range []string{"first owner instruction", "second\nowner instruction", "third owner instruction"} {
			if err := h.eng.Say(h.id, msg); err != nil {
				t.Fatalf("Say(%q): %v", msg, err)
			}
		}
		h.coordIdleWithStatus(coord1, "idle") // J12 delivers one bounded batch
		sends := sendsTo(t, h.srv.Requests(), coord1)
		if len(sends) != 1 {
			t.Fatalf("coord sends = %d, want 1 batch:\n%q", len(sends), sends)
		}
		msg, prev := sends[0], -1
		for _, want := range []string{`owner said (typed): "first owner instruction"`, `owner said (typed): "second owner instruction"`, `owner said (typed): "third owner instruction"`, "- T1:"} {
			pos := strings.Index(msg, want)
			if pos < 0 || pos <= prev {
				t.Fatalf("wake batch missing or misordered %q (pos=%d prev=%d):\n%s", want, pos, prev, msg)
			}
			prev = pos
		}
		if strings.Contains(msg, "relayed by the orchestrator") || strings.Contains(msg, "Heard:") || strings.Contains(msg, "- T18:") {
			t.Errorf("wake batch leaked relay wording or exceeded batch cap:\n%s", msg)
		}
		rem := h.state().Project.Wakes
		if len(rem) != 5 || slices.ContainsFunc(rem, func(w flow.Wake) bool { return strings.HasPrefix(w.Key, "owner_say:") }) || rem[0].Task != "T18" {
			t.Errorf("remaining wakes = %+v, want 5 task wakes starting at T18", rem)
		}
		if j12 := logRules(t, h.paths.Log(h.id), "J12"); len(j12) != 1 || len(j12[0].Asked) != maxFactsWakes ||
			!strings.HasPrefix(j12[0].Asked[0], "project:owner_say:") || !strings.HasPrefix(j12[0].Asked[2], "project:owner_say:") {
			t.Errorf("J12 log = %+v, want 3 owner_say keys first of 20", j12)
		}
		if err := h.eng.Say(h.id, strings.Repeat("é", 1001)); err == nil || len(h.state().Project.Wakes) != 5 {
			t.Errorf("Say(>1000 runes) = %v (wakes=%d), want refusal and unchanged wakes", err, len(h.state().Project.Wakes))
		}
	})

	t.Run("stopped_holds_until_resume_and_open_ask_stays_open", func(t *testing.T) {
		t.Parallel()
		h := newProjectHarness(t, "Stopped say")
		h.edit(func(s *flow.State) {
			s.Asks = []flow.Ask{{ID: "ask-rel", Project: h.id, Folder: h.repo, Kind: flow.KindRelease, Gated: true, Question: "Tag 0.2.0 now?", Options: []string{"Yes, tag 0.2.0", "Hold"}, Recommend: "Hold", At: h.now, Status: flow.AskStatusOpen}}
		})
		if err := h.eng.Stop(h.id); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if err := h.eng.Say(h.id, "Yes, tag 0.2.0"); err != nil {
			t.Fatalf("Say on stopped project: %v", err)
		}
		if open := h.eng.Asks(true); len(open) != 1 || open[0].ID != "ask-rel" || open[0].Status != flow.AskStatusOpen {
			t.Fatalf("open asks after Say = %+v, want ask-rel still open", open)
		}
		if _, err := os.Stat(h.paths.proofs()); !os.IsNotExist(err) {
			t.Errorf("Say minted proofs file: stat err=%v", err)
		}
		if code := refusalCode(h.eng.Answer("ask-rel", "Yes, tag 0.2.0", AnswerProof{})); code != "proof_required" {
			t.Errorf("unproved Answer after Say = %q, want proof_required", code)
		}
		if err := h.eng.Start(h.repo, "Stopped say", 0); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		h.now = h.now.Add(time.Duration(h.state().Settings.CoordinatorGapMinutes+1) * time.Minute)
		h.pass() // J7 starts coordinator
		coord := h.state().Project.RunID
		h.coordIdleWithStatus(coord, "idle")
		if sends := sendsTo(t, h.srv.Requests(), coord); len(sends) != 1 || !strings.Contains(sends[0], `owner said (typed): "Yes, tag 0.2.0"`) {
			t.Fatalf("resumed coordinator sends = %q, want typed Say wake", sends)
		}
	})
}

// Pins app-factory.md §6.4: coordinator.md states that an "owner said" wake is
// an instruction to act on (or open an ask), not wait in conversation, and
// never the answer to an open ask (only the card answers).
func TestCoordinatorPromptHasOwnerSayRule(t *testing.T) {
	t.Parallel()
	var line string
	for ln := range strings.Lines(coordinatorTemplate) {
		if strings.Contains(strings.ToLower(ln), "owner said") {
			line = strings.ToLower(ln)
			break
		}
	}
	if line == "" {
		t.Fatalf("coordinator.md missing an \"owner said\" wake rule")
	}
	for _, want := range []string{"ask", "never", "card"} {
		if !strings.Contains(line, want) {
			t.Errorf("coordinator.md \"owner said\" rule missing %q:\n%s", want, line)
		}
	}
}

// Pins Carbon F1: NormalizeSay refuses text > 1,000 runes, but caps a long
// daemon-attached heard transcript instead of rejecting a short instruction.
func TestNormalizeSayCapsLongHeardWithoutRefusingShortText(t *testing.T) {
	t.Parallel()

	longHeard := "prefix-" + strings.Repeat("h", 1050)
	gotText, gotHeard, ref := NormalizeSay("skip the export task", longHeard)
	if ref != nil {
		t.Fatalf("NormalizeSay(short text, %d-rune heard) refused: %+v", utf8.RuneCountInString(longHeard), ref)
	}
	if gotText != "skip the export task" {
		t.Errorf("text = %q, want %q", gotText, "skip the export task")
	}
	if gotHeard == "" || utf8.RuneCountInString(gotHeard) > 1000 || !strings.HasPrefix(gotHeard, "prefix-"+strings.Repeat("h", 50)) {
		t.Errorf("heard = %q (runes=%d), want non-empty prefix capped at <= 1000 runes", gotHeard, utf8.RuneCountInString(gotHeard))
	}
	if _, _, ref := NormalizeSay(strings.Repeat("t", 1001), "short"); ref == nil || ref.Code != "text_too_long" {
		t.Errorf("NormalizeSay(>1000-rune text) = %+v, want text_too_long refusal", ref)
	}

	h := newProjectHarness(t, "Long heard")
	if _, err := h.eng.SayByID(context.Background(), h.id, "skip the export task", longHeard); err != nil {
		t.Fatalf("SayByID(short text, long heard): %v", err)
	}
	wakes := h.state().Project.Wakes
	if len(wakes) != 1 || !strings.HasPrefix(wakes[0].Key, "owner_say:") {
		t.Fatalf("wakes = %+v, want 1 owner_say wake", wakes)
	}
	if strings.Contains(wakes[0].Facts, "\n") || !strings.Contains(wakes[0].Facts, "skip the export task") {
		t.Errorf("wake facts = %q, want single line containing short instruction", wakes[0].Facts)
	}
}

// Pins Carbon F4 / Neon observation 1: sayLocked checks the last rune
// (including CJK sentence-final punctuation) rather than the last UTF-8 byte,
// so relayed Chinese instructions do not get a spurious ASCII period ("。.").
func TestSayByIDRelayedCJKPunctuationNoExtraPeriod(t *testing.T) {
	t.Parallel()

	h := newProjectHarness(t, "CJK say")
	cases := []struct {
		text, heard, wantPrefix, forbid string
	}{
		{text: "跳过导出。", heard: "让笔记工厂跳过导出。", wantPrefix: "跳过导出。 Heard: ", forbid: "。."},
		{text: "跳过导出？", heard: "要跳过导出吗？", wantPrefix: "跳过导出？ Heard: ", forbid: "？."},
		{text: "跳过导出！", heard: "立刻跳过导出！", wantPrefix: "跳过导出！ Heard: ", forbid: "！."},
		{text: "skip export", heard: "please skip export", wantPrefix: "skip export. Heard: "},
	}
	for i, tc := range cases {
		if _, err := h.eng.SayByID(context.Background(), h.id, tc.text, tc.heard); err != nil {
			t.Fatalf("case %d SayByID(%q): %v", i, tc.text, err)
		}
		wakes := h.state().Project.Wakes
		facts := wakes[len(wakes)-1].Facts
		if !strings.HasPrefix(facts, tc.wantPrefix) {
			t.Errorf("case %d facts = %q, want prefix %q", i, facts, tc.wantPrefix)
		}
		if tc.forbid != "" && strings.Contains(facts, tc.forbid) {
			t.Errorf("case %d facts = %q, contains spurious %q", i, facts, tc.forbid)
		}
	}
}
