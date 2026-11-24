package flow

import (
	"cmp"
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
	RuleOptionSave, RuleOptionKeep                                                                               = "Save", "Keep current"
	AskedByN24, AskedByN31                                                                                       = "N24", "N31"
)

// SettingChange is the typed setting mutation on an engine-opened rule ask (design.md 10.1 SE1).
type SettingChange struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Was   string `json:"was"`
}

// RuleQuestion formats the read-back question for an engine-opened setting proposal (SE1, SE3).
func RuleQuestion(c SettingChange) string {
	return fmt.Sprintf("This project: %s %s (now %s). Save?", settingLabel(c.Name), cmp.Or(c.Value, "empty"), cmp.Or(c.Was, "empty"))
}

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
	Set        *SettingChange   `json:"set,omitempty"`
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
)

// DelegableAsk reports whether a delegation rule may answer a (design.md 4.3b, 7.1).
// Slice 14's A1 must call this check.
func DelegableAsk(a Ask) bool {
	return a.Kind != KindOverride && a.Kind != KindRule
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
		var asked []string
		if a.RunID != "" && a.AskedBy != RoleCoordinator {
			asked = []string{AskDeliverAnswer}
		} else if a.Set == nil {
			asked = []string{AskWake}
		}
		a.Status, a.Answer, a.Why, a.AnsweredBy, a.AnsweredAt, a.ProofID, a.DeviceID = AskStatusAnswered, ev.Answer, ev.Why, ev.By, now, ev.ProofID, ev.DeviceID
		return a, "A3", Decision{Asked: asked, Why: "ask answered"}
	case a.Status == AskStatusOpen && ev.Kind == EvAskCancel:
		a.Status = AskStatusCancelled
		return a, "A4", Decision{Why: "ask cancelled"}
	}
	return a, "", Decision{}
}

// ValidateAsk checks KB-ASK-6, KB-ASK-9 (parentheses) and KB-ASK-15 (AQ3) and returns the cleaned Ask.
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
		named := taskID != "" && (a.Task == taskID || a.Kind == KindOverride && slices.ContainsFunc(a.Targets, func(t OverrideTarget) bool { return t.Task == taskID }))
		if !named || a.Status != AskStatusOpen || !a.Gated {
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
	lastRecap := LastRecapDecidedAt(t)
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

// AskOverrideResult is the done-once act prefix recorded in the first target
// task's Task.Acts when an override result line fires (design.md 4.3b, OF1).
const AskOverrideResult = "override_result"

// OverrideResultEvent is one owner result line ready to fire for an answered-Yes
// override card (design.md 4.3b OR1–OR6, OF1–OF5; 12.1).
type OverrideResultEvent struct {
	AskID string
	Task  string // first target task ID whose Task.Acts records override_result@<ask_id>
	Rule  string // OF2, OF3, or OF4
	Text  string
}

// OverrideResult derives pending owner result lines over stored state (design.md 4.3b, 12.1).
func OverrideResult(st State, now time.Time) []OverrideResultEvent {
	byID := make(map[string]Task, len(st.Tasks))
	for _, t := range st.Tasks {
		byID[t.ID] = t
	}
	grace := time.Duration(st.Settings.ChecksGraceMinutes) * time.Minute
	var out []OverrideResultEvent
	for _, a := range st.Asks {
		if a.Kind != KindOverride || a.Status != AskStatusAnswered || a.AnsweredBy != "owner" || a.Answer != OverrideOptionMerge || len(a.Targets) == 0 {
			continue
		}
		if first, ok := byID[a.Targets[0].Task]; !ok || Done(first.Acts, AskOverrideResult+"@"+a.ID) {
			continue
		}
		var merged, clauses []string
		waiting := map[string][]string{}
		for _, tgt := range a.Targets {
			t, exists := byID[tgt.Task]
			att, ov, open := overrideTargetAttempt(t, tgt.Head, a.ProofID)
			switch {
			case !exists:
				clauses = append(clauses, tgt.Task+" not merged: stopped")
			case t.Done != nil:
				merged = append(merged, tgt.Task)
			case ov != nil && ov.Spent:
				clauses = append(clauses, tgt.Task+" not merged: checks got worse after the card was made")
			case att != nil && open && overrideConflict(t, *att, tgt.Head, a.AnsweredAt):
				clauses = append(clauses, tgt.Task+" not merged: conflict with base")
			case att != nil && open && !Covers(*att, tgt.Head, H(*att)):
				clauses = append(clauses, tgt.Task+" not merged: newer commit")
			case att == nil || !open || Ended(t) || t.Parked || t.LastRow == "N4" || t.LastRow == "N5" || t.LastRow == "N7":
				clauses = append(clauses, tgt.Task+" not merged: stopped")
			default:
				notDone, dropped := UnmetDeps(t, st.Tasks)
				unmet := append(notDone, dropped...)
				waiting[tgt.Task] = unmet
				clauses = append(clauses, tgt.Task+map[bool]string{true: " waiting on " + strings.Join(unmet, ", "), false: " waiting"}[len(unmet) > 0])
			}
		}
		if rule := overrideResultRule(waiting, a.AnsweredAt, now, grace); rule != "" {
			if len(merged) > 0 {
				clauses = append([]string{strings.Join(merged, ", ") + " merged"}, clauses...)
			}
			out = append(out, OverrideResultEvent{AskID: a.ID, Task: a.Targets[0].Task, Rule: rule, Text: strings.Join(clauses, "; ")})
		}
	}
	return out
}

func overrideTargetAttempt(t Task, head, proofID string) (*Attempt, *Override, bool) {
	oa := OpenAttempt(t)
	for i := len(t.Attempts) - 1; i >= 0; i-- {
		for j := len(t.Attempts[i].Overrides) - 1; j >= 0; j-- {
			if o := &t.Attempts[i].Overrides[j]; o.Head == head && (proofID == "" || o.ProofID == proofID) {
				return &t.Attempts[i], o, oa != nil && i == len(t.Attempts)-1
			}
		}
	}
	return nil, nil, false
}

func overrideConflict(t Task, a Attempt, cardHead string, answeredAt time.Time) bool {
	if t.LastRow == "N16" && Covers(a, cardHead, H(a)) {
		return true
	}
	return slices.ContainsFunc(a.Acts, func(act Act) bool {
		h, ok := strings.CutPrefix(act.Key, AskTellConflict+"@")
		return ok && act.Result == "done" && !act.At.Before(answeredAt) && Covers(a, cardHead, h)
	})
}

func overrideResultRule(waiting map[string][]string, answeredAt, now time.Time, grace time.Duration) string {
	if len(waiting) == 0 {
		return "OF2"
	}
	allExt := true
	for _, unmet := range waiting {
		if len(unmet) == 0 || slices.ContainsFunc(unmet, func(dep string) bool { _, ok := waiting[dep]; return ok }) {
			allExt = false
			break
		}
	}
	switch {
	case allExt:
		return "OF3"
	case !answeredAt.IsZero() && now.Sub(answeredAt) >= grace:
		return "OF4"
	default:
		return ""
	}
}
