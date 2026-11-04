package eventlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPruneKeepsRetentionDaysAndNothingElse(t *testing.T) {
	data := t.TempDir()
	ev := filepath.Join(data, "events")
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, n := range []string{"2026-10-14.jsonl", "2026-09-30.jsonl", "2026-09-29.jsonl", "2025-01-01.jsonl", "notes.jsonl", "2026-01-01.jsonl.bak", "sub/2020-01-01.jsonl"} {
		touch(t, filepath.Join(ev, n))
	}
	touch(t, filepath.Join(data, "2020-01-01.jsonl")) // outside events/
	removed, err := Prune(data, 14, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %q, want 2025-01-01 and 2026-09-29", removed)
	}
	for _, keep := range []string{"events/2026-10-14.jsonl", "events/2026-09-30.jsonl", "events/notes.jsonl", "events/2026-01-01.jsonl.bak", "events/sub/2020-01-01.jsonl", "2020-01-01.jsonl"} {
		if _, err := os.Stat(filepath.Join(data, keep)); err != nil {
			t.Errorf("%s was deleted", keep)
		}
	}
	if r, _ := Prune(data, 0, now.AddDate(5, 0, 0)); len(r) != 0 {
		t.Errorf("retention 0 deleted %q", r)
	}
}

func TestPruneNeverTouchesRepoBenchData(t *testing.T) {
	for _, sub := range [][]string{
		{"docs", "dev", "bench", "results", "run1"},
		{"docs", "dev", "eval", "runs", "run1"},
		{"docs", "bench", "results", "run1"},
		{"docs", "eval", "runs", "run1"},
		{"testdata", "run1"},
	} {
		data := filepath.Join(append([]string{t.TempDir()}, sub...)...)
		old := filepath.Join(data, "events", "2020-01-01.jsonl")
		touch(t, old)
		if r, _ := Prune(data, 1, time.Now()); len(r) != 0 {
			t.Errorf("pruned %q under %v", r, sub)
		}
		if r, _ := Clear(data); len(r) != 0 {
			t.Errorf("cleared %q under %v", r, sub)
		}
		if _, err := os.Stat(old); err != nil {
			t.Errorf("%v file deleted", sub)
		}
	}
}

func TestPruneSkipsSymlinks(t *testing.T) {
	data := t.TempDir()
	target := filepath.Join(t.TempDir(), "precious.jsonl")
	touch(t, target)
	if err := os.MkdirAll(filepath.Join(data, "events"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(data, "events", "2020-01-01.jsonl")); err != nil {
		t.Fatal(err)
	}
	if r, _ := Prune(data, 1, time.Now()); len(r) != 0 {
		t.Errorf("pruned a symlink: %q", r)
	}
	if _, err := os.Stat(target); err != nil {
		t.Error("symlink target deleted")
	}
}

func TestPruneBrainTranscripts(t *testing.T) {
	data := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -30)
	files := map[string]time.Time{
		"20260901T100000Z.jsonl":          old,
		"20260901T100000Z.requests.jsonl": old,
		"20260901T100000Z-reset2.jsonl":   old,
		"20260902T100000Z.jsonl":          now, // an old run still written to: kept
		"20261013T100000Z.jsonl":          now,
		"notes.jsonl":                     old,
	}
	for n, mt := range files {
		p := filepath.Join(data, "brain", n)
		touch(t, p)
		_ = os.Chtimes(p, mt, mt)
	}
	removed, err := Prune(data, 14, now)
	if err != nil || len(removed) != 3 {
		t.Errorf("removed %q (%v), want the three 20260901 files", removed, err)
	}
	for _, keep := range []string{"20260902T100000Z.jsonl", "20261013T100000Z.jsonl", "notes.jsonl"} {
		if _, err := os.Stat(filepath.Join(data, "brain", keep)); err != nil {
			t.Errorf("%s deleted", keep)
		}
	}
	if r, _ := Clear(data); len(r) != 2 {
		t.Errorf("Clear removed %q", r)
	}
}

func TestPruneAndClearCards(t *testing.T) {
	data := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cardsPath := filepath.Join(data, "cards.jsonl")
	content := `{"card_id":"c1","kind":"card","at":"2026-09-29T23:59:59Z","title":"old"}` + "\n" +
		`{"card_id":"c2","kind":"card","at":"2026-09-30T00:00:00Z","title":"cutoff"}` + "\n" +
		`{"card_id":"c3","kind":"card","at":"2026-10-14T10:00:00Z","title":"today"}` + "\n"
	if err := os.WriteFile(cardsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := Prune(data, 14, now)
	if err != nil || len(removed) != 1 || removed[0] != cardsPath {
		t.Fatalf("Prune removed %q (%v), want [%q]", removed, err, cardsPath)
	}
	got, err := os.ReadFile(cardsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"card_id":"c2","kind":"card","at":"2026-09-30T00:00:00Z","title":"cutoff"}` + "\n" +
		`{"card_id":"c3","kind":"card","at":"2026-10-14T10:00:00Z","title":"today"}` + "\n"
	if string(got) != want {
		t.Errorf("cards.jsonl after Prune =\n%s\nwant\n%s", got, want)
	}
	// Second prune with no expired cards does nothing.
	if r, err := Prune(data, 14, now); err != nil || len(r) != 0 {
		t.Errorf("second Prune removed %q (%v), want nothing", r, err)
	}
	// Clear removes cards.jsonl.
	if r, err := Clear(data); err != nil || len(r) != 1 || r[0] != cardsPath {
		t.Errorf("Clear removed %q (%v), want [%q]", r, err, cardsPath)
	}
	if _, err := os.Stat(cardsPath); !os.IsNotExist(err) {
		t.Errorf("cards.jsonl still exists after Clear: %v", err)
	}
}
