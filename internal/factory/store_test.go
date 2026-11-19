package factory

import (
	"os"
	"path/filepath"
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

// TestLoadStateFillsNewSettings: a state.json written before stuck_minutes
// existed loads it at its default and keeps the settings it has.
func TestLoadStateFillsNewSettings(t *testing.T) {
	path := Paths{DataDir: t.TempDir()}.State("p1")
	if err := writeAtomic(path, []byte(`{"seq":3,"settings":{"review_rounds":1,"checks_grace_minutes":9}}`)); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (flow.Settings{ReviewRounds: 1, ChecksGraceMinutes: 9, StuckMinutes: flow.DefaultSettings().StuckMinutes}); got.Settings != want {
		t.Fatalf("settings = %+v, want %+v", got.Settings, want)
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
	// A whole last line whose newline never made it is torn too; otherwise
	// the next append would glue two records onto one line.
	if err := os.WriteFile(path, []byte(`{"seq":1}`+"\n"+`{"seq":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TrimLog(path, 9); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"seq":1}`+"\n" {
		t.Fatalf("unterminated last line kept: %q", data)
	}
}

// TestStoreRefusesRealHome: a store call pointed outside the temp dir fails
// under go test, whoever built the path (KB-TST-1).
func TestStoreRefusesRealHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	p := Paths{DataDir: filepath.Join(home, ".local", "share", "sheepdog")}
	if SaveState(p.State("guard-test"), flow.State{}) == nil {
		t.Fatal("SaveState wrote under the real home")
	}
	if _, err := LoadState(p.State("guard-test")); err == nil || os.IsNotExist(err) {
		t.Fatalf("LoadState read under the real home: %v", err)
	}
	if AppendLog(p.Log("guard-test"), LogLine{}) == nil || TrimLog(p.Log("guard-test"), 0) == nil {
		t.Fatal("the log was touched under the real home")
	}
	if SaveState(Paths{}.State("guard-test"), flow.State{}) == nil {
		t.Fatal("a zero Paths wrote into the checkout")
	}
}
