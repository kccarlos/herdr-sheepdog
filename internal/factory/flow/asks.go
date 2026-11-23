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
	AskStatusOpen, AskStatusAnswered, AskStatusCancelled                                                         = "open", "answered", "cancelled"
	KindDecision, KindGate, KindRelease, KindPaidKey, KindDelete, KindInstall, KindReset, KindRule, KindOverride = "decision", "gate", "release", "paid-key", "delete", "install", "reset", "rule", "override"
	EvAskOpen, EvAskAnswer, EvAskCancel                                                                          = "ask_open", "answer", "cancel"
	AskNotifyOwner, AskDeliverAnswer, RefuseClosed, RefuseNotDelegable                                           = "notify_owner", "deliver_answer", "already_closed", "not_delegable"
	MaxAskQuestionLen, MinAskOptions, MaxAskOptions                                                              = 300, 2, 9
	GateOptionMerge, GateOptionHold                                                                              = "Merge", "Hold"
	N24OptionDrop, N24OptionContinue                                                                             = "Drop", "Continue"
	OverrideOptionMerge, OverrideOptionCancel                                                                    = "Yes, merge now", "No, wait"
	AskedByN24, AskedByN31                                                                                       = "N24", "N31"
)

// OverrideTarget is one task bound into an engine-filled override card (design.md 4.3b, plan.md 7.1).
type OverrideTarget struct {
	Task       string `json:"task"`
	PR         int    `json:"pr,omitempty"`
	Head       string `json:"head"`
	Checks     Checks `json:"checks"`
	Unreported bool   `json:"unreported,omitempty"`
}

// Ask is one structured question stored in State.Asks (design.md 3, 7.1).
type Ask struct {
	ID         string           `json:"id"`
	Project    string           `json:"project,omitempty"`
	Folder     string           `json:"folder,omitempty"`
	Kind       string           `json:"kind"`
	Gated      bool             `json:"gated"`
	Question   string           `json:"question"`
	Options    []string         `json:"options"`
	Recommend  string           `json:"recommend"`
	Task       string           `json:"task,omitempty"`
	Head       string           `json:"head,omitempty"`
	Targets    []OverrideTarget `json:"targets,omitempty"`
	AskedBy    string           `json:"asked_by,omitempty"`
	RunID      string           `json:"run_id,omitempty"`
	At         time.Time        `json:"at"`
	Status     string           `json:"status"`
	Answer     string           `json:"answer,omitempty"`
	Why        string           `json:"why,omitempty"`
	AnsweredAt time.Time        `json:"answered_at,omitempty"`
	AnsweredBy string           `json:"answered_by,omitempty"`
	ProofID    string           `json:"proof_id,omitempty"`
	DeviceID   string           `json:"device_id,omitempty"`
}

// AskEvent is one input to the pure ask table A2–A5 (design.md 7.1).
type AskEvent struct{ Kind, Answer, Why, By, ProofID, DeviceID string }

var (
	validAskKinds = []string{KindDecision, KindGate, KindRelease, KindPaidKey, KindDelete, KindInstall, KindReset, KindRule, KindOverride}
	parenKeyRE    = regexp.MustCompile(`\([^()\s]+\)$`)
	negWaitRE     = regexp.MustCompile(`^(?:no|not|don't|dont|do not|never|decline|reject|deny|cancel|abort|stop|wait|hold|defer|postpone|skip|pause)(?:$|[\s,.:;-])`)
	yesOptRE      = regexp.MustCompile(`^yes(?:$|[\s,.:;-])`)
)

// DelegableAsk reports whether a delegation rule may answer a (design.md 4.3b, 7.1).
// Slice 14's A1 must call this check.
func DelegableAsk(a Ask) bool {
	return a.Kind != KindOverride
}

