package northstar

import (
	"strings"
	"unicode"
)

// Thresholds of the user-effort check: two consecutive user lines whose
// token overlap (shared distinct tokens over the smaller line's) reaches
// RepeatOverlap are a repeat, RephraseOverlap a rephrasing. Lines shorter
// than MinEffortTokens are left out ("yes", "ok").
const (
	RepeatOverlap   = 0.8
	RephraseOverlap = 0.5
	MinEffortTokens = 3
)

// Effort is a run's user effort.
type Effort struct {
	// GoalReached: the judge said the goal was answered (or, without a
	// judge, the simulated user said so at end_call).
	GoalReached bool `json:"goal_reached"`
	// Known is false when neither a verdict nor the user's opinion says.
	Known       bool  `json:"known"`
	Turns       int   `json:"turns"`
	TimeToGoal  int64 `json:"time_to_goal_ms,omitempty"`
	Repeats     int   `json:"repeats"`
	Rephrasings int   `json:"rephrasings"`
	// Pairs lists the repeated or rephrased line pairs.
	Pairs [][2]string `json:"pairs,omitempty"`
	// CapEnded: a cap (turns, time, silence) ended the call.
	CapEnded bool `json:"cap_ended,omitempty"`
}

// Low reports the run's low-effort verdict: the goal was reached, no line
// was repeated or rephrased, and no cap ended the call.
func (e Effort) Low() bool {
	return e.GoalReached && e.Repeats == 0 && e.Rephrasings == 0 && !e.CapEnded
}

// CheckEffort measures a run's user effort.
func CheckEffort(r Run) Effort {
	e := Effort{Turns: r.Turns}
	if e.Turns == 0 {
		e.Turns = len(r.User)
	}
	switch v := r.verdict("answered"); {
	case v == "yes" || v == "no":
		e.Known, e.GoalReached = true, v == "yes"
	case r.GoalMetSelf != nil:
		e.Known, e.GoalReached = true, *r.GoalMetSelf
	}
	switch r.EndReason {
	case "max_turns", "max_time", "silence":
		e.CapEnded = true
	}
	if e.GoalReached {
		// The goal was reached by sheepdog's last line of the call.
		for _, s := range r.Speech {
			if s.Side == r.Heard && s.AtMS > e.TimeToGoal {
				e.TimeToGoal = s.AtMS
			}
		}
	}
	for i := 1; i < len(r.User); i++ {
		a, b := r.User[i-1].Text, r.User[i].Text
		ov, ok := Overlap(a, b)
		if !ok {
			continue
		}
		switch {
		case ov >= RepeatOverlap:
			e.Repeats++
			e.Pairs = append(e.Pairs, [2]string{a, b})
		case ov >= RephraseOverlap:
			e.Rephrasings++
			e.Pairs = append(e.Pairs, [2]string{a, b})
		}
	}
	return e
}

// Overlap is the shared distinct tokens of a and b over the smaller set's
// size (words for Latin text, character bigrams for Chinese); ok is false
// when either line has fewer than MinEffortTokens tokens.
func Overlap(a, b string) (float64, bool) {
	sa, sb := tokenSet(a), tokenSet(b)
	if len(sa) < MinEffortTokens || len(sb) < MinEffortTokens {
		return 0, false
	}
	small, big := sa, sb
	if len(sb) < len(sa) {
		small, big = sb, sa
	}
	shared := 0
	for t := range small {
		if big[t] {
			shared++
		}
	}
	return float64(shared) / float64(len(small)), true
}

// tokenSet is the distinct lower-case words of s, with every run of Han
// characters split into its character bigrams (a single character stands
// alone).
func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	var word []rune
	var han []rune
	flushWord := func() {
		if len(word) > 0 {
			out[strings.ToLower(string(word))] = true
			word = word[:0]
		}
	}
	flushHan := func() {
		switch {
		case len(han) == 1:
			out[string(han)] = true
		case len(han) > 1:
			for i := 0; i+1 < len(han); i++ {
				out[string(han[i:i+2])] = true
			}
		}
		han = han[:0]
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}
