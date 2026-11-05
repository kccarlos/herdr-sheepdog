package state

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The reported session and the work journal survive a restart, and the
// journal keeps the last MaxJournal entries.
func TestSessionAndJournalPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	x := tr("w5:p1", "", herdr.StateWorking, t0)
	x.Session = &herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: herdr.SessionKindID, Value: "366f482c"}
	x.Cwd = "/work/calc"
	if a, changed := mustApply(t, s, x); !changed || a.SessionSource != "herdr:claude" || a.SessionKind != "id" || a.SessionValue != "366f482c" || a.Cwd != "/work/calc" {
		t.Fatalf("after apply: %+v", a)
	}
	// The same session again is no change; a new one is.
	y := tr("w5:p1", herdr.StateWorking, herdr.StateWorking, t0)
	y.Session = x.Session
	if _, changed := mustApply(t, s, y); changed {
		t.Error("the same session counted as a change")
	}
	y.Session = &herdr.AgentSession{Source: "herdr:claude", Kind: herdr.SessionKindID, Value: "new"}
	if a, changed := mustApply(t, s, y); !changed || a.SessionValue != "new" {
		t.Errorf("a new session: %+v", a)
	}

	for i := 0; i < MaxJournal+5; i++ {
		e := JournalEntry{At: t0.Add(time.Duration(i) * time.Minute), Event: "done", LastAssistant: fmt.Sprintf("entry %d", i), FilesTouched: []string{"a.go"}, Tools: map[string]int{"Edit": i}}
		if err := s.AppendJournal("w5:p1", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendJournal("w9:p9", JournalEntry{}); err == nil {
		t.Error("a journal entry for an unknown pane was accepted")
	}
	// A caller's copy does not alias the store's.
	got, _ := s.Get("w5:p1")
	got.Journal[0].Tools["Edit"] = 99
	got.Journal[0].FilesTouched[0] = "changed"

	re, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := re.Get("w5:p1")
	if len(a.Journal) != MaxJournal || a.Journal[0].LastAssistant != "entry 5" || a.Journal[MaxJournal-1].LastAssistant != fmt.Sprintf("entry %d", MaxJournal+4) {
		t.Fatalf("journal after reload = %d entries, first %+v", len(a.Journal), a.Journal[0])
	}
	if a.Journal[0].Tools["Edit"] != 5 || !reflect.DeepEqual(a.Journal[0].FilesTouched, []string{"a.go"}) || !a.Journal[0].At.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("entry round trip = %+v", a.Journal[0])
	}
	if a.SessionValue != "new" || a.Cwd != "/work/calc" {
		t.Errorf("session after reload: %+v", a)
	}
}

func TestPIDSessionNotOverwrittenByStaleHerdrSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initial herdr snapshot reports old-id from SessionStart.
	initTr := tr("w5:p1", "", herdr.StateWorking, t0)
	initTr.Agent = "claude"
	initTr.Session = &herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: herdr.SessionKindID, Value: "old-id"}
	mustApply(t, s, initTr)

	// 2. Same-PID /clear rotates the live session to rotated-id via PID discovery.
	if _, err := s.Update("w5:p1", func(a *Agent) {
		a.SessionSource = "pid:claude"
		a.SessionKind = herdr.SessionKindID
		a.SessionValue = "rotated-id"
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Subsequent transitions and 30s snapshots still carry Watcher's cached old-id:
	// they must not clobber rotated-id or trigger redundant disk writes.
	snap := tr("w5:p1", herdr.StateWorking, herdr.StateWorking, t0.Add(30*time.Second))
	snap.Agent = "claude"
	snap.Session = &herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: herdr.SessionKindID, Value: "old-id"}
	if a, changed := mustApply(t, s, snap); changed || a.SessionSource != "pid:claude" || a.SessionValue != "rotated-id" {
		t.Fatalf("after stale snapshot: changed=%v row=%+v, want unchanged pid:claude rotated-id", changed, a)
	}

	// 4. Across a daemon restart, the startup snapshot carrying old-id still preserves rotated-id.
	re, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := mustApply(t, re, snap); a.SessionSource != "pid:claude" || a.SessionValue != "rotated-id" {
		t.Fatalf("after restart + stale snapshot: row=%+v, want pid:claude rotated-id", a)
	}

	// 5. A genuinely new herdr session (new CLI process started in the pane) DOES update the row.
	restarted := tr("w5:p1", herdr.StateWorking, herdr.StateWorking, t0.Add(time.Minute))
	restarted.Agent = "claude"
	restarted.Session = &herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: herdr.SessionKindID, Value: "fresh-process-id"}
	if a, changed := mustApply(t, re, restarted); !changed || a.SessionSource != "herdr:claude" || a.SessionValue != "fresh-process-id" {
		t.Fatalf("after fresh herdr session: changed=%v row=%+v, want herdr:claude fresh-process-id", changed, a)
	}

	// 6. Second-zero start + /new before the very first 30s snapshot: PID discovery
	// already recorded codex-rotated when the first snapshot arrives with codex-initial.
	z := tr("w6:p1", "", herdr.StateWorking, t0)
	z.Agent = "codex"
	mustApply(t, re, z)
	if _, err := re.Update("w6:p1", func(a *Agent) {
		a.SessionSource = "pid:codex"
		a.SessionKind = herdr.SessionKindID
		a.SessionValue = "codex-rotated"
	}); err != nil {
		t.Fatal(err)
	}
	firstSnap := tr("w6:p1", herdr.StateWorking, herdr.StateWorking, t0.Add(30*time.Second))
	firstSnap.Agent = "codex"
	firstSnap.Session = &herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: herdr.SessionKindID, Value: "codex-initial"}
	if a, _ := mustApply(t, re, firstSnap); a.SessionSource != "pid:codex" || a.SessionValue != "codex-rotated" {
		t.Fatalf("after delayed first snapshot: row=%+v, want pid:codex codex-rotated", a)
	}
	// Repeating firstSnap is now a no-op, while a brand-new codex process session updates.
	if _, changed := mustApply(t, re, firstSnap); changed {
		t.Fatal("repeated firstSnap counted as a change")
	}
	secondProc := firstSnap
	secondProc.Session = &herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: herdr.SessionKindID, Value: "codex-second-proc"}
	if a, changed := mustApply(t, re, secondProc); !changed || a.SessionValue != "codex-second-proc" {
		t.Fatalf("after second codex process: changed=%v row=%+v", changed, a)
	}
}
