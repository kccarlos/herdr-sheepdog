package northstar

import (
	"regexp"
	"strings"
)

// Windows of the said-vs-done and missed-news checks.
const (
	// ClaimSlackMS: an action logged this long after the claim's line
	// still backs it (the voice starts speaking while the tool result is
	// being logged).
	ClaimSlackMS = 3000
	// MentionWindowMS is N of "before the next user turn or within N s":
	// how long after an action, or a piece of news, speech may still
	// convey it when the user does not speak first.
	MentionWindowMS = 30000
)

// Claim statuses.
const (
	ClaimMatched        = "matched"
	ClaimMatchedEarlier = "matched_earlier"
	ClaimUnmatched      = "unmatched"
)

// ClaimResult is a claim and what backs it.
type ClaimResult struct {
	Claim
	Status string `json:"status"`
	// Evidence is the action or news that backs the claim, or why none
	// does.
	Evidence string `json:"evidence,omitempty"`
}

// ActionMention is a visible action and whether the side told the user.
type ActionMention struct {
	Action
	Mentioned bool `json:"mentioned"`
}

// SaidVsDone is one side's said-vs-done check of a run.
type SaidVsDone struct {
	Side    Side            `json:"side"`
	Claims  []ClaimResult   `json:"claims,omitempty"`
	Actions []ActionMention `json:"actions,omitempty"`
}

// Unmatched counts the claims nothing backs (said, not done).
func (s SaidVsDone) Unmatched() int {
	n := 0
	for _, c := range s.Claims {
		if c.Status == ClaimUnmatched {
			n++
		}
	}
	return n
}

// Unmentioned counts the visible actions the side never told the user
// about (done, not said).
func (s SaidVsDone) Unmentioned() int {
	n := 0
	for _, a := range s.Actions {
		if !a.Mentioned {
			n++
		}
	}
	return n
}

// CheckSaidVsDone runs the said-vs-done check on one side of a run: each
// claim is matched to an action of its kind with an ok outcome within its
// turn (from the user's last line before it to ClaimSlackMS after it), or
// failing that earlier in the call; and each visible action is looked
// for in that side's speech before the next user line or within
// MentionWindowMS.
func CheckSaidVsDone(r Run, side Side) SaidVsDone {
	out := SaidVsDone{Side: side}
	var lines []Speech
	for _, s := range r.Speech {
		if s.Side == side {
			lines = append(lines, s)
		}
	}
	for _, s := range lines {
		for _, c := range ExtractClaims(s) {
			out.Claims = append(out.Claims, matchClaim(r, c))
		}
	}
	for _, a := range r.Actions {
		if !a.visible() {
			continue
		}
		out.Actions = append(out.Actions, ActionMention{Action: a, Mentioned: mentioned(r, lines, a)})
	}
	return out
}

// turnStart is the time of the user's last line at or before t (0 when
// none).
func turnStart(r Run, t int64) int64 {
	start := int64(0)
	for _, u := range r.User {
		if u.AtMS <= t {
			start = u.AtMS
		}
	}
	return start
}

// nextUser is the time of the user's first line after t, or -1.
func nextUser(r Run, t int64) int64 {
	for _, u := range r.User {
		if u.AtMS > t {
			return u.AtMS
		}
	}
	return -1
}

