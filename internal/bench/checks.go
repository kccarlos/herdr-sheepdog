package bench

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// failures labels a finished run with the shared taxonomy.
func (r *runner) failures() []eval.Failure {
	run, sc := r.run, r.sc
	var fs []eval.Failure
	for _, fb := range run.Fallbacks {
		switch {
		case eval.IsRateLimited(fb):
			fs = append(fs, eval.Failure{Type: eval.FailError, Detail: "orchestrator rate limited: " + fb})
		case eval.IsTimeoutText(fb) || strings.HasPrefix(fb, "timeout"):
			fs = append(fs, eval.Failure{Type: eval.FailTimeout, Detail: "orchestrator turn timed out: " + fb})
		default:
			fs = append(fs, eval.Failure{Type: eval.FailBrainFallback, Detail: fb})
		}
	}
	fs = append(fs, eval.ActionFailures(sc.Expect, run.Actions)...)
	for _, v := range run.Verdicts {
		switch {
		case v.Err != "":
			fs = append(fs, eval.Failure{Type: eval.FailError, Detail: "judge " + v.Name + ": " + v.Err})
		case !v.Passed():
			fs = append(fs, eval.Failure{Type: eval.FailJudgeNo, Detail: v.Name + ": " + orStr(eval.FirstLine(v.Rationale), "no rationale")})
		}
	}
	fs = append(fs, voiceChecks(sc, *run)...)
	fs = append(fs, r.eventFails...)
	if len(fs) > 0 {
		switch run.EndReason {
		case eval.EndMaxTime:
			fs = append(fs, eval.Failure{Type: eval.FailTimeout, Detail: "the conversation hit its time cap"})
		case eval.EndSilence:
			fs = append(fs, eval.Failure{Type: eval.FailSilence, Detail: "the user waited in silence past the cap"})
		case eval.EndMaxTurns:
			fs = append(fs, eval.Failure{Type: eval.FailMaxTurns, Detail: "every turn used without the call ending"})
		}
	}
	return fs
}

// voiceChecks are the voice-layer checks: a user line the voice model did
// not hear at all, the opening's expected words, and the scenario's
// event expectations.
func voiceChecks(sc Scenario, run Run) []eval.Failure {
	var fs []eval.Failure
	for _, l := range run.User {
		if l.Heard == "" && !l.BargeIn {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: fmt.Sprintf("voice: the voice model heard nothing of %q", l.Said)})
		}
	}
	if alts := sc.Voice.OpeningHeard; len(alts) > 0 && len(run.User) > 0 {
		ok := false
		for _, a := range alts {
			if fuzzyContains(run.User[0].Heard, a) {
				ok = true
				break
			}
		}
		if !ok {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: fmt.Sprintf("voice: the opening was heard as %q (want one of %q)", run.User[0].Heard, alts)})
		}
	}
	return fs
}

// voiceEventChecks checks the scenario's event expectations against the
// recorded events.
func voiceEventChecks(sc Scenario, evs []voiceclient.Event) []eval.Failure {
	var fs []eval.Failure
	for _, m := range sc.Voice.Events {
		if !anyEvent(evs, m) {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: "voice: no " + describe(m)})
		}
	}
	for _, m := range sc.Voice.Absent {
		if anyEvent(evs, m) {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: "voice: unexpected " + describe(m)})
		}
	}
	return fs
}

func describe(m EventMatch) string {
	s := m.Type
	if len(m.Payload) > 0 {
		b, _ := json.Marshal(m.Payload)
		s += " " + string(b)
	}
	if m.Note != "" {
		s += " (" + m.Note + ")"
	}
	return s
}

func anyEvent(evs []voiceclient.Event, m EventMatch) bool {
	for _, ev := range evs {
		if ev.Type != m.Type {
			continue
		}
		ok := true
		for k, want := range m.Payload {
			if fmt.Sprint(ev.Payload[k]) != fmt.Sprint(want) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// fuzzyContains: every token of want appears in got, in order.
func fuzzyContains(got, want string) bool {
	g, w := Tokens(got), Tokens(want)
	i := 0
	for _, t := range g {
		if i < len(w) && t == w[i] {
			i++
		}
	}
	return i == len(w)
}
