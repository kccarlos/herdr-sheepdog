package eval

// The fixed failure taxonomy (after pipecat's evals results): every
// failed or errored run is labelled with these, never free text, so runs
// can be counted. The detail beside each label is free text.

// FailureType is one label of the taxonomy.
type FailureType string

// The labels, most severe first (Primary picks the first that applies).
const (
	// FailError: the harness could not run the case properly (the
	// simulated user's or the judge's model failed, the orchestrator's provider
	// rate-limited us, a fixture is broken). The run counts as errored
	// and is left out of the pass rate.
	FailError FailureType = "error"
	// FailNotRun: the run never started (the spend budget ran out, the
	// campaign was cancelled). Never a pass; left out of the pass rate.
	FailNotRun FailureType = "not_run"
	// FailTimeout: an orchestrator turn ran out of time and the daemon answered
	// with its fallback sentence, or the conversation hit its time cap.
	FailTimeout FailureType = "timeout"
	// FailBrainFallback: an orchestrator turn failed for another reason (a model
	// error that is not a rate limit) and the daemon fell back.
	FailBrainFallback FailureType = "brain_fallback"
	// FailUnintendedAction: sheepdog did something to a worker the
	// scenario forbids (or anything, where nothing was allowed).
	FailUnintendedAction FailureType = "unintended_action"
	// FailWrongAgent: an action went to a worker other than the one the
	// user meant.
	FailWrongAgent FailureType = "wrong_agent"
	// FailMissingCall: an action the scenario requires never happened
	// (orchestrator: none of the expected tools was called).
	FailMissingCall FailureType = "missing_function_call"
	// FailJudgeNo: the judge said no on a rubric item.
	FailJudgeNo FailureType = "judge_no"
	// FailCheck: a code check on the reply failed (word or sentence
	// count, reply language, needs_user, urgency, the rail).
	FailCheck FailureType = "check"
	// FailSilence: the user waited in silence past the cap.
	FailSilence FailureType = "silence"
	// FailMaxTurns: the conversation used every turn without ending.
	FailMaxTurns FailureType = "max_turns"
)

// FailureOrder is the taxonomy in severity order.
var FailureOrder = []FailureType{FailError, FailNotRun, FailTimeout, FailBrainFallback, FailUnintendedAction, FailWrongAgent, FailMissingCall, FailJudgeNo, FailCheck, FailSilence, FailMaxTurns}

// Failure is one labelled failure of a run.
type Failure struct {
	Type   FailureType `json:"type"`
	Detail string      `json:"detail"`
}

// Primary is the most severe failure type of fs ("" for none).
func Primary(fs []Failure) FailureType {
	for _, t := range FailureOrder {
		for _, f := range fs {
			if f.Type == t {
				return t
			}
		}
	}
	return ""
}

// RunStatus is one run's outcome.
type RunStatus string

// The run statuses. A run is errored when the harness failed (FailError)
// and not_run when it never started; neither counts toward the pass rate,
// and neither is ever a pass.
const (
	RunPass   RunStatus = "pass"
	RunFail   RunStatus = "fail"
	RunError  RunStatus = "error"
	RunNotRun RunStatus = "not_run"
)

// statusOf derives a run's status from its failures.
func statusOf(fs []Failure) RunStatus {
	switch Primary(fs) {
	case "":
		return RunPass
	case FailError:
		return RunError
	case FailNotRun:
		return RunNotRun
	}
	return RunFail
}
