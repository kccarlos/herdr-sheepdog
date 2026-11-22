package factory

import (
	"errors"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// Change is one event on Engine.Changes() (design.md 12.3).
type Change struct {
	Project string `json:"project,omitempty"`
	Kind    string `json:"kind"`
	AskID   string `json:"ask_id,omitempty"`
	Task    string `json:"task,omitempty"`
	Seq     int64  `json:"seq,omitempty"`
}

// Asks is a test stub for PR 6.1 TDD step 1.
func (e *Engine) Asks(_ bool) []flow.Ask { return nil }

// Answer is a test stub for PR 6.1 TDD step 1.
func (e *Engine) Answer(_, _ string, _ AnswerProof) error {
	return errors.New("Answer not implemented")
}

// Say is a test stub for PR 6.1 TDD step 1.
func (e *Engine) Say(_, _ string) error {
	return errors.New("Say not implemented")
}

// Changes is a test stub for PR 6.1 TDD step 1.
func (e *Engine) Changes() <-chan Change {
	return make(chan Change)
}
