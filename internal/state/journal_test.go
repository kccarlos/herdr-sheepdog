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
