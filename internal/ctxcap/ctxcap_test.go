package ctxcap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func snap(pieces ...[3]string) Snapshot {
	var bd Builder
	for _, p := range pieces {
		bd.Add(Section{Key: p[0], Label: p[0], Class: p[1]}, p[2])
	}
	return bd.Snapshot(nil)
}

// The builder covers its text exactly; CheckCoverage finds a gap.
func TestBuilderCoverage(t *testing.T) {
	s := snap([3]string{"a", ClassSystem, "hello\n"}, [3]string{"b", ClassShepherd, "héllo — 🎤\n"})
	if err := s.CheckCoverage(); err != nil {
		t.Fatal(err)
	}
	if got := s.WithTexts()[1].Text; got != "héllo — 🎤\n" {
		t.Errorf("text = %q", got)
	}
	s.Sections[1].Start++
	var ce *CoverageError
	if err := s.CheckCoverage(); !errors.As(err, &ce) || ce.Index != 1 {
		t.Errorf("gap not found: %v", err)
	}
}

// The diff's states: added, changed, pruned, compacted, and removed for
// each reason.
func TestDiffSnapshots(t *testing.T) {
	prev := snap([3]string{"prompt:time", ClassSystem, "10:00\n"}, [3]string{"seq:1", ClassShepherd, "hi\n"}, [3]string{"seq:2", ClassToolResult, "long result\n"})
	cur := snap([3]string{"prompt:time", ClassSystem, "10:01\n"}, [3]string{"seq:1", ClassShepherd, "hi\n\n"}, [3]string{"seq:2", ClassPruned, "[stub, pruned]\n"}, [3]string{"seq:3", ClassRequest, "req\n"})
	cur.Sections[2].Pruned = true
	d := DiffSnapshots(4, prev, cur, false)
	if strings.Join(d.Added, ",") != "seq:3" || strings.Join(d.Changed, ",") != "prompt:time" || strings.Join(d.Pruned, ",") != "seq:2" || len(d.Removed) != 0 || d.Prev != 4 {
		t.Errorf("diff = %+v", d)
	}
	compacted := snap([3]string{"seq:9", ClassCompaction, "summary\n"}, [3]string{"seq:3", ClassRequest, "req\n"})
	d = DiffSnapshots(5, cur, compacted, false)
	if strings.Join(d.Compacted, ",") != "seq:9" || len(d.Removed) != 3 || d.Removed[0].Reason != ReasonCompacted || d.Removed[0].Text == "" {
		t.Errorf("compaction diff = %+v", d)
	}
	d = DiffSnapshots(6, cur, snap([3]string{"prompt:time", ClassSystem, "10:01\n"}, [3]string{"seq:1", ClassShepherd, "fresh\n"}), true)
	if len(d.Removed) != 3 || d.Removed[0].Reason != ReasonReset || strings.Join(d.Added, ",") != "seq:1" || len(d.Changed) != 0 {
		t.Errorf("reset diff = %+v", d)
	}
	d = DiffSnapshots(0, Snapshot{}, cur, false)
	if len(d.Added) != 4 || d.Prev != 0 {
		t.Errorf("first point = %+v", d)
	}
}

// The ring keeps the last BrainSteps steps; Get finds them by number.
func TestBrainLogRing(t *testing.T) {
	var notified int
	l, err := OpenBrainLog("", "run", func(BrainStep) { notified++ })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < BrainSteps+5; i++ {
		l.Record(BrainStep{Run: "run", Snapshot: snap([3]string{"k", ClassSystem, "x\n"})})
	}
	if l.Len() != BrainSteps || notified != BrainSteps+5 {
		t.Fatalf("len %d notified %d", l.Len(), notified)
	}
	if _, _, _, err := l.Get(5); !errors.Is(err, ErrNoStep) {
		t.Errorf("evicted step: %v", err)
	}
	cur, prev, ok, err := l.Get(0)
	if err != nil || !ok || cur.Step != BrainSteps+5 || prev.Step != BrainSteps+4 {
		t.Errorf("latest %d prev %d %v %v", cur.Step, prev.Step, ok, err)
	}
	if first, _, ok, _ := l.Get(6); first.Step != 6 || ok {
		t.Errorf("oldest = %d hasPrev %v", first.Step, ok)
	}
	if steps := l.Steps(); len(steps) != BrainSteps || steps[0].Text != "" || len(steps[0].Sections) != 0 {
		t.Errorf("steps metadata carries the snapshot")
	}
}

