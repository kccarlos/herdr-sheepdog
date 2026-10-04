package voice

import (
	"strings"
	"testing"
)

// TestRecapRecentKeepsTheNewestWithinBudget: newest first within the
// budget, returned oldest first, the oldest included cut to its tail.
func TestRecapRecentKeepsTheNewestWithinBudget(t *testing.T) {
	r := Recap{Max: 3}
	r.Add(RecapUser, "dropped by Max")
	r.Add(RecapUser, "0123456789")
	r.Add(RecapAssistant, "abcde")
	r.Add(RecapSupervisor, "  ")
	r.Add(RecapSupervisor, "XYZ")
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}
	got := r.Recent(12)
	if len(got) != 3 || got[0].Text != "6789" || got[1].Text != "abcde" || got[2].Text != "XYZ" {
		t.Errorf("Recent = %+v", got)
	}
	if lines := RecapLines(got); lines != "Shepherd: 6789\nYou: abcde\nSupervisor update: XYZ" {
		t.Errorf("RecapLines = %q", lines)
	}
}

// TestRecapCutKeepsWhatWasHeard: a cut reply is replaced by its heard
// part; a reply heard not at all leaves the recap.
func TestRecapCutKeepsWhatWasHeard(t *testing.T) {
	r := Recap{}
	r.Add(RecapAssistant, "Auth is running the tests and then it deploys.")
	if !r.Cut("Auth is running the tests and then it deploys.", "Auth is running", 1500) {
		t.Fatal("no entry cut")
	}
	if lines := RecapLines(r.Recent(100)); !strings.HasPrefix(lines, "You (cut off after 2 s): Auth is running") {
		t.Errorf("lines = %q", lines)
	}
	r.Add(RecapAssistant, "Docs finished.")
	r.Cut("Docs finished.", "", 0)
	if got := r.Recent(100); len(got) != 1 {
		t.Errorf("an unheard reply stayed: %+v", got)
	}
}

// TestRecapLeavesOutAcksAndSelfRelays is docs/history/bench-2026-10-03.md #9:
// the re-seed of a fresh session must not teach it the degraded
// session's habits — the bare "On it." / "checking" / "好的" it said
// while relaying a push's own question, its "<no speech detected>"
// placeholder, and the relayed question itself recorded as if the
// shepherd had asked it.
func TestRecapLeavesOutAcksAndSelfRelays(t *testing.T) {
	r := Recap{}
	r.Add(RecapSupervisor, "测试跑完了，数量出来是三，应该是五。要我直接让 codex 把它修掉吗？")
	r.Add(RecapAssistant, "On it.")
	r.Add(RecapUser, "要我直接让 codex 把它修掉吗？") // the push's question heard back
	r.Add(RecapAssistant, "checking")
	r.Add(RecapAssistant, "好的。")
	r.Add(RecapAssistant, "<no speech detected>")
	r.Add(RecapUser, "让 codex 修掉吗") // short: the shepherd's own answer, kept
	r.Add(RecapUser, "让 codex 修。")
	r.Add(RecapAssistant, "On it, telling codex to fix the count now.")
	got := RecapLines(r.Recent(1000))
	want := "Supervisor update: 测试跑完了，数量出来是三，应该是五。要我直接让 codex 把它修掉吗？\n" +
		"Shepherd: 让 codex 修掉吗\n" +
		"Shepherd: 让 codex 修。\n" +
		"You: On it, telling codex to fix the count now."
	if got != want {
		t.Errorf("recap =\n%s\nwant\n%s", got, want)
	}
	for _, ack := range []string{"On it.", "on it", "Checking", "好的", "收到。", "<no speech detected>"} {
		if !BareAck(ack) {
			t.Errorf("BareAck(%q) = false", ack)
		}
	}
	if BareAck("Docs finished.") {
		t.Error("BareAck took a real line")
	}
}

// The focus slot keeps the latest line, apart from the entries, and a
// reset forgets it.
func TestRecapFocusSlot(t *testing.T) {
	var r Recap
	r.SetFocus(" first ")
	r.SetFocus("second")
	r.Add(RecapUser, "how are the tests?")
	if r.Focus() != "second" || r.Len() != 1 {
		t.Fatalf("focus = %q, entries = %d", r.Focus(), r.Len())
	}
	if !IsFocus(map[string]any{TagFocus: true}) || IsFocus(nil) {
		t.Error("IsFocus")
	}
	r.Reset()
	if r.Focus() != "" || r.Len() != 0 {
		t.Errorf("after reset: focus = %q, entries = %d", r.Focus(), r.Len())
	}
}
