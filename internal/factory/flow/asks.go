package flow

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	AskStatusOpen, AskStatusAnswered, AskStatusCancelled                                           = "open", "answered", "cancelled"
	KindDecision, KindGate, KindRelease, KindPaidKey, KindDelete, KindInstall, KindReset, KindRule = "decision", "gate", "release", "paid-key", "delete", "install", "reset", "rule"
	EvAskOpen, EvAskAnswer, EvAskCancel                                                            = "ask_open", "answer", "cancel"
	AskNotifyOwner, AskDeliverAnswer, RefuseClosed                                                 = "notify_owner", "deliver_answer", "already_closed"
	MaxAskQuestionLen, MinAskOptions, MaxAskOptions                                                = 300, 2, 9
)

// Ask is one structured question stored in State.Asks (design.md 3, 7.1).
type Ask struct {
	ID         string    `json:"id"`
	Project    string    `json:"project,omitempty"`
	Folder     string    `json:"folder,omitempty"`
	Kind       string    `json:"kind"`
	Gated      bool      `json:"gated"`
	Question   string    `json:"question"`
	Options    []string  `json:"options"`
	Recommend  string    `json:"recommend"`
	Task       string    `json:"task,omitempty"`
	Head       string    `json:"head,omitempty"`
	AskedBy    string    `json:"asked_by,omitempty"`
	RunID      string    `json:"run_id,omitempty"`
	At         time.Time `json:"at"`
	Status     string    `json:"status"`
	Answer     string    `json:"answer,omitempty"`
	Why        string    `json:"why,omitempty"`
	AnsweredAt time.Time `json:"answered_at,omitempty"`
	AnsweredBy string    `json:"answered_by,omitempty"`
	ProofID    string    `json:"proof_id,omitempty"`
	DeviceID   string    `json:"device_id,omitempty"`
}

// AskEvent is one input to the pure ask table A2–A5 (design.md 7.1).
type AskEvent struct{ Kind, Answer, Why, By, ProofID, DeviceID string }

var (
	validAskKinds = []string{KindDecision, KindGate, KindRelease, KindPaidKey, KindDelete, KindInstall, KindReset, KindRule}
	parenKeyRE    = regexp.MustCompile(`\([^()\s]+\)$`)
	negWaitRE     = regexp.MustCompile(`^(?:no|not|don't|dont|do not|never|decline|reject|deny|cancel|abort|stop|wait|hold|defer|postpone|skip|pause)(?:$|[\s,.:;-])`)
	yesOptRE      = regexp.MustCompile(`^yes(?:$|[\s,.:;-])`)
)

// StepAsk applies pure ask table rows A2–A5 (design.md 7.1).
func StepAsk(a Ask, ev AskEvent, now time.Time) (Ask, string, Decision) {
	switch {
	case ev.Kind == EvAskOpen && a.Status == "":
		a.Status, a.Gated, a.At = AskStatusOpen, a.Kind != KindDecision, now
		return a, "A2", Decision{Asked: []string{map[bool]string{false: AskWake, true: AskNotifyOwner}[a.Gated]}, Why: "ask opened"}
	case (a.Status == AskStatusAnswered || a.Status == AskStatusCancelled) && (ev.Kind == EvAskAnswer || ev.Kind == EvAskCancel):
		return a, "A5", Decision{Why: RefuseClosed}
	case a.Status == AskStatusOpen && ev.Kind == EvAskAnswer:
		a.Status, a.Answer, a.Why, a.AnsweredBy, a.AnsweredAt, a.ProofID, a.DeviceID = AskStatusAnswered, ev.Answer, ev.Why, ev.By, now, ev.ProofID, ev.DeviceID
		return a, "A3", Decision{Asked: []string{map[bool]string{false: AskWake, true: AskDeliverAnswer}[a.RunID != "" && a.AskedBy != RoleCoordinator]}, Why: "ask answered"}
	case a.Status == AskStatusOpen && ev.Kind == EvAskCancel:
		a.Status = AskStatusCancelled
		return a, "A4", Decision{Why: "ask cancelled"}
	}
	return a, "", Decision{}
}

// ValidateAsk checks KB-ASK-6, KB-ASK-9 and KB-ASK-15 and returns the cleaned Ask.
func ValidateAsk(a Ask) (Ask, string, string) {
	a.Kind, a.Task, a.Gated = strings.TrimSpace(a.Kind), strings.TrimSpace(a.Task), strings.TrimSpace(a.Kind) != KindDecision
	q := CleanText(a.Question)
	switch {
	case !slices.Contains(validAskKinds, a.Kind):
		return Ask{}, "unknown_kind", fmt.Sprintf("Unknown ask kind %q.", a.Kind)
	case strings.ContainsAny(a.Question, "\n\r") || q == "" || len([]rune(q)) > MaxAskQuestionLen:
		return Ask{}, "invalid_ask", fmt.Sprintf("Question must be a single line of 1 to %d characters.", MaxAskQuestionLen)
	case len(a.Options) < MinAskOptions || len(a.Options) > MaxAskOptions:
		return Ask{}, "invalid_ask", fmt.Sprintf("Ask must have %d to %d options.", MinAskOptions, MaxAskOptions)
	}
	opts, seen := make([]string, len(a.Options)), make(map[string]bool, len(a.Options))
	for i, raw := range a.Options {
		if opt := CleanText(raw); strings.ContainsAny(raw, "\n\r") || opt == "" || seen[opt] || parenKeyRE.MatchString(opt) {
			return Ask{}, "invalid_ask", fmt.Sprintf("Option %q must be unique, non-blank, single-line, and not end with a short parenthetical key.", raw)
		} else {
			opts[i], seen[opt] = opt, true
		}
	}
	a.Question, a.Options, a.Recommend = q, opts, CleanText(a.Recommend)
	switch {
	case a.Recommend == "" || !seen[a.Recommend]:
		return Ask{}, "invalid_ask", "Recommended option must be one of the options."
	case negWaitRE.MatchString(strings.ToLower(a.Recommend)) && !slices.ContainsFunc(opts, func(o string) bool { return yesOptRE.MatchString(strings.ToLower(o)) }):
		return Ask{}, "invalid_ask", "When the recommended option is negative or wait, another option must start with Yes."
	}
	return a, "", ""
}

// ResolveAskOption matches raw against options by exact label or 1-based index.
func ResolveAskOption(options []string, raw string) (string, bool) {
	if s := CleanText(raw); slices.Contains(options, s) {
		return s, true
	} else if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
		return options[n-1], true
	}
	return "", false
}
