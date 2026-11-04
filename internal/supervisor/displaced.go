package supervisor

import (
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// The displacement rail (audit 2026-11, finding 1). The user hears
// question A and answers it, but in the seconds before the next
// announcement the voice's slot moved to question B, which they never
// heard: B replaced A (another pane blocked) or blocked just after A
// closed. The rail's other checks pass, since the words came after B
// appeared, so a bare "yes" meant for A went to B.
//
// The rule, in code and nowhere else (not in a prompt): a spoken or
// typed input does not authorise keys for the answer_prompt p when
//
//  1. p displaced another question: when p became the voice's pending
//     question (a block, a revival by send_keys, a held question
//     promoted, a question surfaced in a tool reply), the question the
//     user was last told about was a different one, and it was still
//     open or had closed within supervisor.authorise_window_s; and
//  2. the input started before p itself was told to the user (its
//     announcement sent to the voice model, EventPush, or the tool
//     reply that surfaced it), or p has not been told yet.
//
// "Different" and "told" go by pane and prompt (pendingSignature), not
// by id: a revival of the question the user heard is that question.
// "Started" is the input's first fragment, so a "yes" that began while A
// was the open question and ended after B blocked is still about A.
//
// Outside the rule, nothing changes: a question installed when the user
// had been told of no other one (the only question, or the first since
// the last closed longer than the window ago), and any answer that began
// after p was told. A tap names its question by id and is never
// refused here. With no voice session nothing is ever told, so the rule
// never applies. The bookkeeping is in memory: after a restart nothing
// counts as told. All of it is under s.mu.

// promptKey names a question by pane and prompt.
type promptKey struct{ pane, sig string }

func keyOf(p state.PendingAction) promptKey {
	return promptKey{pane: p.PaneID, sig: pendingSignature(p)}
}

// toldState is what the displacement rail knows.
type toldState struct {
	// last is the question the user was last told about; lastClosed is
	// when it left the open questions (zero while open, or not seen).
	last       promptKey
	lastClosed bool
	lastAt     time.Time
	// seq is, per question still open, s.inputSeq when it was first told.
	seq map[promptKey]uint64
	// displacing are the pending ids installed under rule 1.
	displacing map[string]bool
}

// maxTold bounds the told and displacing sets.
const maxTold = 32

// installedLocked records that p became the voice's pending question
// (rule 1). Call it before p is told. Caller holds s.mu.
func (s *Supervisor) installedLocked(p state.PendingAction) {
	if p.Kind != state.PendingAnswerPrompt {
		return
	}
	t := &s.told
	k := keyOf(p)
	if t.last == (promptKey{}) || t.last == k {
		return
	}
	// Normally already noted (closedLocked, toldGoneLocked); a question
	// replaced on its pane by another is gone as of now.
	s.toldGoneLocked(t.last.pane)
	if t.lastClosed && s.cfg.Now().Sub(t.lastAt) > s.cfg.AuthoriseWindow {
		return
	}
	if t.displacing == nil || len(t.displacing) >= maxTold {
		t.displacing = map[string]bool{}
	}
	t.displacing[p.ID] = true
}

// deliveredLocked records that the question with this id was told to
// the user now (rule 2). Caller holds s.mu.
func (s *Supervisor) deliveredLocked(id string) {
	p, ok := s.store.Pending()
	if !ok || p.ID != id {
		if p, ok = s.store.Question(id); !ok {
			return
		}
	}
	if p.Kind != state.PendingAnswerPrompt {
		return
	}
	t := &s.told
	k := keyOf(p)
	if t.seq == nil || len(t.seq) >= maxTold {
		t.seq = map[promptKey]uint64{}
	}
	if _, told := t.seq[k]; !told {
		t.seq[k] = s.inputSeq
	}
	t.last, t.lastClosed, t.lastAt = k, false, time.Time{}
}

// closedLocked records that p left the open questions. Caller holds s.mu.
func (s *Supervisor) closedLocked(p state.PendingAction) {
	t := &s.told
	k := keyOf(p)
	delete(t.seq, k)
	delete(t.displacing, p.ID)
	if t.last == k && !t.lastClosed {
		t.lastClosed, t.lastAt = true, s.cfg.Now()
	}
}

// toldGoneLocked notes, when the question last told was on pane and is no
// longer open, that it closed now. The store drops a pane's question by
// itself when the pane leaves blocked (HandleTransition calls this right
// after). Caller holds s.mu.
func (s *Supervisor) toldGoneLocked(pane string) {
	t := &s.told
	if t.last.pane != pane || t.lastClosed || s.keyOpenLocked(t.last) {
		return
	}
	t.lastClosed, t.lastAt = true, s.cfg.Now()
}

// keyOpenLocked reports whether a question with key k is open.
func (s *Supervisor) keyOpenLocked(k promptKey) bool {
	for _, q := range s.store.Questions() {
		if keyOf(q) == k {
			return true
		}
	}
	return false
}

// displacedLocked is the rule: the refusal reason when in may not answer
// p, "" otherwise. Caller holds s.mu.
func (s *Supervisor) displacedLocked(p state.PendingAction, in *shepherdInput) string {
	if p.Kind != state.PendingAnswerPrompt || in.kind == InputTap || !s.told.displacing[p.ID] {
		return ""
	}
	if seq, told := s.told.seq[keyOf(p)]; told && in.began > seq {
		return ""
	}
	return fmt.Sprintf("the user's words began before %s's prompt was told to them, while another question was the one they had heard", s.aliasOf(p.PaneID))
}
