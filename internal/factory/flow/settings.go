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
	ReviewRounds       int `json:"review_rounds"`
	ChecksGraceMinutes int `json:"checks_grace_minutes"` // N36 (design.md 10.1)
	StuckMinutes       int `json:"stuck_minutes"`        // S2, until slice 5a (design.md 10.1)
}

// Range limits for each setting.
const (
	ReviewRoundsMin = 1
	ReviewRoundsMax = 5
	ChecksGraceMin  = 1
	ChecksGraceMax  = 60
	StuckMin        = 5
	StuckMax        = 240
)

// DefaultSettings are slice 2's defaults: two passing rounds on the final head
// (design.md 10.1, D12).
func DefaultSettings() Settings {
	return Settings{ReviewRounds: 2, ChecksGraceMinutes: 5, StuckMinutes: 30}
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
	if s.ReviewRounds < ReviewRoundsMin || s.ReviewRounds > ReviewRoundsMax {
		return fmt.Errorf("review_rounds %d: allowed %d–%d", s.ReviewRounds, ReviewRoundsMin, ReviewRoundsMax)
	}
	if s.ChecksGraceMinutes < ChecksGraceMin || s.ChecksGraceMinutes > ChecksGraceMax {
		return fmt.Errorf("checks_grace_minutes %d: allowed %d–%d", s.ChecksGraceMinutes, ChecksGraceMin, ChecksGraceMax)
	}
	if s.StuckMinutes < StuckMin || s.StuckMinutes > StuckMax {
		return fmt.Errorf("stuck_minutes %d: allowed %d–%d", s.StuckMinutes, StuckMin, StuckMax)
	}
	return nil
}
