package factory

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestDefaultPathsGuard: a test that reaches for the real data dir fails
// (design.md 16 Hygiene; KB-TST-1, KB-TST-3).
func TestDefaultPathsGuard(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("DefaultPaths returned under go test: tests could touch the real home")
		}
	}()
	_, _ = DefaultPaths()
}

func TestStateRoundTrip(t *testing.T) {
	p := Paths{DataDir: t.TempDir()}
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	want := flow.State{
		Seq:      7,
		Project:  flow.Project{ID: "p1"},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{
			ID: "t1", Title: "Add x", Brief: "b", Outcomes: []string{"x works"},
			Done: &flow.Merged{MergedCommit: "abc"},
			Attempts: []flow.Attempt{{
				BaseCommit: "000", Branch: "t1", Worktree: "/w", PR: 3, RunID: "r1",
				Ready:   []flow.Ready{{Head: "111"}},
				Rounds:  []flow.Round{{N: 1, Kind: "full", Head: "111", Reviewer: flow.Reviewer{CLI: "claude", Model: "m", Effort: "high"}, Verdict: "pass", ReportPath: "r.md"}},
				Accepts: []flow.Accept{{Head: "111", By: "bar", Rounds: []int{1}}},
				Acts:    []flow.Act{{Key: "merge@111", Result: "done", At: at}},
			}},
		}},
	}
	path := p.State("p1")
	if err := SaveState(path, want); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func TestTrimLog(t *testing.T) {
	path := Paths{DataDir: t.TempDir()}.Log("p1")
	if err := TrimLog(path, 0); err != nil {
		t.Fatalf("missing log: %v", err)
	}
	for seq := int64(1); seq <= 4; seq++ {
		if err := AppendLog(path, LogLine{Seq: seq, Project: "p1", Rule: "N43"}); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"seq":5,"ru`) // torn by a crash
	_ = f.Close()
	if err := TrimLog(path, 2); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := `{"seq":1,"at":"0001-01-01T00:00:00Z","project":"p1","by":"","rule":"N43"}` + "\n" +
		`{"seq":2,"at":"0001-01-01T00:00:00Z","project":"p1","by":"","rule":"N43"}` + "\n"
	if string(data) != want {
		t.Fatalf("trimmed log:\n%s", data)
	}
	if err := os.WriteFile(path, []byte("junk\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if TrimLog(path, 2) == nil {
		t.Fatal("a corrupt line that is not the last was accepted")
	}
}