// A run's file is appended to; a new run loads the newest earlier file's
// tail and numbers on.
func TestBrainLogFiles(t *testing.T) {
	dir := t.TempDir()
	l, err := OpenBrainLog(dir, "20260923T100000Z", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		l.Record(BrainStep{Run: "20260923T100000Z", Snapshot: snap([3]string{"k", ClassSystem, "x\n"})})
	}
	_ = l.Close()
	data, err := os.ReadFile(filepath.Join(dir, "20260923T100000Z"+RequestsSuffix))
	if err != nil || strings.Count(string(data), "\n") != 3 {
		t.Fatalf("file: %v %q", err, data)
	}
	l2, err := OpenBrainLog(dir, "20260923T110000Z", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l2.Close() }()
	if s := l2.Record(BrainStep{Run: "20260923T110000Z"}); s.Step != 4 || l2.Len() != 4 {
		t.Errorf("step %d len %d", s.Step, l2.Len())
	}
	cur, prev, _, _ := l2.Get(0)
	if d := DiffBrain(cur, prev, true); len(d.Removed) != 1 || d.Removed[0].Reason != ReasonReset {
		t.Errorf("a restart between steps is a reset: %+v", d)
	}
}

// Audio frames coalesce into one item with a duration; the transcript
// attaches to it and opens a turn; the tool call claims it; the model's
// words close on ModelDone; a new epoch shows the old items removed by
// reset.
func TestVoiceLog(t *testing.T) {
	var turns []VoiceTurn
	l := NewVoiceLog(func(t VoiceTurn, updated bool) {
		if !updated {
			turns = append(turns, t)
		}
	})
	l.SetClock(func() time.Time { return time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC) })
	s := l.Session("gemini", "live")
	if s.ID() != "gemini#1" {
		t.Errorf("id = %s", s.ID())
	}
	s.Epoch("session open (fresh)", "fresh", nil)
	s.Instructions("be brief", nil)
	for i := 0; i < 5; i++ {
		s.Audio(3200, 16000)
	}
	s.Heard("hello ", false)
	s.Heard("there", true)
	s.Audio(3200, 16000) // a new utterance's audio: a new item
	s.Claim("call:1")    // claims the newest utterance with a transcript
	s.Item(VoiceItem{Kind: VKindToolCall, Class: ClassToolCall, Role: "model", Label: "tool call", Text: "→ x", Links: []string{"call:1"}})
	s.Said("Sure")
	s.Said(", checking.")
	s.ModelDone()
	s.Item(VoiceItem{Kind: VKindToolResponse, Class: ClassToolResult, Role: "user", Label: "tool response", Text: "← ok", Links: []string{"call:1"}})

	p, _, _, err := l.Get(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCoverage(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, sec := range p.WithTexts() {
		got = append(got, sec.Label+"="+strings.TrimSpace(sec.Text[strings.Index(sec.Text, "\n")+1:])+" "+strings.Join(sec.Links, ","))
	}
	want := []string{
		"session open (fresh)=fresh ", "system instructions=be brief ",
		`user (audio + transcript)=🎤 audio 0.5 s — heard: "hello there" call:1`,
		"user audio=🎤 audio 0.1 s ", "tool call=→ x call:1", "voice said=Sure, checking. ", "tool response=← ok call:1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var kinds []string
	for _, tr := range turns {
		kinds = append(kinds, tr.Kind)
	}
	if strings.Join(kinds, " ") != "marker instructions audio tool_response" {
		t.Errorf("turns = %v", kinds)
	}

	s2 := l.Session("gemini", "live")
	s2.Epoch("session open (fresh)", "again", nil)
	cur, prev, ok, _ := l.Get(0)
	if d := DiffVoice(cur, prev, ok); len(d.Removed) != 7 || d.Removed[0].Reason != ReasonReset || len(d.Added) != 1 {
		t.Errorf("new epoch diff = %+v", d)
	}
	// The earlier turn still renders its own epoch.
	if old, _, _, _ := l.Get(4); !strings.Contains(old.Text, "← ok") || strings.Contains(old.Text, "again") {
		t.Errorf("turn 4 = %q", old.Text)
	}
	var nilSession *VoiceSession
	nilSession.Audio(10, 16000) // a nil recorder records nothing
	var nilLog *VoiceLog
	if nilLog.Session("x", "y") != nil {
		t.Error("nil log gives a recorder")
	}
}

// LinksFromTags reads the three link tags.
func TestLinksFromTags(t *testing.T) {
	got := LinksFromTags(map[string]any{TagPushID: "p1", TagEventID: "e1", TagCallID: "c1", "other": "x"})
	if strings.Join(got, ",") != "push:p1,evt:e1,call:c1" {
		t.Errorf("links = %v", got)
	}
	if CallLink("") != "" || PushLink("a") != "push:a" || EventLink("b") != "evt:b" {
		t.Error("link builders")
	}
}
