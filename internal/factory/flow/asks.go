package flow

import "time"

// Ask statuses, kinds, events and decision verbs (design.md 7.1, 7.2).
const (
	AskStatusOpen      = "open"
	AskStatusAnswered  = "answered"
	AskStatusCancelled = "cancelled"

	KindDecision = "decision"
	KindGate     = "gate"
	KindRelease  = "release"
	KindPaidKey  = "paid-key"
	KindDelete   = "delete"
	KindInstall  = "install"
	KindReset    = "reset"
	KindRule     = "rule"

	EvAskOpen   = "ask_open"
	EvAskAnswer = "answer"
	EvAskCancel = "cancel"

	AskNotifyOwner    = "notify_owner"
	AskDeliverAnswer  = "deliver_answer"
	RefuseClosed      = "already_closed"
	MaxAskQuestionLen = 300
	MinAskOptions     = 2
	MaxAskOptions     = 9
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

// AskEvent is one input to the pure ask transition table A2–A5 (design.md 7.1).
type AskEvent struct {
	Kind     string
	Answer   string
	Why      string
	By       string
	ProofID  string
	DeviceID string
}

// StepAsk is a test stub for PR 6.1 TDD step 1 (design.md 7.1 A2–A5).
func StepAsk(a Ask, _ AskEvent, _ time.Time) (Ask, string, Decision) {
	return a, "", Decision{}
}

// ValidateAsk is a test stub for PR 6.1 TDD step 1 (KB-ASK-6, KB-ASK-9).
func ValidateAsk(_ Ask) (Ask, string, string) {
	return Ask{}, "", ""
}
