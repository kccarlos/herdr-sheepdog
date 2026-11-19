package flow

import (
	"testing"
	"time"
)

func row(id string, when func(int) bool) Row[int] {
	return Row[int]{ID: id, WaitsOn: "engine", When: when, Then: func(int) Decision { return Decision{Why: id} }}
}

var catchAll = row("none", nil)

func TestFirstMatch(t *testing.T) {
	rows := []Row[int]{
		row("big", func(f int) bool { return f > 10 }),
		row("pos", func(f int) bool { return f > 0 }),
		catchAll,
	}
	if err := CheckTable(rows); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		f    int
		want string
	}{{20, "big"}, {5, "pos"}, {-1, "none"}} {
		r, d := FirstMatch(rows, c.f)
		if r.ID != c.want || d.Why != c.want {
			t.Errorf("FirstMatch(%d) = %q/%q, want %q", c.f, r.ID, d.Why, c.want)
		}
	}
}

func TestCheckTable(t *testing.T) {
	pos := row("pos", func(f int) bool { return f > 0 })
	bad := map[string][]Row[int]{
		"empty":           nil,
		"no catch-all":    {pos},
		"catch-all early": {catchAll, pos, catchAll},
		"no id":           {row("", func(int) bool { return true }), catchAll},
		"no waits on":     {{ID: "x", When: pos.When, Then: pos.Then}, catchAll},
	}
	for name, rows := range bad {
		if CheckTable(rows) == nil {
			t.Errorf("%s: CheckTable passed", name)
		}
	}
}

func TestActs(t *testing.T) {
	at := time.Unix(0, 0)
	acts := []Act{{"merge@3f9c", "failed", at}, {"merge@3f9c", "done", at}, {"push", "failed", at}}
	if !Done(acts, "merge@3f9c") || Done(acts, "push") || Done(acts, "x") {
		t.Error("Done")
	}
	if Count(acts, "merge@3f9c") != 2 || Count(acts, "push") != 1 || Count(acts, "x") != 0 {
		t.Error("Count")
	}
}

func TestSettingsRange(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatal(err)
	}
	set := map[string]func(*Settings, int){
		"review_rounds":        func(s *Settings, n int) { s.ReviewRounds = n },
		"checks_grace_minutes": func(s *Settings, n int) { s.ChecksGraceMinutes = n },
	}
	limits := map[string][2]int{
		"review_rounds":        {ReviewRoundsMin, ReviewRoundsMax},
		"checks_grace_minutes": {ChecksGraceMin, ChecksGraceMax},
	}
	for name, lim := range limits {
		for _, c := range []struct {
			n  int
			ok bool
		}{{lim[0], true}, {lim[1], true}, {lim[0] - 1, false}, {lim[1] + 1, false}} {
			s := DefaultSettings()
			set[name](&s, c.n)
			if err := s.Validate(); (err == nil) != c.ok {
				t.Errorf("%s %d: err %v, want ok=%v", name, c.n, err, c.ok)
			}
		}
	}
}
