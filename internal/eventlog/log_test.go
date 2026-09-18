package eventlog

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("bad line %s: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestLineShapeAndDurMS(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)}
	l, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	l.Log("llm", "llm.call", "", map[string]any{"model": "m", "dur_ms": 42})
	l.Log("herdr", "herdr.state", "w1:p1", map[string]any{"state": "blocked"})
	l.Log("web", "web.client.connect", "", nil)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "events", "2026-09-17.jsonl"))
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	first := lines[0]
	for _, k := range []string{"ts", "source", "type", "payload", "dur_ms"} {
		if _, ok := first[k]; !ok {
			t.Errorf("line 1 missing %q: %v", k, first)
		}
	}
	if first["dur_ms"] != float64(42) || first["source"] != "llm" || first["type"] != "llm.call" {
		t.Errorf("line 1 = %v", first)
	}
	if _, ok := first["pane_id"]; ok {
		t.Errorf("empty pane_id must be omitted: %v", first)
	}
	if _, ok := first["seq"]; ok {
		t.Errorf("seq must not be written: %v", first)
	}
	if lines[1]["pane_id"] != "w1:p1" {
		t.Errorf("line 2 pane_id = %v", lines[1]["pane_id"])
	}
	if _, ok := lines[1]["dur_ms"]; ok {
		t.Errorf("dur_ms must be omitted when absent: %v", lines[1])
	}
	if _, ok := lines[2]["payload"]; ok {
		t.Errorf("nil payload must be omitted: %v", lines[2])
	}
	if !strings.HasPrefix(first["ts"].(string), "2026-09-17T10:00:00") {
		t.Errorf("ts = %v", first["ts"])
	}
}

func TestRotatesAtMidnight(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 9, 17, 23, 59, 59, 0, time.UTC)}
	l, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	l.Log("t", "a", "", nil)
	c.Set(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	l.Log("t", "b", "", nil)
	l.Log("t", "c", "", nil)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	d1 := readLines(t, filepath.Join(dir, "events", "2026-09-17.jsonl"))
	d2 := readLines(t, filepath.Join(dir, "events", "2026-09-18.jsonl"))
	if len(d1) != 1 || d1[0]["type"] != "a" {
		t.Errorf("day 1 = %v", d1)
	}
	if len(d2) != 2 || d2[0]["type"] != "b" || d2[1]["type"] != "c" {
		t.Errorf("day 2 = %v", d2)
	}
}

func TestConcurrentWritersAndTail(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{RingSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	const writers, per = 8, 100
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				l.Log("t", "x", "", map[string]any{"w": w, "i": i})
			}
		}(w)
	}
	wg.Wait()
	if got := l.Seq(); got != writers*per {
		t.Fatalf("seq = %d, want %d", got, writers*per)
	}
	tail := l.Tail(10)
	if len(tail) != 10 {
		t.Fatalf("tail = %d", len(tail))
	}
	for i := 1; i < len(tail); i++ {
		if tail[i].Seq != tail[i-1].Seq+1 {
			t.Fatalf("tail not contiguous: %d then %d", tail[i-1].Seq, tail[i].Seq)
		}
	}
	if tail[9].Seq != writers*per {
		t.Fatalf("tail ends at %d", tail[9].Seq)
	}
	if all := l.Tail(0); len(all) != 50 {
		t.Fatalf("Tail(0) = %d, want ring size 50", len(all))
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, l.Path(time.Now()))
	if len(lines) != writers*per {
		t.Fatalf("file has %d lines, want %d", len(lines), writers*per)
	}
}

func TestSubscribeAndDrop(t *testing.T) {
	l, err := Open("", Options{SubscriberBuffer: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fast := l.Subscribe(ctx)
	slow := l.Subscribe(ctx)

	l.Log("t", "one", "", nil)
	if ev := <-fast; ev.Type != "one" || ev.Seq != 1 {
		t.Fatalf("fast got %+v", ev)
	}
	// Fill the slow subscriber past its buffer while draining the fast one.
	var drained []Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range fast {
			drained = append(drained, ev)
			if ev.Type == "eventlog.subscriber_dropped" {
				return
			}
		}
	}()
	for i := 0; i < 6; i++ {
		l.Log("t", "burst", "", nil)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no subscriber_dropped event")
	}
	// The slow channel delivers what it buffered and is then closed.
	n := 0
	for range slow {
		n++
	}
	if n != 4 {
		t.Fatalf("slow received %d buffered events, want 4", n)
	}
	// A new subscriber keeps working after another was dropped.
	late := l.Subscribe(ctx)
	l.Log("t", "after", "", nil)
	select {
	case ev := <-late:
		if ev.Type != "after" {
			t.Fatalf("late got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("late subscriber got nothing")
	}
	cancel()
	if _, ok := <-late; ok {
		// drain until closed
		for range late {
		}
	}
}

func TestSubscribeClosedOnLogClose(t *testing.T) {
	l, err := Open("", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ch := l.Subscribe(context.Background())
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}
	l.Log("t", "ignored", "", nil) // must not panic
	if l.Seq() != 0 {
		t.Fatal("logged after close")
	}
}

type unmarshalable struct{ C chan int }

func TestBadPayloadDoesNotKillWriter(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Log("t", "bad", "", unmarshalable{C: make(chan int)})
	l.Log("t", "good", "", map[string]any{"ok": true})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, l.Path(time.Now()))
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	p, _ := lines[0]["payload"].(map[string]any)
	if _, ok := p["encode_error"]; !ok {
		t.Errorf("bad payload line = %v", lines[0])
	}
	if lines[1]["type"] != "good" {
		t.Errorf("second line = %v", lines[1])
	}
}

func TestWriteErrorReportedOnce(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	c := &clock{now: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)}
	l, err := Open(dir, Options{Now: c.Now, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	// Make the events dir unwritable so the day file cannot be created.
	if err := os.Chmod(filepath.Join(dir, "events"), 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Join(dir, "events"), 0o700) }()
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	l.Log("t", "a", "", nil)
	l.Log("t", "b", "", nil)
	if l.Seq() != 2 || len(l.Tail(0)) != 2 {
		t.Fatal("ring must keep working without a file")
	}
	if err := l.Close(); err == nil {
		t.Fatal("Close should report the write error")
	}
	if n := strings.Count(stderr.String(), "eventlog: write error"); n != 1 {
		t.Fatalf("stderr reports = %d: %q", n, stderr.String())
	}
}

func TestReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Log("herdr", "herdr.read", "w1:p1", map[string]any{"bytes": 10, "dur_ms": 3})
	l.Log("supervisor", "pending.created", "w1:p1", map[string]any{"id": "ap-1"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(l.Path(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	evs, err := Replay(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != "herdr.read" || evs[1].Seq != 2 || evs[0].DurMS == nil || *evs[0].DurMS != 3 {
		t.Fatalf("replay = %+v", evs)
	}
	if _, err := Replay(strings.NewReader("{}\nnot json\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want line 2 error, got %v", err)
	}
	if evs, err := Replay(strings.NewReader("\n\n")); err != nil || len(evs) != 0 {
		t.Fatalf("blank lines: %v %v", evs, err)
	}
}

func TestEncodedMatchesFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Log("t", "x", "p", map[string]any{"k": "v"})
	enc := string(l.Tail(1)[0].Encoded())
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(l.Path(time.Now()))
	if strings.TrimSpace(string(data)) != enc {
		t.Fatalf("file %q != encoded %q", data, enc)
	}
}
