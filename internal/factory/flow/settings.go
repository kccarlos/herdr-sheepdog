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
}

// Range limits for each setting.
const (
	ReviewRoundsMin = 1
	ReviewRoundsMax = 1 // slice 2 raises it to 5 with L8
	ChecksGraceMin  = 1
	ChecksGraceMax  = 60
)

// DefaultSettings are the skeleton's defaults: one passing round
// (plan.md slice 1; slice 2 sets the owner's two).
func DefaultSettings() Settings {
	return Settings{ReviewRounds: 1, ChecksGraceMinutes: 5}
}

// Validate refuses a value outside its range, naming the range.
func (s Settings) Validate() error {
	if s.ReviewRounds < ReviewRoundsMin || s.ReviewRounds > ReviewRoundsMax {
		return fmt.Errorf("review_rounds %d: allowed %d–%d", s.ReviewRounds, ReviewRoundsMin, ReviewRoundsMax)
	}
	if s.ChecksGraceMinutes < ChecksGraceMin || s.ChecksGraceMinutes > ChecksGraceMax {
		return fmt.Errorf("checks_grace_minutes %d: allowed %d–%d", s.ChecksGraceMinutes, ChecksGraceMin, ChecksGraceMax)
	}
	return nil
}