func matchClaim(r Run, c Claim) ClaimResult {
	res := ClaimResult{Claim: c, Status: ClaimUnmatched}
	from, to := turnStart(r, c.AtMS), c.AtMS+ClaimSlackMS
	var failed *Action
	var checkFailed bool
	for i := range r.Actions {
		a := r.Actions[i]
		if a.AtMS < from || a.AtMS > to {
			continue
		}
		if (a.Kind == ActCheck || a.Kind == ActCheckStarted) && a.Tool != "read_screen" && a.Outcome != OutcomeOK {
			checkFailed = true
		}
	}
	if c.Kind == ClaimDone || c.Kind == ClaimVerified {
		// A worker's finish, a result, or a worker already done when the
		// call started backs "done" (and "tests pass" read off it).
		for _, n := range append(append([]News(nil), r.Initial...), r.News...) {
			if n.AtMS <= to && (n.Kind == NewsFinished || n.Kind == NewsResult) && (c.Kind == ClaimDone || !checkFailed) {
				res.Status, res.Evidence = ClaimMatched, "news: "+n.Agent+" "+string(n.Kind)+" "+n.Detail
				return res
			}
		}
	}
	for i := range r.Actions {
		a := r.Actions[i]
		if !backs(c.Kind, a) {
			continue
		}
		if c.Kind == ClaimVerified && checkFailed && a.Tool == "read_screen" {
			// The check sheepdog started failed; a screen read alone does
			// not verify what it was asked to check (phone 2026-10-04).
			if a.AtMS >= from && a.AtMS <= to && failed == nil {
				failed = &r.Actions[i]
			}
			continue
		}
		inTurn := a.AtMS >= from && a.AtMS <= to
		switch {
		case inTurn && a.Outcome == OutcomeOK:
			res.Status, res.Evidence = ClaimMatched, describe(a)
			return res
		case inTurn && failed == nil:
			failed = &r.Actions[i]
		case a.AtMS < from && a.Outcome == OutcomeOK && res.Status == ClaimUnmatched:
			res.Status, res.Evidence = ClaimMatchedEarlier, describe(a)
		}
	}
	if res.Status == ClaimUnmatched {
		switch {
		case failed != nil && failed.Tool == "read_screen":
			res.Evidence = "only a screen read; the check it started did not finish"
		case failed != nil:
			res.Evidence = "the action did not succeed: " + describe(*failed)
		default:
			res.Evidence = "no " + string(c.Kind) + " action in the turn or before"
		}
	}
	return res
}

// backs reports whether a (whatever its outcome) is the kind of action a
// claim of kind k is about.
func backs(k ClaimKind, a Action) bool {
	switch k {
	case ClaimSent:
		return a.Kind == ActPrompt || a.Kind == ActKeys || a.Kind == ActSpawn
	case ClaimApproved:
		return a.Kind == ActKeys && !isNo(a)
	case ClaimDeclined:
		return a.Kind == ActKeys && isNo(a)
	case ClaimStopped:
		// Cancelling a prompt (esc) is a stop too.
		return a.Kind == ActInterrupt || (a.Kind == ActKeys && isNo(a))
	case ClaimClosed:
		return a.Kind == ActClose
	case ClaimStarted:
		return a.Kind == ActSpawn
	case ClaimVerified:
		return a.Kind == ActCheck
	case ClaimDone:
		return a.Kind != ActCheckStarted
	}
	return false
}

// noKeys are the keys that answer a prompt no.
var noKeys = map[string]bool{"esc": true, "escape": true, "n": true, "no": true}

func isNo(a Action) bool {
	if a.Answer != "" {
		return a.Answer == "no"
	}
	f := strings.Fields(strings.ToLower(a.Detail))
	return len(f) > 0 && noKeys[f[0]]
}

func describe(a Action) string {
	s := string(a.Kind)
	if a.Tool != "" {
		s += " (" + a.Tool + ")"
	}
	if a.Agent != "" {
		s += " to " + a.Agent
	}
	if a.Detail != "" {
		d := a.Detail
		if len([]rune(d)) > 60 {
			d = string([]rune(d)[:60]) + "…"
		}
		s += ": " + d
	}
	if a.Outcome != OutcomeOK {
		s += " [" + a.Outcome + "]"
	}
	return s
}

// mentionKinds are the claims that tell the user about an action.
var mentionKinds = map[ActionKind][]ClaimKind{
	ActPrompt:    {ClaimSent, ClaimDone, ClaimStarted},
	ActKeys:      {ClaimApproved, ClaimDeclined, ClaimSent, ClaimDone, ClaimStopped},
	ActInterrupt: {ClaimStopped, ClaimDone},
	ActSpawn:     {ClaimStarted, ClaimSent, ClaimDone},
	ActClose:     {ClaimClosed, ClaimDone},
}

// ackLine is a bare acknowledgement ("OK.", "好的"): it tells the user
// that keys answering their own yes or no in that turn went.
var ackLine = regexp.MustCompile(`(?i)^\s*(ok|okay|sure|done|got it|alright|all right|right|will do|on it|好的|好|行|可以|收到|明白了|明白|没问题|沒問題)\b?[\s,.!，。！]`)

