package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// The live-only wording checks the judge decides when one is configured
// are spoken_contains_any_of, first_sentence_contains_any_of and
// spoken_no_jargon. The code versions are a substring match and a
// stand-in word list: they miss a paraphrase, a Chinese reply, a jargon
// word nobody listed. The structural checks (word and sentence counts,
// needs_user, urgency, ask, reply language) stay in code, where they are
// exact.

// svpCriteria turns a case's wording checks and its own judge list into
// judge criteria, each tagged with the check it stands for.
func svpCriteria(exp SVPExpect) (criteria, checks []string) {
	if len(exp.SpokenContainsAnyOf) > 0 {
		criteria = append(criteria, fmt.Sprintf("Does sheepdog's reply convey, in any language or wording, at least one of these: %s?", quoteList(exp.SpokenContainsAnyOf)))
		checks = append(checks, "spoken_contains_any_of")
	}
	if len(exp.FirstSentenceContainsAnyOf) > 0 {
		criteria = append(criteria, fmt.Sprintf("Does the first sentence of sheepdog's reply lead with, in any language or wording, one of these: %s?", quoteList(exp.FirstSentenceContainsAnyOf)))
		checks = append(checks, "first_sentence_contains_any_of")
	}
	if exp.SpokenNoJargon {
		criteria = append(criteria, "Is sheepdog's reply free of jargon a non-technical listener would have to parse (internal component names, file paths, PR or issue numbers, version numbers, milliseconds, tool names)?")
		checks = append(checks, "spoken_no_jargon")
	}
	for _, c := range exp.Judge {
		criteria = append(criteria, c)
		checks = append(checks, "judge")
	}
	return criteria, checks
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

// judgeSVP asks the judge every criterion of c about the one reply, and
// returns the verdicts and a failure reason per no (or judge error).
func judgeSVP(ctx context.Context, j Judge, c SVPCase, req brain.Request, reply brain.Reply, rec *recorder, host *braintest.Host) ([]Verdict, []string) {
	criteria, checks := svpCriteria(c.Expect)
	if len(criteria) == 0 {
		return nil, nil
	}
	transcript := svpTranscript(c, req, reply)
	facts := svpFacts(host, rec)
	verdicts := judgeAll(ctx, j, criteria, transcript, facts, true)
	var reasons []string
	for i, v := range verdicts {
		switch {
		case v.Err != "":
			reasons = append(reasons, fmt.Sprintf("%s: judge error: %s", reasonJudgeError, v.Err))
		case !v.Passed():
			why := v.Rationale
			if why == "" {
				why = "no rationale"
			}
			reasons = append(reasons, fmt.Sprintf("%s (%s): %s — %s", reasonJudgeNo, checks[i], v.Criterion, firstLine(why)))
		}
	}
	return verdicts, reasons
}

// Reason prefixes the failure classifier keys on.
const (
	reasonJudgeNo    = "judge said no"
	reasonJudgeError = "judge"
	reasonFallback   = "the turn fell back (no brain reply)"
)

// svpTranscript renders the case's conversation as the CEO lived it.
func svpTranscript(c SVPCase, req brain.Request, reply brain.Reply) string {
	var b strings.Builder
	for _, line := range c.Transcript {
		switch {
		case strings.HasPrefix(line, "CEO: "):
			b.WriteString(line)
		case strings.HasPrefix(line, "Dog: "):
			b.WriteString("Sheepdog: " + strings.TrimPrefix(line, "Dog: "))
		case strings.HasPrefix(line, "PUSH "):
			b.WriteString("[worker event] " + strings.TrimPrefix(line, "PUSH "))
		default:
			b.WriteString("CEO: " + line)
		}
		b.WriteString("\n")
	}
	if req.Kind.IsEvent() {
		fmt.Fprintf(&b, "[worker event: %s %s; sheepdog announces it unprompted]\n", req.Agent, req.Event)
	} else {
		fmt.Fprintf(&b, "CEO: %s\n", req.Question)
	}
	spoken := strings.TrimSpace(reply.Spoken)
	if a := strings.TrimSpace(reply.Ask); a != "" {
		spoken = strings.TrimSpace(spoken + " " + a)
	}
	if spoken == "" {
		spoken = "(says nothing)"
	}
	fmt.Fprintf(&b, "Sheepdog: %s\n", spoken)
	return b.String()
}

// svpFacts is the ground truth for the judge: each agent's screen
// (clipped) and the tools the brain called.
func svpFacts(host *braintest.Host, rec *recorder) string {
	var b strings.Builder
	for _, a := range host.Agents() {
		fmt.Fprintf(&b, "Agent %s (%s, %s)", a.Alias, a.Kind, a.State)
		if a.Pending != nil {
			fmt.Fprintf(&b, ", waiting at a prompt: %q", a.Pending.Question)
		}
		b.WriteString("\n")
		if scr, err := host.ReadScreen(context.Background(), a.PaneID, 0); err == nil && scr != "" {
			b.WriteString("  screen (last lines):\n")
			for _, l := range lastLines(scr, 30) {
				b.WriteString("    " + l + "\n")
			}
		}
	}
	if names := rec.names(); len(names) > 0 {
		fmt.Fprintf(&b, "Tools sheepdog called this turn: %s\n", strings.Join(names, ", "))
	} else {
		b.WriteString("Sheepdog called no tools this turn.\n")
	}
	return b.String()
}

func lastLines(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// classifySVP labels an SVP result's failure reasons with the taxonomy.
func classifySVP(reasons []string, err error) []Failure {
	var fs []Failure
	if err != nil {
		t := FailError
		if !isRateLimited(err) && isTimeoutText(err.Error()) {
			t = FailTimeout
		}
		fs = append(fs, Failure{t, firstLine(err.Error())})
	}
	for _, r := range reasons {
		fs = append(fs, Failure{classifyReason(r), r})
	}
	return fs
}

// classifyReason maps one failure reason (svp.go's wording) to a label.
func classifyReason(r string) FailureType {
	switch {
	case strings.HasPrefix(r, reasonFallback):
		switch {
		case isRateLimited(errorString(r)):
			return FailError
		case isTimeoutText(r):
			return FailTimeout
		}
		return FailBrainFallback
	case strings.HasPrefix(r, reasonJudgeNo):
		return FailJudgeNo
	case strings.HasPrefix(r, reasonJudgeError+": judge error"):
		return FailError
	case strings.HasPrefix(r, "none of [") || strings.HasPrefix(r, "read_screen with source"):
		return FailMissingCall
	case strings.Contains(r, "completed successfully but must not") || strings.HasPrefix(r, "a prompt to a worker contains"):
		return FailUnintendedAction
	}
	return FailCheck
}
