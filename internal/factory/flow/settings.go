package flow

import (
	"fmt"
	"time"
)

// PassInterval is the loop's tick. It is a constant, not a setting
// (design.md 10.1: the engine's own machinery is not tunable).
const PassInterval = 15 * time.Second

// Settings holds the numbers the rows use. A version ships only the
// settings its own rows use (design.md 10.1).
type Settings struct {
	ReviewRounds               int `json:"review_rounds"`
	MaxRounds                  int `json:"max_rounds"`                     // L12 (design.md 10.1)
	RetroAfterRoundsWithMajors int `json:"retro_after_rounds_with_majors"` // L12 (design.md 10.1)
	RetroFromRound             int `json:"retro_from_round"`               // L12 (design.md 10.1)
	ChecksGraceMinutes         int `json:"checks_grace_minutes"`           // N36 (design.md 10.1)
	StuckMinutes               int `json:"stuck_minutes"`                  // S2, until slice 5a (design.md 10.1)
}

// Range limits for each setting.
const (
	ReviewRoundsMin = 1
	ReviewRoundsMax = 5
	MaxRoundsMax    = 10
	RetroMajorsMin  = 2
	RetroMajorsMax  = 4
	RetroFromMin    = 2
	RetroFromMax    = 6
	ChecksGraceMin  = 1
	ChecksGraceMax  = 60
	StuckMin        = 5
	StuckMax        = 240
)

// DefaultSettings are slice 2's defaults: two passing rounds on the final head
// (design.md 10.1, D12).
func DefaultSettings() Settings {
	return Settings{
		ReviewRounds: 2, MaxRounds: 5,
		RetroAfterRoundsWithMajors: 2, RetroFromRound: 3,
		ChecksGraceMinutes: 5, StuckMinutes: 30,
	}
}

// Changed lists every setting that differs from its default as "name
// value", sorted, for the coordinator's facts block (design.md 10). A new
// setting adds its line here.
func (s Settings) Changed() []string {
	d := DefaultSettings()
	var out []string
	for _, c := range []struct {
		name     string
		got, def int
	}{
		{"checks_grace_minutes", s.ChecksGraceMinutes, d.ChecksGraceMinutes},
		{"max_rounds", s.MaxRounds, d.MaxRounds},
		{"retro_after_rounds_with_majors", s.RetroAfterRoundsWithMajors, d.RetroAfterRoundsWithMajors},
		{"retro_from_round", s.RetroFromRound, d.RetroFromRound},
		{"review_rounds", s.ReviewRounds, d.ReviewRounds},
		{"stuck_minutes", s.StuckMinutes, d.StuckMinutes},
	} {
		if c.got != c.def {
			out = append(out, fmt.Sprintf("%s %d", c.name, c.got))
		}
	}
	return out
}

// Validate refuses a value outside its range, naming the range.
func (s Settings) Validate() error {
	for _, r := range []struct {
		name         string
		v, low, high int
	}{
		{"review_rounds", s.ReviewRounds, ReviewRoundsMin, ReviewRoundsMax},
		{"max_rounds", s.MaxRounds, s.ReviewRounds, MaxRoundsMax},
		{"retro_after_rounds_with_majors", s.RetroAfterRoundsWithMajors, RetroMajorsMin, RetroMajorsMax},
		{"retro_from_round", s.RetroFromRound, RetroFromMin, RetroFromMax},
		{"checks_grace_minutes", s.ChecksGraceMinutes, ChecksGraceMin, ChecksGraceMax},
		{"stuck_minutes", s.StuckMinutes, StuckMin, StuckMax},
	} {
		if r.v < r.low || r.v > r.high {
			return fmt.Errorf("%s %d: allowed %d–%d", r.name, r.v, r.low, r.high)
		}
	}
	return nil
}
