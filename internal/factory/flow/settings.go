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
	ReviewRounds int `json:"review_rounds"`
}

// Range limits for each setting.
const (
	ReviewRoundsMin = 1
	ReviewRoundsMax = 5
)

// DefaultSettings are the skeleton's defaults: one passing round
// (plan.md slice 1; slice 2 sets the owner's two).
func DefaultSettings() Settings {
	return Settings{ReviewRounds: 1}
}

// Validate refuses a value outside its range, naming the range.
func (s Settings) Validate() error {
	if s.ReviewRounds < ReviewRoundsMin || s.ReviewRounds > ReviewRoundsMax {
		return fmt.Errorf("review_rounds %d: allowed %d–%d", s.ReviewRounds, ReviewRoundsMin, ReviewRoundsMax)
	}
	return nil
}
