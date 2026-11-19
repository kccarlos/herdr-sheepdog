package flow

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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
	ReviewRoundsMax = 1 // slice 2 raises it to 5 with L8
	ChecksGraceMin  = 1
	ChecksGraceMax  = 60
	StuckMin        = 5
	StuckMax        = 240
)

// DefaultSettings are the skeleton's defaults: one passing round
// (plan.md slice 1; slice 2 sets the owner's two).
func DefaultSettings() Settings {
	return Settings{ReviewRounds: 1, ChecksGraceMinutes: 5, StuckMinutes: 30}
}

// Changed lists every setting that differs from its default as "name
// value", sorted, for the coordinator's facts block (design.md 10).
func (s Settings) Changed() []string {
	var got, def map[string]any
	b, _ := json.Marshal(s)
	_ = json.Unmarshal(b, &got)
	b, _ = json.Marshal(DefaultSettings())
	_ = json.Unmarshal(b, &def)
	var out []string
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if got[k] != def[k] {
			out = append(out, fmt.Sprintf("%s %v", k, got[k]))
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