// StepAsk applies pure ask table rows A2–A5 (design.md 7.1).
func StepAsk(a Ask, ev AskEvent, now time.Time) (Ask, string, Decision) {
	switch {
	case ev.Kind == EvAskOpen && a.Status == "":
		a.Status, a.Gated, a.At = AskStatusOpen, a.Kind != KindDecision, now
		return a, "A2", Decision{Asked: []string{map[bool]string{false: AskWake, true: AskNotifyOwner}[a.Gated]}, Why: "ask opened"}
	case (a.Status == AskStatusAnswered || a.Status == AskStatusCancelled) && (ev.Kind == EvAskAnswer || ev.Kind == EvAskCancel):
		return a, "A5", Decision{Why: RefuseClosed}
	case a.Status == AskStatusOpen && ev.Kind == EvAskAnswer:
		if ev.By == "rule" && !DelegableAsk(a) {
			return a, "", Decision{Why: RefuseNotDelegable}
		}
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
	a.Targets = slices.Clone(a.Targets)
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
		opt := CleanText(raw)
		if strings.ContainsAny(raw, "\n\r") || opt == "" || seen[opt] || parenKeyRE.MatchString(opt) {
			return Ask{}, "invalid_ask", fmt.Sprintf("Option %q must be unique, non-blank, single-line, and not end with a short parenthetical key.", raw)
		}
		opts[i], seen[opt] = opt, true
	}
	a.Question, a.Options, a.Recommend = q, opts, CleanText(a.Recommend)
	if a.Recommend == "" || !seen[a.Recommend] {
		return Ask{}, "invalid_ask", "Recommended option must be one of the options."
	}
	if negWaitRE.MatchString(strings.ToLower(a.Recommend)) && !slices.ContainsFunc(opts, func(o string) bool { return yesOptRE.MatchString(strings.ToLower(o)) }) {
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

// OpenOwnerAskForTask returns an open gated owner ask naming taskID (preferring
// KindGate when both exist), or nil (design.md 4.11, N31, N42).
func OpenOwnerAskForTask(asks []Ask, taskID string) *Ask {
	var fallback *Ask
	for i := range asks {
		a := &asks[i]
		if a.Task != taskID || a.Status != AskStatusOpen || !a.Gated {
			continue
		}
		if a.Kind == KindGate {
			return a
		}
		if fallback == nil {
			fallback = a
		}
	}
	return fallback
}

// OwnerAnsweredN24 reports whether the owner already answered an N24 ask for taskID.
func OwnerAnsweredN24(asks []Ask, taskID string) bool {
	return slices.ContainsFunc(asks, func(a Ask) bool {
		return a.Task == taskID && a.AskedBy == AskedByN24 && a.Status == AskStatusAnswered && a.AnsweredBy == "owner"
	})
}

// GateYesCoversH reports whether the owner's latest N31 gate answer covering h
// is GateOptionMerge (N31, design.md 3.1, 4.7).
func GateYesCoversH(asks []Ask, att *Attempt, taskID, h string) bool {
	if att == nil {
		return false
	}
	var latest *Ask
	for i := range asks {
		a := &asks[i]
		if a.Task == taskID && a.Kind == KindGate && a.AskedBy == AskedByN31 && a.Status == AskStatusAnswered &&
			a.AnsweredBy == "owner" && a.Head != "" && Covers(*att, a.Head, h) &&
			(latest == nil || !a.AnsweredAt.Before(latest.AnsweredAt)) {
			latest = a
		}
	}
	return latest != nil && strings.TrimSpace(latest.Answer) == GateOptionMerge
}

// GateNoSinceRecap returns an owner GateOptionHold on an N31 ask in this
// attempt with no recap decided since, or nil (N30, design.md 4.7).
func GateNoSinceRecap(asks []Ask, t Task, att *Attempt) *Ask {
	if att == nil {
		return nil
	}
	var lastRecap time.Time
	for _, rc := range t.Recaps {
		if rc.Decided != nil && rc.Decided.At.After(lastRecap) {
			lastRecap = rc.Decided.At
		}
	}
	for i := len(asks) - 1; i >= 0; i-- {
		a := &asks[i]
		inAttempt := a.Head != "" && (slices.ContainsFunc(att.Ready, func(r Ready) bool { return r.Head == a.Head }) ||
			slices.ContainsFunc(att.MergeOnly, func(m Advance) bool { return m.From == a.Head || m.To == a.Head }))
		if a.Task == t.ID && a.Kind == KindGate && a.AskedBy == AskedByN31 && a.Status == AskStatusAnswered &&
			a.AnsweredBy == "owner" && strings.TrimSpace(a.Answer) == GateOptionHold && inAttempt &&
			(lastRecap.IsZero() || a.AnsweredAt.After(lastRecap)) {
			return a
		}
	}
	return nil
}

// AskAnswerWords formats an answered ask's chosen option and optional reason.
func AskAnswerWords(a Ask) string {
	if a.Why != "" {
		return a.Answer + " — " + a.Why
	}
	return a.Answer
}

// LatestOwnerAskAnswer returns the owner's latest answered ask words for taskID and askedBy.
func LatestOwnerAskAnswer(asks []Ask, taskID, askedBy string) string {
	for i := len(asks) - 1; i >= 0; i-- {
		if a := asks[i]; a.Task == taskID && a.AskedBy == askedBy && a.Status == AskStatusAnswered && a.AnsweredBy == "owner" {
			return AskAnswerWords(a)
		}
	}
	return ""
}

// FormatAskCommand renders the server-formatted command text for an ask card
// (S0.10, design.md 4.3b, plan.md 7.1).
func FormatAskCommand(a Ask) string {
	if a.Kind == KindOverride && len(a.Targets) > 0 {
		lines := make([]string, len(a.Targets))
		for i, t := range a.Targets {
			line := t.Task
			if t.PR > 0 {
				line += fmt.Sprintf(" #%d", t.PR)
			}
			if t.Head != "" {
				line += " @ " + t.Head[:min(len(t.Head), 7)]
			}
			if t.Checks != "" {
				line += " · " + string(t.Checks)
			}
			if t.Unreported {
				line += " · has unreported work"
			}
			lines[i] = line
		}
		return strings.Join(lines, "\n")
	}
	cmd := a.Kind
	if a.Task != "" {
		cmd += " · " + a.Task
	}
	if a.Head != "" {
		cmd += " @ " + a.Head[:min(len(a.Head), 7)]
	}
	return cmd
}
