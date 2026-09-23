package eventlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Ids are written on every line, strictly increasing across day files
// and restarts — even when the clock goes back.
func TestIDsIncreaseAcrossDaysAndRestarts(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 9, 17, 23, 59, 59, 0, time.UTC)}
	l, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	l.Log("t", "a", "", nil)
	l.Log("t", "b", "", nil) // same instant: last+1
	c.Set(time.Date(2026, 9, 18, 0, 0, 1, 0, time.UTC))
	l.Log("t", "c", "", nil)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// Restart with the clock an hour behind.
	c.Set(time.Date(2026, 9, 17, 23, 0, 0, 0, time.UTC))
	l2, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	l2.Log("t", "d", "", nil)
	c.Set(time.Date(2026, 9, 18, 0, 0, 2, 0, time.UTC))
	l2.Log("t", "e", "", nil)
	if err := l2.Close(); err != nil {
		t.Fatal(err)
	}
	var ids []float64
	var types []string
	for _, day := range []string{"2026-09-17", "2026-09-18"} {
		for _, line := range readLines(t, filepath.Join(dir, "events", day+".jsonl")) {
			ids = append(ids, line["id"].(float64))
			types = append(types, line["type"].(string))
		}
	}
	// d went to the 17th's file (its ts), with an id after c's.
	if strings.Join(types, "") != "abdce" {
		t.Fatalf("types by file = %v", types)
	}
	byType := map[string]float64{}
	for i, typ := range types {
		byType[typ] = ids[i]
	}
	order := []string{"a", "b", "c", "d", "e"}
	for i := 1; i < len(order); i++ {
		if byType[order[i]] <= byType[order[i-1]] {
			t.Fatalf("id(%s)=%v not after id(%s)=%v", order[i], byType[order[i]], order[i-1], byType[order[i-1]])
		}
	}
	if byType["a"] != float64(time.Date(2026, 9, 17, 23, 59, 59, 0, time.UTC).UnixMicro()) || byType["b"] != byType["a"]+1 {
		t.Errorf("a=%v b=%v", byType["a"], byType["b"])
	}
}

// A line written before ids existed gets one derived from its ts.
func TestReplayDerivesMissingIDs(t *testing.T) {
	in := `{"ts":"2026-09-17T10:00:00Z","source":"t","type":"a"}
{"ts":"2026-09-17T10:00:00Z","source":"t","type":"b"}
{"id":5,"ts":"2026-09-17T09:00:00Z","source":"t","type":"c"}
`
	evs, err := Replay(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC).UnixMicro()
	if evs[0].ID != base || evs[1].ID != base+1 || evs[2].ID != 5 {
		t.Fatalf("ids = %d %d %d", evs[0].ID, evs[1].ID, evs[2].ID)
	}
	if !strings.Contains(string(evs[0].Encoded()), `"id":`) {
		t.Errorf("encoded = %s", evs[0].Encoded())
	}
}

// Since replays what came after an id: from the ring when it reaches
// back that far, else from the day files (a previous run's events), and
// truncated to the newest maxN.
func TestSinceReadsTheRingAndTheFiles(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	l, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		c.Set(c.Now().Add(time.Second))
		l.Log("t", "old", "", map[string]any{"i": i})
	}
	old := l.Tail(0)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	c.Set(time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC))
	l, err = Open(dir, Options{Now: c.Now, RingSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	for i := 0; i < 4; i++ {
		c.Set(c.Now().Add(time.Second))
		l.Log("t", "new", "", map[string]any{"i": i})
	}
	// From the files: after the second old event, across the restart and
	// the day boundary (the ring only holds the last three new ones).
	evs, truncated, err := l.Since(old[1].ID, 0)
	if err != nil || truncated {
		t.Fatalf("since: %v truncated=%v", err, truncated)
	}
	if len(evs) != 3+4 || evs[0].ID != old[2].ID || evs[len(evs)-1].Type != "new" {
		t.Fatalf("since old[1] = %d events: %+v", len(evs), evs)
	}
	// From the ring.
	ring := l.Tail(0)
	evs, _, _ = l.Since(ring[0].ID, 0)
	if len(evs) != 2 || evs[0].ID != ring[1].ID {
		t.Fatalf("since ring[0] = %+v", evs)
	}
	// Truncated to the newest.
	evs, truncated, _ = l.Since(0, 2)
	if !truncated || len(evs) != 2 || evs[1].ID != ring[2].ID {
		t.Fatalf("since 0 max 2 = %+v truncated=%v", evs, truncated)
	}
	days, _ := l.Days()
	if strings.Join(days, ",") != "2026-09-17,2026-09-18" {
		t.Errorf("days = %v", days)
	}
}

// A torn line (a crash mid-write) does not hide the lines after it, and
// the next id still follows the last good one.
func TestReadDaySkipsATornLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	in := `{"id":100,"ts":"2026-09-17T10:00:00Z","source":"t","type":"a"}
{"id":101,"ts":"2026-09-17T10:00:0
{"id":102,"ts":"2026-09-17T10:00:01Z","source":"t","type":"c"}
`
	if err := os.WriteFile(filepath.Join(dir, "events", "2026-09-17.jsonl"), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)} // before those ids
	l, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	evs, err := l.ReadDay("2026-09-17")
	if err != nil || len(evs) != 2 || evs[1].Type != "c" {
		t.Fatalf("read = %+v %v", evs, err)
	}
	l.Log("t", "d", "", nil)
	if tail := l.Tail(1); tail[0].ID <= 102 {
		t.Errorf("next id = %d, want > 102", tail[0].ID)
	}
}