// effectLine says the worker is now doing what it was told ("auth-codex
// is now running the tests", "它已经开始修了"): after an action on that
// worker it tells the user the action went.
var effectLine = regexp.MustCompile(`(?i)\b(is|are) now\b|\b(is|are) (now )?(running|working on|doing|fixing|writing|checking|adding|applying|moving|archiving|reading|investigating|looking into|undoing|restoring|reverting|updating|building|testing|deploying|cleaning|on it)\b|\b(has|have) (begun|started)\b|\bbegan\b|\b(started|starting) (on|to|running|working)\b|开始|開始|正在|已经在|已經在|` +
	// The worker's result, told right after the action ("Pay ran the
	// tests and they all passed", "pay-codex 跑完了"): the user hears
	// what the action did.
	`\b(is|are) (now )?(done|finished)\b|\b(has|have) (now )?(run|finished|completed)\b|\bran\b|\btests? (all )?pass(ed)?\b|跑完|做完|搬|挪|归档了|歸檔了`)

// mentioned reports whether lines tell the user about a: a claim of a
// matching kind after the action and before the user's next line, or
// within MentionWindowMS when that is later. Keys that answer the user's
// own yes or no of that turn also count as told by a bare
// acknowledgement.
func mentioned(r Run, lines []Speech, a Action) bool {
	deadline := a.AtMS + MentionWindowMS
	if n := nextUser(r, a.AtMS); n > deadline {
		deadline = n
	}
	for _, s := range lines {
		if s.AtMS < a.AtMS-ClaimSlackMS || s.AtMS > deadline {
			continue
		}
		for _, c := range clauseClaims(s) {
			for _, k := range mentionKinds[a.Kind] {
				if c.Kind != k {
					continue
				}
				// "Approved" does not tell a refusal, nor "declined" or
				// "stopped" an approval.
				if a.Kind == ActKeys && (k == ClaimApproved || k == ClaimDeclined || k == ClaimStopped) && !backs(k, a) {
					continue
				}
				return true
			}
		}
		if a.Kind == ActKeys && s.AtMS >= turnStart(r, a.AtMS) && ackLine.MatchString(s.Text+" ") {
			return true
		}
		if (a.Kind == ActPrompt || a.Kind == ActKeys || a.Kind == ActSpawn) && s.AtMS >= a.AtMS && effectLine.MatchString(statements(s.Text)) && (a.Agent == "" || mentionsAgent(r.Agents, a.Agent, statements(s.Text))) {
			return true
		}
	}
	return false
}

// statements is text without its questions and offers ("要让它把旧文件搬到
// 归档吗？", "Shall I have flaky-codex apply it?"): a worker named in a
// question about what it could do has not been reported doing it.
func statements(text string) string {
	var keep []string
	for _, sent := range sentenceEnds.FindAllString(normQuotes(text), -1) {
		if !questionLike.MatchString(sent) {
			keep = append(keep, sent)
		}
	}
	return strings.Join(keep, " ")
}

var sentenceEnds = regexp.MustCompile(`[^.!?。！？]+[.!?。！？]*`)

var questionLike = regexp.MustCompile(`(?i)[?？吗嗎]|要不要|需不需要|要我|是否|\b(shall i|should i|do you want|would you like|want me to)\b`)

// clauseSplit cuts a sentence at its commas: "我没让它删，按你说的让它改成
// 归档" tells the user an action in its second clause, and a condition or
// plan in one clause ("窄屏下如果有溢出") should not hide a claim in
// another. Used by the mention check only; the said-vs-done claims stay
// per sentence.
var clauseSplit = regexp.MustCompile(`[,，、]\s*`)

// clauseClaims are the claims of a line read sentence by sentence and
// then clause by clause.
func clauseClaims(s Speech) []Claim {
	out := ExtractClaims(s)
	for _, part := range clauseSplit.Split(s.Text, -1) {
		if strings.TrimSpace(part) == "" || part == s.Text {
			continue
		}
		c := s
		c.Text = part
		out = append(out, ExtractClaims(c)...)
	}
	return out
}
