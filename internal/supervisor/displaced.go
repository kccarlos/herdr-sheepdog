package supervisor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The displacement rail (audit 2026-11, finding 1; reviewed three times).
// The user hears question A and answers it, but in the seconds before the
// next announcement the voice's slot moved to question B, which they never
// heard: B replaced A (another pane blocked, or A's pane asked something
// else) or blocked just after A closed. The rail's other checks pass,
// since the words came after B appeared, so a bare "yes" meant for A went
// to B.
//
// The rule, in code and nowhere else (not in a prompt): a spoken or typed
// input does not authorise keys for the answer_prompt p when
//
//  1. the input started before p was told to the user, or p has not been
//     told; and
//  2. some other question had been told to the user before the input
//     started, and it is still open or closed within
//     supervisor.authorise_window_s: the words may be about that one; and
//  3. the words do not name p's agent alone: words that name p's agent
//     and none of those other questions' agents are about p (bench
//     2026-11-04, "Yes, once, approve fix-claude's command"), unless p's
//     own pane had another prompt told before them (a name does not say
//     which of a pane's prompts was meant).
//
// An input that already answered a prompt is refused before this rule is
// read (railInputLocked): a second send of the same words to a re-shown
// prompt is refused without asking the user again, since their answer was
// delivered.
//
// It is read when the keys are checked, from what is open and what was
// told at that moment, not when p was installed: a question can be
// installed before the one the user hears is told (two blocks in quick
// succession, or a question kept across a daemon restart), and it must
// still count.
//
// "Told" is per question, by pane and prompt (pendingSignature), not by
// id: a revival of an open question the user heard is that question. A
// question is told when a line that states it reaches the user:
//   - a push about it (tagged pending_id: its announcement, a reminder, the
//     re-ask below) sent to the voice model (voice.EventPush), or shown in
//     the panel with no voice (--voice=off, PushShown), unless the voice
//     layer then judges that line unheard, not voiced, or cut before that
//     question was stated (the telling is taken back);
//   - an orchestrator reply delivered to the user that states it in its
//     agent's clause with ask framing and the prompt's own content
//     (toldInReplyFor, and a late reply when its push goes out), unless a
//     voice tool call's delivery is cancelled or its playback is cut short
//     before stating that question. A held question (muted space) a reply
//     states is promoted only when the reply reads its spoken summary out.
//     A question a reply states while it is not open, its pane still at
//     the prompt, is told when it is installed again within the window
//     (send_keys revives it).
//
// A tool result that goes to the orchestrator tells nothing: the user has
// not heard it. When a question closes (answered, its pane left the
// prompt, replaced on its pane) its telling is kept for the window, then
// forgotten; the same prompt asked again later is a new question.
// "Started" is the input's first fragment, so a "yes" that began while A
// was the open question and ended after B blocked is still about A.
//
// When the rule refuses, the user hears why and is asked again, once per
// input (reaskLocked): the fast path answers with that line, a refused
// voice send_keys pushes it, and a refused typed turn (TypedTurn) returns
// it synchronously in the typed reply. That line tells p, so the user's
// next answer goes through. Refusing a yes the user meant for the only
// question they heard is a failure too: that is why the rule is about what
// was told, not about what was installed.
//
// Outside the rule nothing changes: a question when nothing else told is
// live (the only question, or the first since the last closed longer than
// the window ago), and any answer that began after p was told. A tap names
// its question by id and is never refused here. With no voice session
// nothing is ever told by a push. Told questions (open or within the
// window) are persisted in session.told_prompts so a restart keeps
// displacement protection intact. All of it is under s.mu.

// promptKey names a question by pane and prompt.
type promptKey struct{ pane, sig string }

func keyOf(p state.PendingAction) promptKey {
	return promptKey{pane: p.PaneID, sig: pendingSignature(p)}
}

// toldEntry is what the rail knows of a question that was told.
type toldEntry struct {
	// seq is s.inputSeq when it was first told: an input with a greater
	// began started after.
	seq uint64
	// order breaks ties deterministically when multiple questions are told
	// without an intervening user input (same seq).
	order uint64
	// line is the push that told it ("" for a reply returned as a tool
	// result or typed reply): that line judged unheard or cut takes the
	// telling back.
	line string
	// uttID is the voice utterance whose delivery told it ("" for a daemon
	// push with no utterance or a synchronous typed delivery): a cancelled
	// tool response or cut playback for that utterance takes the telling
	// back unless the heard prefix already stated it.
	uttID string
	// closedAt is when the question left the open questions; zero while
	// open.
	closedAt time.Time
}

// toldState is what the displacement rail knows.
type toldState struct {
	// told are the questions told, open or closed within the window.
	told      map[promptKey]*toldEntry
	nextOrder uint64
	// lines are the spoken lines handed to the voice that state questions
	// still open: the questions each tells when it goes out.
	lines map[string][]promptKey
	// lineUtt maps a pushed reply line to the utterance that produced it.
	lineUtt map[string]string
	// callUtt maps recent voice tool call IDs to utterance IDs, so a
	// cancelled EventToolResult without Result.UtteranceID revokes any
	// inline telling from that call.
	callUtt   map[string]string
	callOrder []string
	// pushRun is the push line currently being spoken by the voice; cutPush
	// holds it across EventInterrupted until the matching cut transcript.
	pushRun string
	cutPush string
	// reasked is, per question, the input whose refusal was last said to
	// the user.
	reasked map[promptKey]uint64
	// asked is, per pane, the newest question installed for it: what a
	// reply about that pane states while its question is not open (left
	// the list on a screen read that did not parse, say).
	asked map[string]state.PendingAction
	// stated are the questions a reply stated to the user while they were
	// not open, with the pane still at its prompt: installed again within
	// the window (send_keys revives it), the question was told.
	stated map[promptKey]statedEntry
	// persisted is the last Session.ToldPrompts snapshot written to store.
	persisted []state.ToldPrompt
}

// statedEntry is a reply's telling of a question that was not open.
type statedEntry struct {
	seq   uint64
	at    time.Time
	uttID string
}

const maxTrackedCallUtts = 64

// restoreTold loads persisted told_prompts at Supervisor startup, assigning
// monotonic sequence numbers below any post-restart input so a bare answer
// after restart still knows which prompts had already been told.
func (s *Supervisor) restoreTold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := s.store.Snapshot().Session.ToldPrompts
	if len(saved) == 0 {
		return
	}
	now := s.cfg.Now()
	s.told.persisted = append([]state.ToldPrompt(nil), saved...)
	for _, tp := range saved {
		if tp.PaneID == "" || tp.Sig == "" {
			continue
		}
		if !tp.ClosedAt.IsZero() && now.Sub(tp.ClosedAt) > s.cfg.AuthoriseWindow {
			continue
		}
		s.inputSeq++
		s.told.nextOrder++
		if s.told.told == nil {
			s.told.told = map[promptKey]*toldEntry{}
		}
		s.told.told[promptKey{pane: tp.PaneID, sig: tp.Sig}] = &toldEntry{
			seq:      s.inputSeq,
			order:    s.told.nextOrder,
			closedAt: tp.ClosedAt,
		}
	}
	s.relayedSeq = s.inputSeq
	s.pushSeq = s.inputSeq
	s.syncToldLocked()
}

// persistToldLocked writes open and within-window told questions to
// Session.ToldPrompts in sequence order when they changed. Caller holds s.mu.
func (s *Supervisor) persistToldLocked() {
	type item struct {
		seq   uint64
		order uint64
		tp    state.ToldPrompt
	}
	var items []item
	for k, e := range s.told.told {
		if e == nil {
			continue
		}
		items = append(items, item{
			seq:   e.seq,
			order: e.order,
			tp:    state.ToldPrompt{PaneID: k.pane, Sig: k.sig, ClosedAt: e.closedAt},
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].seq != items[j].seq {
			return items[i].seq < items[j].seq
		}
		if items[i].order != items[j].order {
			return items[i].order < items[j].order
		}
		if items[i].tp.PaneID != items[j].tp.PaneID {
			return items[i].tp.PaneID < items[j].tp.PaneID
		}
		return items[i].tp.Sig < items[j].tp.Sig
	})
	var next []state.ToldPrompt
	if len(items) > 0 {
		next = make([]state.ToldPrompt, len(items))
		for i, it := range items {
			next[i] = it.tp
		}
	}
	if sameToldPrompts(s.told.persisted, next) {
		return
	}
	s.told.persisted = next
	cp := append([]state.ToldPrompt(nil), next...)
	if err := s.store.UpdateSession(func(ss *state.Session) { ss.ToldPrompts = cp }); err != nil {
		s.log.Log(logSource, "supervisor.state_error", "", map[string]any{"error": err.Error()})
	}
}

func sameToldPrompts(a, b []state.ToldPrompt) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].PaneID != b[i].PaneID || a[i].Sig != b[i].Sig || !a[i].ClosedAt.Equal(b[i].ClosedAt) {
			return false
		}
	}
	return true
}

// resetToldLocked drops closed and unopen stated tellings on conversation
// reset and persists the remaining open tellings. Caller holds s.mu.
func (s *Supervisor) resetToldLocked() {
	s.syncToldLocked()
	for k, e := range s.told.told {
		if !e.closedAt.IsZero() {
			delete(s.told.told, k)
		}
	}
	clear(s.told.stated)
	s.told.pushRun = ""
	s.told.cutPush = ""
	s.persistToldLocked()
}

// noteCallUtteranceLocked records that voice tool callID ran as utterance
// uttID. Caller holds s.mu.
func (s *Supervisor) noteCallUtteranceLocked(callID, uttID string) {
	if callID == "" || uttID == "" {
		return
	}
	t := &s.told
	if t.callUtt == nil {
		t.callUtt = map[string]string{}
	}
	if _, exists := t.callUtt[callID]; !exists {
		t.callOrder = append(t.callOrder, callID)
		if len(t.callOrder) > maxTrackedCallUtts {
			old := t.callOrder[0]
			t.callOrder = t.callOrder[1:]
			delete(t.callUtt, old)
		}
	}
	t.callUtt[callID] = uttID
}

// openKeysLocked are the questions open now. Caller holds s.mu.
func (s *Supervisor) openKeysLocked() map[promptKey]state.PendingAction {
	open := map[promptKey]state.PendingAction{}
	for _, q := range s.store.Questions() {
		if q.Kind == state.PendingAnswerPrompt {
			open[keyOf(q)] = q
		}
	}
	if p, ok := s.store.Pending(); ok && p.Kind == state.PendingAnswerPrompt {
		open[keyOf(p)] = p
	}
	return open
}

// syncToldLocked brings the bookkeeping up to date with the open
// questions: a told question no longer open closed now (the store drops a
// pane's question by itself when it leaves blocked, and a pane's question
// replaced by another is gone); one closed longer than the window ago is
// forgotten. Nothing is cleared wholesale. Caller holds s.mu.
func (s *Supervisor) syncToldLocked() {
	t := &s.told
	open := s.openKeysLocked()
	now := s.cfg.Now()
	for k, e := range t.told {
		if _, ok := open[k]; !ok && e.closedAt.IsZero() {
			e.closedAt = now
		}
		if !e.closedAt.IsZero() && now.Sub(e.closedAt) > s.cfg.AuthoriseWindow {
			delete(t.told, k)
		}
	}
	for text, keys := range t.lines {
		live := keys[:0:0]
		for _, k := range keys {
			if _, ok := open[k]; ok {
				live = append(live, k)
			}
		}
		if len(live) == 0 {
			delete(t.lines, text)
			delete(t.lineUtt, text)
		} else {
			t.lines[text] = live
		}
	}
	for k := range t.reasked {
		if _, ok := open[k]; !ok {
			delete(t.reasked, k)
		}
	}
	for k, e := range t.stated {
		if now.Sub(e.at) > s.cfg.AuthoriseWindow {
			delete(t.stated, k)
		}
	}
	s.persistToldLocked()
}

// installedLocked records that p became an open question: the voice's
// pending one (a block, a revival by send_keys, a held question promoted)
// or a held one. A closed question with p's pane and prompt is not p: p is
// new, and not told, unless a reply stated p's pane and prompt to the user
// within the window while it was not open (the user asked about it, the
// orchestrator said what it asks, then send_keys revived it). Call it
// before p is stored. Caller holds s.mu.
func (s *Supervisor) installedLocked(p state.PendingAction) {
	if p.Kind != state.PendingAnswerPrompt {
		return
	}
	s.syncToldLocked()
	t := &s.told
	k := keyOf(p)
	if e := t.told[k]; e != nil && !e.closedAt.IsZero() {
		delete(t.told, k)
	}
	if t.asked == nil {
		t.asked = map[string]state.PendingAction{}
	}
	t.asked[p.PaneID] = p
	if st, ok := t.stated[k]; ok {
		delete(t.stated, k)
		if t.told[k] == nil {
			if t.told == nil {
				t.told = map[promptKey]*toldEntry{}
			}
			t.nextOrder++
			t.told[k] = &toldEntry{seq: st.seq, order: t.nextOrder, uttID: st.uttID}
		}
	}
	s.persistToldLocked()
}

// closedLocked records that p left the open questions (cleared from the
// slot, dropped from the list): unless its pane and prompt are still open
// under another id, it closed now. Caller holds s.mu.
func (s *Supervisor) closedLocked() {
	s.syncToldLocked()
}

// untellLocked takes back the telling of the open question k, now held
// (its space was muted, holdSpaceQuestions): it no longer counts as told
// to p's answer, but as a question the user heard it still counts for
// the window, like a closed one, against an answer to another. No line
// tells it any more. Caller holds s.mu.
func (s *Supervisor) untellLocked(k promptKey) {
	t := &s.told
	if e := t.told[k]; e != nil && e.closedAt.IsZero() {
		e.closedAt = s.cfg.Now()
	}
	for text, keys := range t.lines {
		live := keys[:0:0]
		for _, k2 := range keys {
			if k2 != k {
				live = append(live, k2)
			}
		}
		if len(live) == 0 {
			delete(t.lines, text)
			delete(t.lineUtt, text)
		} else {
			t.lines[text] = live
		}
	}
	delete(t.stated, k)
	s.persistToldLocked()
}

// toldKeyLocked records that the open question k was told to the user
// now, by line ("" for a reply returned synchronously). Caller holds s.mu.
func (s *Supervisor) toldKeyLocked(k promptKey, line string) {
	s.toldKeyForLocked(k, line, "")
}

// toldKeyForLocked is toldKeyLocked tagged with the voice utterance uttID
// whose delivery stated k ("" when non-voice/typed or a daemon push).
// Caller holds s.mu.
func (s *Supervisor) toldKeyForLocked(k promptKey, line, uttID string) {
	t := &s.told
	if e := t.told[k]; e != nil && e.closedAt.IsZero() {
		return
	}
	if t.told == nil {
		t.told = map[promptKey]*toldEntry{}
	}
	t.nextOrder++
	t.told[k] = &toldEntry{seq: s.inputSeq, order: t.nextOrder, line: line, uttID: uttID}
	s.persistToldLocked()
}

// promptLineLocked records text as a spoken line that states the open
// question with this id, whether or not it is the voice's question now.
// Caller holds s.mu.
func (s *Supervisor) promptLineLocked(text, id string) {
	s.promptLineForLocked(text, id, "")
}

func (s *Supervisor) promptLineForLocked(text, id, uttID string) {
	q, ok := s.openByIDLocked(id)
	if !ok || text == "" {
		return
	}
	s.addLineLocked(text, []promptKey{keyOf(q)})
	if uttID != "" {
		if s.told.lineUtt == nil {
			s.told.lineUtt = map[string]string{}
		}
		s.told.lineUtt[text] = uttID
	}
}

func (s *Supervisor) addLineLocked(text string, keys []promptKey) {
	if len(keys) == 0 {
		return
	}
	s.syncToldLocked()
	t := &s.told
	if t.lines == nil {
		t.lines = map[string][]promptKey{}
	}
	for _, k := range keys {
		dup := false
		for _, have := range t.lines[text] {
			dup = dup || have == k
		}
		if !dup {
			t.lines[text] = append(t.lines[text], k)
		}
	}
}

// openByIDLocked is the open answer_prompt with this id. Caller holds
// s.mu.
func (s *Supervisor) openByIDLocked(id string) (state.PendingAction, bool) {
	if id == "" {
		return state.PendingAction{}, false
	}
	if p, ok := s.store.Pending(); ok && p.ID == id && p.Kind == state.PendingAnswerPrompt {
		return p, true
	}
	if q, ok := s.store.Question(id); ok && q.Kind == state.PendingAnswerPrompt {
		return q, true
	}
	return state.PendingAction{}, false
}

// linePushedLocked records that the spoken line text went to the voice now
// (EventPush): the questions it states, and the one pendingID names, were
// told. A held question (muted space) is told, and promoted, only by a
// push about it (pendingID: its announcement); lines never list one
// (replyLineForLocked). Caller holds s.mu.
func (s *Supervisor) linePushedLocked(text, pendingID string) {
	if text != "" {
		s.told.pushRun = text
		s.told.cutPush = ""
	}
	s.markLineToldLocked(text, pendingID)
}

func (s *Supervisor) markLineToldLocked(text, pendingID string) {
	s.syncToldLocked()
	open := s.openKeysLocked()
	keys := append([]promptKey(nil), s.told.lines[text]...)
	var named promptKey
	if q, ok := s.openByIDLocked(pendingID); ok {
		named = keyOf(q)
		keys = append(keys, named)
	}
	uttID := s.told.lineUtt[text]
	for _, k := range keys {
		q, ok := open[k]
		if !ok {
			continue
		}
		if q.Held || s.heldQuestion(q.ID) {
			if k != named {
				continue
			}
			// A held question is only pushed when it is announced
			// (promoteHeld runs first); one pushed anyway was heard.
			q = s.promoteHeldLocked(q)
		}
		s.toldKeyForLocked(keyOf(q), text, uttID)
	}
}

// lineUnheardLocked takes back the telling of the questions text told,
// now that the voice layer judged it unheard or not voiced. Caller holds
// s.mu.
func (s *Supervisor) lineUnheardLocked(text string) {
	if text == "" {
		return
	}
	changed := false
	for k, e := range s.told.told {
		if e != nil && e.closedAt.IsZero() && e.line == text {
			delete(s.told.told, k)
			changed = true
		}
	}
	if changed {
		s.persistToldLocked()
	}
}

// notePushTurnDoneLocked clears the active push-run marker when a voice
// turn ends without interruption. Caller holds s.mu.
func (s *Supervisor) notePushTurnDoneLocked() {
	s.told.pushRun = ""
	s.told.cutPush = ""
}

// notePushInterruptedLocked preserves an interrupted push line so the
// following cut transcript can revoke questions not yet heard. Caller
// holds s.mu.
func (s *Supervisor) notePushInterruptedLocked() {
	if s.told.pushRun != "" {
		s.told.cutPush = s.told.pushRun
		s.told.pushRun = ""
	}
}

// onToolResult revokes any inline voice telling recorded for a tool call
// whose response was cancelled or errored before reaching playback.
func (s *Supervisor) onToolResult(e voice.EventToolResult) {
	if !e.Cancelled && e.Err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	uttID := e.Result.UtteranceID
	if uttID == "" && e.Call.ID != "" {
		uttID = s.told.callUtt[e.Call.ID]
	}
	if uttID == "" {
		return
	}
	changed := false
	for k, ent := range s.told.told {
		if ent != nil && ent.closedAt.IsZero() && ent.uttID == uttID {
			delete(s.told.told, k)
			changed = true
		}
	}
	for k, st := range s.told.stated {
		if st.uttID == uttID {
			delete(s.told.stated, k)
		}
	}
	for line, uid := range s.told.lineUtt {
		if uid == uttID {
			delete(s.told.lineUtt, line)
		}
	}
	if changed {
		s.persistToldLocked()
	}
}

// onToldCut revokes tellings from a reply or push whose client playback
// was cut short before stating the question. Questions already stated in
// heard remain told.
func (s *Supervisor) onToldCut(cut *voice.TranscriptCut, heard string) {
	if cut == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncToldLocked()
	open := s.openKeysLocked()
	changed := false

	if cut.UtteranceID != "" {
		for k, ent := range s.told.told {
			if ent == nil || !ent.closedAt.IsZero() || ent.uttID != cut.UtteranceID {
				continue
			}
			q, ok := open[k]
			if ok && s.statesLocked(heard, q) && (!s.paneMutedCached(q.PaneID) || readsOut(heard, q)) {
				continue
			}
			delete(s.told.told, k)
			changed = true
		}
		for k, st := range s.told.stated {
			if st.uttID != cut.UtteranceID {
				continue
			}
			q, ok := s.told.asked[k.pane]
			if ok && keyOf(q) == k && s.statesLocked(heard, q) {
				continue
			}
			delete(s.told.stated, k)
		}
		if changed {
			s.persistToldLocked()
		}
		return
	}

	activePush := s.told.cutPush
	s.told.cutPush = ""
	if activePush == "" && s.told.pushRun != "" {
		activePush = s.told.pushRun
		s.told.pushRun = ""
	}
	for k, ent := range s.told.told {
		if ent == nil || !ent.closedAt.IsZero() || ent.line == "" {
			continue
		}
		q, ok := open[k]
		if !ok {
			continue
		}
		inCutRun := ent.line == activePush ||
			(ent.line == s.lastPush && (cut.Full == ent.line || readsOut(cut.Full, q) || s.statesLocked(cut.Full, q)))
		if !inCutRun {
			continue
		}
		if readsOut(heard, q) || s.statesLocked(heard, q) {
			continue
		}
		delete(s.told.told, k)
		changed = true
	}
	if changed {
		s.persistToldLocked()
	}
}

// PushShown records a spoken line shown to the user with no voice model
// (--voice=off: the panel shows skipped pushes): it tells what it states.
func (s *Supervisor) PushShown(text string, tags map[string]any) {
	id, _ := tags[tagPendingID].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markLineToldLocked(text, id)
}

// toldInReply records an orchestrator reply delivered synchronously (e.g.
// a typed turn's text): the open questions it states were told.
func (s *Supervisor) toldInReply(text string) {
	s.toldInReplyFor(text, "")
}

// toldInReplyFor records an orchestrator reply delivered to the user now,
// tagged with uttID when spoken via a voice tool result ("" for typed
// replies). A held question is promoted only when the reply reads its
// whole spoken summary. A question it states that is not open (its pane
// still at the prompt) is told if it is installed again within the window
// (installedLocked).
func (s *Supervisor) toldInReplyFor(text, uttID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncToldLocked()
	for _, q := range s.statedInLocked(text) {
		if q.Held || s.heldQuestion(q.ID) {
			if !readsOut(text, q) {
				continue
			}
			q = s.promoteHeldLocked(q)
		} else if readsOut(text, q) && s.paneMutedCached(q.PaneID) {
			q = s.promoteHeldLocked(q)
		}
		s.toldKeyForLocked(keyOf(q), "", uttID)
	}
	for _, q := range s.statedNotOpenLocked(text) {
		if s.told.stated == nil {
			s.told.stated = map[promptKey]statedEntry{}
		}
		s.told.stated[keyOf(q)] = statedEntry{seq: s.inputSeq, at: s.cfg.Now(), uttID: uttID}
	}
}

// readsOut reports whether text carries q's whole spoken summary: the
// question read out, not mentioned in passing.
func readsOut(text string, q state.PendingAction) bool {
	sum := strings.ToLower(strings.TrimRight(strings.TrimSpace(q.SpokenSummary), ".。!?！？ "))
	return sum != "" && strings.Contains(strings.ToLower(text), sum)
}

// replyLineForLocked records text, a late reply about to be pushed for
// uttID, as a line that tells the open questions it states. A held question
// is left out: a late reply does not promote it. Caller holds s.mu.
func (s *Supervisor) replyLineForLocked(text, uttID string) {
	var keys []promptKey
	for _, q := range s.statedInLocked(text) {
		if q.Held || s.heldQuestion(q.ID) {
			continue
		}
		keys = append(keys, keyOf(q))
	}
	s.addLineLocked(text, keys)
	if len(keys) > 0 && uttID != "" {
		if s.told.lineUtt == nil {
			s.told.lineUtt = map[string]string{}
		}
		s.told.lineUtt[text] = uttID
	}
}

// promptStockWords are words of a prompt too common to say which prompt
// a reply states.
var promptStockWords = map[string]bool{
	"asks": true, "ask": true, "asking": true, "wants": true, "wanting": true, "permission": true,
	"approve": true, "approval": true, "allow": true, "accept": true, "confirm": true, "confirmation": true,
	"run": true, "running": true, "following": true, "command": true, "proceed": true, "should": true,
	"shall": true, "could": true, "may": true, "let": true, "waiting": true, "question": true, "prompt": true,
	"yes": true, "don't": true, "dont": true, "file": true, "path": true, "link": true, "again": true,
	"commands": true, "start": true, "who": true, "how": true, "will": true, "not": true, "are": true,
	"have": true, "decision": true, "need": true, "needs": true, "whether": true, "choose": true,
	"choice": true, "option": true, "options": true, "select": true, "pick": true,
}

// cmdDriverWords are multi-subcommand CLI drivers whose bare binary name
// ("git", "npm", "docker") appears constantly in ordinary status prose
// without identifying a specific command approval.
var cmdDriverWords = map[string]bool{
	"git": true, "npm": true, "pnpm": true, "yarn": true, "npx": true, "bun": true,
	"cargo": true, "docker": true, "kubectl": true, "helm": true, "go": true,
	"python": true, "python3": true, "pip": true, "pip3": true, "uv": true,
	"brew": true, "apt": true, "gh": true, "gcloud": true, "aws": true,
	"terraform": true, "make": true, "bazel": true, "blaze": true, "gradle": true,
	"mvn": true, "dotnet": true, "bundle": true, "rake": true, "mix": true,
	"sh": true, "bash": true, "zsh": true, "env": true, "sudo": true, "nohup": true, "xargs": true,
}

var cmdProseFiller = map[string]bool{
	"to": true, "in": true, "on": true, "it": true, "is": true, "of": true,
	"at": true, "by": true, "or": true, "an": true, "as": true, "be": true,
}

var askCueWords = map[string]bool{
	"ask": true, "asks": true, "asking": true,
	"want": true, "wants": true, "wanting": true,
	"need": true, "needs": true,
	"permission": true, "approve": true, "approval": true, "allow": true,
	"confirm": true, "confirmation": true, "proceed": true,
	"should": true, "shall": true, "would": true, "may": true, "whether": true,
	"choose": true, "choice": true, "pick": true, "select": true, "option": true, "options": true,
	"accept": true, "decline": true, "deny": true, "reject": true, "trust": true,
}

var askCuePhrases = []string{
	"go ahead", "waiting on you", "waiting for your", "waiting for approval", "up to you",
}

var zhAskCues = []string{
	"请求", "询问", "在问", "问你", "想要", "想运行", "想执行", "想创建", "想修改", "想删除",
	"允许", "批准", "确认", "同意", "是否", "要不要", "可不可以", "能不能", "好吗", "可以吗", "行吗",
	"怎么", "选择", "选项", "做个决定", "等你", "吗",
}

var genericOptionWords = map[string]bool{
	"yes": true, "no": true, "ok": true, "okay": true, "proceed": true, "continue": true,
	"cancel": true, "quit": true, "exit": true, "accept": true, "reject": true, "decline": true,
	"deny": true, "allow": true, "approve": true, "confirm": true, "change": true, "changes": true,
	"edits": true, "edit": true, "session": true, "conversation": true, "always": true,
	"persist": true, "settings": true, "json": true, "common": true, "auto": true, "switch": true,
	"differently": true, "codex": true, "claude": true, "tell": true, "ask": true, "again": true,
	"commands": true, "command": true, "start": true, "starts": true, "run": true, "runs": true,
	"esc": true, "enter": true, "shift": true, "tab": true, "option": true, "recommended": true,
	"推荐": true,
}

var trustLocationWords = map[string]bool{
	"folder": true, "directory": true, "workspace": true, "project": true, "contents": true,
}

// statedInLocked are the open questions text states: in the clauses
// attributed to the question's agent, text carries ask/decision framing
// and states that prompt's own content. Caller holds s.mu.
func (s *Supervisor) statedInLocked(text string) []state.PendingAction {
	var out []state.PendingAction
	for _, q := range s.openKeysLocked() {
		if s.statesLocked(text, q) {
			out = append(out, q)
		}
	}
	return out
}

// statedNotOpenLocked are the questions text states that are not open:
// the newest question of a pane with no open question, not held, whose
// pane is still not working (at its prompt as far as herdr says). Caller
// holds s.mu.
func (s *Supervisor) statedNotOpenLocked(text string) []state.PendingAction {
	openPanes := map[string]bool{}
	for k := range s.openKeysLocked() {
		openPanes[k.pane] = true
	}
	var out []state.PendingAction
	for pane, q := range s.told.asked {
		row, ok := s.store.Get(pane)
		if !ok {
			delete(s.told.asked, pane)
			continue
		}
		if openPanes[pane] || q.Held || row.State == herdr.StateWorking || s.paneMutedCached(pane) {
			continue
		}
		if s.statesLocked(text, q) {
			out = append(out, q)
		}
	}
	return out
}

// statesLocked reports whether text (a reply's spoken text) states q: in
// the clause(s) attributed to q's agent, it frames a question/decision and
// states q's specific command, question, or distinct options. Caller holds
// s.mu.
func (s *Supervisor) statesLocked(text string, q state.PendingAction) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	row, ok := s.store.Get(q.PaneID)
	if !ok {
		return false
	}
	agentText, names := s.agentClausesLocked(text, row)
	if agentText == "" {
		return false
	}
	if readsOut(agentText, q) {
		return true
	}
	agentWords := wordSet(nameTokens(agentText))
	optMatched := matchedDistinctOptions(agentWords, q.Options, names)
	if !hasAskCue(agentText, agentWords) && optMatched < 2 {
		return false
	}
	return matchesPromptContent(agentText, agentWords, q, names, optMatched)
}

type paneNames struct {
	pane    string
	aliases []string
	tokSets [][]string
	allToks map[string]bool
}

func (s *Supervisor) buildPaneNamesLocked(target state.Agent) []paneNames {
	rows := s.store.List()
	foundTarget := false
	for _, r := range rows {
		if r.PaneID == target.PaneID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		rows = append(rows, target)
	}
	out := make([]paneNames, 0, len(rows))
	for _, r := range rows {
		pn := paneNames{pane: r.PaneID, allToks: map[string]bool{}}
		for _, raw := range []string{r.Alias, state.Say(r), s.sayOf(r.PaneID)} {
			n := strings.ToLower(strings.TrimSpace(raw))
			if n == "" {
				continue
			}
			pn.aliases = append(pn.aliases, n)
			nw := nameTokens(n)
			for _, w := range nw {
				pn.allToks[w] = true
			}
			long := false
			for _, w := range nw {
				long = long || len([]rune(w)) >= 2 && !shortNameStopWords[w]
			}
			if long && len(nw) > 0 {
				pn.tokSets = append(pn.tokSets, nw)
			}
		}
		out = append(out, pn)
	}
	return out
}

// agentClausesLocked returns the joined clauses of text attributed to
// target's pane (ignoring clauses attributed to other agents) and target's
// own name tokens. Caller holds s.mu.
func (s *Supervisor) agentClausesLocked(text string, target state.Agent) (string, map[string]bool) {
	panes := s.buildPaneNamesLocked(target)
	var targetNames map[string]bool
	for _, pn := range panes {
		if pn.pane == target.PaneID {
			targetNames = pn.allToks
			break
		}
	}
	clauses := splitReplyClauses(text, panes)
	var leading []string
	var attributed []string
	active := map[string]bool{}
	seenNamed := false
	namedTarget := false

	for _, cl := range clauses {
		named := panesNamedInClause(cl, panes)
		if len(named) > 0 {
			if !seenNamed {
				seenNamed = true
				if named[target.PaneID] {
					attributed = append(attributed, leading...)
				}
				leading = nil
			}
			active = named
			if named[target.PaneID] {
				namedTarget = true
			}
		} else if !seenNamed {
			leading = append(leading, cl)
			continue
		}
		if active[target.PaneID] {
			attributed = append(attributed, cl)
		}
	}
	if !namedTarget {
		return "", targetNames
	}
	return strings.Join(attributed, " "), targetNames
}

func splitReplyClauses(text string, panes []paneNames) []string {
	runes := []rune(text)
	var coarse []string
	var cur []rune
	flush := func() {
		if s := strings.TrimSpace(string(cur)); s != "" {
			coarse = append(coarse, s)
		}
		cur = nil
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\n', ';', '；', ',', '，', '!', '！', '。':
			cur = append(cur, r)
			flush()
		case '?', '？':
			cur = append(cur, r)
			flush()
		case '.':
			boundary := i+1 == len(runes) || unicode.IsSpace(runes[i+1]) ||
				runes[i+1] == '"' || runes[i+1] == '\'' || runes[i+1] == ')' || runes[i+1] == '”' || runes[i+1] == '’'
			cur = append(cur, r)
			if boundary {
				flush()
			}
		default:
			cur = append(cur, r)
		}
	}
	flush()

	var out []string
	for _, cl := range coarse {
		out = append(out, splitMultiAgentConjunction(cl, panes)...)
	}
	return out
}

// splitMultiAgentConjunction splits a comma-less clause like
// "Docs finished updating git documentation and auth asks to run git push"
// at a coordinating conjunction when the left side names an agent followed
// by its own predicate (>=2 tokens) and the right side names another agent.
func splitMultiAgentConjunction(clause string, panes []paneNames) []string {
	lower := strings.ToLower(clause)
	for _, conj := range []string{" and ", " but ", " while ", " whereas "} {
		idx := strings.Index(lower, conj)
		if idx <= 0 {
			continue
		}
		left := clause[:idx]
		right := clause[idx+len(conj):]
		leftNamed := panesNamedInClause(left, panes)
		rightNamed := panesNamedInClause(right, panes)
		if len(leftNamed) == 0 || len(rightNamed) == 0 || samePaneSet(leftNamed, rightNamed) {
			continue
		}
		if nonNameTokenCount(left, leftNamed, panes) < 2 {
			continue
		}
		res := []string{left}
		res = append(res, splitMultiAgentConjunction(right, panes)...)
		return res
	}
	return []string{clause}
}

func samePaneSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func nonNameTokenCount(clause string, named map[string]bool, panes []paneNames) int {
	nameToks := map[string]bool{}
	for _, pn := range panes {
		if named[pn.pane] {
			for t := range pn.allToks {
				nameToks[t] = true
			}
		}
	}
	n := 0
	for _, w := range nameTokens(clause) {
		if w != "both" && !nameToks[w] {
			n++
		}
	}
	return n
}

func panesNamedInClause(clause string, panes []paneNames) map[string]bool {
	lower := strings.ToLower(clause)
	toks := nameTokens(clause)
	words := wordSet(toks)
	matched := map[string][]string{}

	for _, pn := range panes {
		for _, alias := range pn.aliases {
			if len([]rune(alias)) >= 2 && isHan(alias) && strings.Contains(lower, alias) {
				matched[pn.pane] = []string{alias}
				break
			}
		}
		for _, ts := range pn.tokSets {
			if containsWords(words, ts) && len(ts) > len(matched[pn.pane]) {
				matched[pn.pane] = ts
			}
		}
	}
	if len(matched) <= 1 {
		out := make(map[string]bool, len(matched))
		for p := range matched {
			out[p] = true
		}
		return out
	}
	// Drop a shorter alias (e.g. "auth") when its tokens only appear inside
	// a longer alias's contiguous span (e.g. "auth-v2").
	out := make(map[string]bool, len(matched))
	for pA, tsA := range matched {
		subsumed := false
		for pB, tsB := range matched {
			if pA == pB || len(tsB) <= len(tsA) || !containsSlice(tsB, tsA) {
				continue
			}
			if countSeq(toks, tsA) <= countSeq(toks, tsB) {
				subsumed = true
				break
			}
		}
		if !subsumed {
			out[pA] = true
		}
	}
	return out
}

func containsSlice(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
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

func countSeq(toks, seq []string) int {
	if len(seq) == 0 || len(seq) > len(toks) {
		return 0
	}
	n := 0
	for i := 0; i+len(seq) <= len(toks); i++ {
		ok := true
		for j := range seq {
			if toks[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			n++
		}
	}
	return n
}

func hasAskCue(agentText string, agentWords map[string]bool) bool {
	if strings.ContainsAny(agentText, "?？") {
		return true
	}
	for w := range agentWords {
		if askCueWords[w] {
			return true
		}
	}
	lower := strings.ToLower(agentText)
	for _, p := range askCuePhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	for _, cue := range zhAskCues {
		if strings.Contains(agentText, cue) {
			return true
		}
	}
	return false
}

func matchesPromptContent(agentText string, agentWords map[string]bool, q state.PendingAction, names map[string]bool, optMatched int) bool {
	if strings.TrimSpace(q.Command) != "" {
		head := leadingCommandTokens(q.Command, names)
		allCmd := spokenCommandTokens(q.Command, names)
		matchedAll, matchedNonDriver := countMatchedCommandTokens(agentWords, allCmd)
		switch {
		case len(head) >= 2:
			if tokenMatches(agentWords, head[0]) && tokenMatches(agentWords, head[1]) {
				return true
			}
			return matchedAll >= 2 && matchedNonDriver
		case len(head) == 1:
			if !cmdDriverWords[head[0]] && tokenMatches(agentWords, head[0]) {
				return true
			}
			return len(allCmd) >= 2 && matchedAll >= 2 && matchedNonDriver
		default:
			if len(allCmd) >= 2 {
				return matchedAll >= 2 && matchedNonDriver
			}
			if len(allCmd) == 1 {
				return !cmdDriverWords[allCmd[0]] && tokenMatches(agentWords, allCmd[0])
			}
			return false
		}
	}

	qTokens := promptQuestionTokens(q.Question, names)
	matchedQ := 0
	hasTrustQ, hasLocQ := false, false
	for _, qt := range qTokens {
		if qt == "trust" {
			hasTrustQ = true
		}
		if trustLocationWords[qt] {
			hasLocQ = true
		}
		if tokenMatches(agentWords, qt) {
			matchedQ++
		}
	}
	if hasTrustQ && hasLocQ && agentWords["trust"] && hasAnyWord(agentWords, trustLocationWords) {
		return true
	}
	if hanQuestionMatches(agentText, q.Question, names) {
		return true
	}
	switch {
	case len(qTokens) >= 2:
		if matchedQ >= 2 || (matchedQ >= 1 && optMatched >= 1) {
			return true
		}
	case len(qTokens) == 1:
		if !cmdDriverWords[qTokens[0]] && tokenMatches(agentWords, qTokens[0]) {
			return true
		}
	}
	return optMatched >= 2
}

func hasAnyWord(have map[string]bool, set map[string]bool) bool {
	for w := range set {
		if have[w] {
			return true
		}
	}
	return false
}

func leadingCommandTokens(cmd string, names map[string]bool) []string {
	var head []string
	for _, field := range strings.Fields(cmd) {
		if field == "" || field[0] == '-' || !isPlainCommandField(field) {
			break
		}
		for _, w := range nameTokens(field) {
			if len([]rune(w)) < 2 || names[w] || promptStockWords[w] || menuStopWords[w] || cmdProseFiller[w] {
				continue
			}
			if !slicesContains(head, w) {
				head = append(head, w)
			}
			if len(head) == 2 {
				return head
			}
		}
	}
	return head
}

func isPlainCommandField(w string) bool {
	for _, r := range w {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return w != ""
}

func spokenCommandTokens(cmd string, names map[string]bool) []string {
	var out []string
	for _, w := range nameTokens(prose.Spoken(cmd, 0)) {
		if len([]rune(w)) < 2 || names[w] || promptStockWords[w] || menuStopWords[w] || cmdProseFiller[w] {
			continue
		}
		if !slicesContains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

func countMatchedCommandTokens(agentWords map[string]bool, allCmd []string) (matched int, nonDriver bool) {
	for _, w := range allCmd {
		if tokenMatches(agentWords, w) {
			matched++
			if !cmdDriverWords[w] {
				nonDriver = true
			}
		}
	}
	return matched, nonDriver
}

func promptQuestionTokens(question string, names map[string]bool) []string {
	var out []string
	for _, w := range nameTokens(prose.Spoken(question, 0)) {
		if len([]rune(w)) < 2 || isHan(w) {
			continue
		}
		if len([]rune(w)) == 2 && shortNameStopWords[w] {
			continue
		}
		if names[w] || promptStockWords[w] || menuStopWords[w] {
			continue
		}
		if !slicesContains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// hanQuestionMatches matches Chinese prompt content after stripping agent
// names and stock ask words: a short 2-3 Han-character prompt requires all
// of its Han content, while a longer prompt requires at least 4 shared Han
// characters (two 2-character content bigrams).
func hanQuestionMatches(agentText, question string, names map[string]bool) bool {
	qHan := cleanHanRunes(question, names)
	if len(qHan) < 2 {
		return false
	}
	aHan := string(cleanHanRunes(agentText, names))
	if len(qHan) <= 3 {
		return strings.Contains(aHan, string(qHan))
	}
	shared := 0
	for i := 0; i+1 < len(qHan); {
		bg := string(qHan[i : i+2])
		if strings.Contains(aHan, bg) {
			shared += 2
			i += 2
			if shared >= 4 {
				return true
			}
			continue
		}
		i++
	}
	return false
}

var zhStockPhrases = []string{
	"请求运行", "请求", "询问", "在问", "问你", "需要你做个决定", "做个决定",
	"想要", "允许", "批准", "确认", "同意", "是否", "要不要", "可不可以", "能不能",
	"好吗", "可以吗", "行吗", "怎么用", "怎么", "请问", "吗",
}

func cleanHanRunes(s string, names map[string]bool) []rune {
	cleaned := s
	for n := range names {
		if isHan(n) && len([]rune(n)) >= 2 {
			cleaned = strings.ReplaceAll(cleaned, n, " ")
		}
	}
	for _, p := range zhStockPhrases {
		cleaned = strings.ReplaceAll(cleaned, p, " ")
	}
	var out []rune
	for _, r := range cleaned {
		if unicode.Is(unicode.Han, r) {
			out = append(out, r)
		}
	}
	return out
}

func matchedDistinctOptions(agentWords map[string]bool, opts []state.Option, names map[string]bool) int {
	n := 0
	for _, o := range opts {
		if o.Meta {
			continue
		}
		matchedThis := false
		for _, w := range nameTokens(prose.Spoken(o.Label, 0)) {
			if len([]rune(w)) < 3 && !isHan(w) {
				continue
			}
			if len([]rune(w)) < 2 {
				continue
			}
			if names[w] || promptStockWords[w] || menuStopWords[w] || genericOptionWords[w] {
				continue
			}
			if tokenMatches(agentWords, w) {
				matchedThis = true
				break
			}
		}
		if matchedThis {
			n++
		}
	}
	return n
}

func tokenMatches(words map[string]bool, w string) bool {
	if words[w] {
		return true
	}
	if isHan(w) && len([]rune(w)) >= 2 {
		for reply := range words {
			if strings.Contains(reply, w) {
				return true
			}
		}
	}
	return false
}

func slicesContains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

func isHan(w string) bool {
	return w != "" && strings.IndexFunc(w, func(r rune) bool { return !unicode.Is(unicode.Han, r) }) == -1
}

func wordSet(ws []string) map[string]bool {
	out := make(map[string]bool, len(ws))
	for _, w := range ws {
		out[w] = true
	}
	return out
}

// nameTokens are the lower-case runs of letters and digits of text, of
// any length: "fix-claude's" is fix, claude, s.
func nameTokens(text string) []string {
	var out []string
	var token []rune
	var han bool
	flush := func() {
		if len(token) > 0 {
			out = append(out, string(token))
			token = nil
		}
	}
	for _, r := range strings.ToLower(text) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		h := unicode.Is(unicode.Han, r)
		if len(token) > 0 && h != han {
			flush()
		}
		han = h
		token = append(token, r)
	}
	flush()
	return out
}

var shortNameStopWords = map[string]bool{"go": true, "on": true, "to": true, "in": true, "it": true, "is": true, "no": true, "me": true, "my": true, "we": true, "do": true, "or": true, "an": true, "as": true, "at": true, "be": true, "by": true, "of": true, "ok": true, "so": true, "up": true, "us": true}

// namedInLocked reports whether text (the user's words) names pane's
// agent: every token of its alias or spoken name is in it. Common short
// words do not count as names. Caller holds s.mu.
func (s *Supervisor) namedInLocked(text, pane string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	row, ok := s.store.Get(pane)
	if !ok {
		return false
	}
	words := wordSet(nameTokens(text))
	for _, n := range []string{row.Alias, state.Say(row), s.sayOf(pane)} {
		n = strings.ToLower(strings.TrimSpace(n))
		if len([]rune(n)) >= 2 && strings.IndexFunc(n, func(r rune) bool { return !unicode.Is(unicode.Han, r) }) == -1 && strings.Contains(strings.ToLower(text), n) {
			return true
		}
		toks := nameTokens(n)
		long := false
		for _, w := range toks {
			long = long || len([]rune(w)) >= 2 && !shortNameStopWords[w]
		}
		if long && containsWords(words, toks) {
			return true
		}
	}
	return false
}

func containsWords(have map[string]bool, want []string) bool {
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// toldOpenLocked reports whether the open question p was told to the
// user. Caller holds s.mu.
func (s *Supervisor) toldOpenLocked(p state.PendingAction) bool {
	s.syncToldLocked()
	e := s.told.told[keyOf(p)]
	return e != nil && e.closedAt.IsZero()
}

// displacedLocked is the rule: the refusal reason when in may not answer
// p, "" otherwise. Caller holds s.mu.
func (s *Supervisor) displacedLocked(p state.PendingAction, in *shepherdInput) string {
	other, ok := s.displacedByLocked(p, in)
	if !ok {
		return ""
	}
	if other.pane == p.PaneID {
		return fmt.Sprintf("the user's words began before %s's new prompt was told to them, while its earlier prompt was the one they had heard", s.aliasOf(p.PaneID))
	}
	return fmt.Sprintf("the user's words began before %s's prompt was told to them, while %s's question was the one they had heard", s.aliasOf(p.PaneID), s.aliasOf(other.pane))
}

// displacedByLocked is the rule itself: the told question in may be
// about instead of p. Words that name p's agent and none of the other
// told questions' agents are about p (the user typed "approve fix-claude's
// command"), unless p's own pane had another prompt told before them: then
// the name does not say which of its prompts they meant. Caller holds s.mu.
func (s *Supervisor) displacedByLocked(p state.PendingAction, in *shepherdInput) (promptKey, bool) {
	if p.Kind != state.PendingAnswerPrompt || in == nil || in.kind == InputTap {
		return promptKey{}, false
	}
	s.syncToldLocked()
	k := keyOf(p)
	if e, ok := s.told.stated[k]; ok && in.began > e.seq {
		return promptKey{}, false
	}
	if e := s.told.told[k]; e != nil && e.closedAt.IsZero() && in.began > e.seq {
		return promptKey{}, false
	}
	now := s.cfg.Now()
	var other promptKey
	var otherSeq, otherOrder uint64
	found, samePane := false, false
	otherPanes := map[string]bool{}
	newerTold := func(k2 promptKey, e *toldEntry) bool {
		if !found {
			return true
		}
		if e.seq != otherSeq {
			return e.seq > otherSeq
		}
		if e.order != otherOrder {
			return e.order > otherOrder
		}
		if k2.pane != other.pane {
			return k2.pane < other.pane
		}
		return k2.sig < other.sig
	}
	for k2, e := range s.told.told {
		if k2 == k || e.seq >= in.began {
			continue
		}
		if !e.closedAt.IsZero() && now.Sub(e.closedAt) > s.cfg.AuthoriseWindow {
			continue
		}
		if k2.pane == p.PaneID {
			samePane = true
		} else {
			otherPanes[k2.pane] = true
		}
		// The newest told before the words began: the one named.
		if newerTold(k2, e) {
			other, otherSeq, otherOrder, found = k2, e.seq, e.order, true
		}
	}
	if found && !samePane && s.namedInLocked(in.text, p.PaneID) {
		for pane := range otherPanes {
			if s.namedInLocked(in.text, pane) {
				return other, true
			}
		}
		return promptKey{}, false
	}
	return other, found
}

// reaskLocked returns, once per input and question, the line that tells
// the user why in did not answer p and asks p again; "" when it was said
// already for this input. Caller holds s.mu.
func (s *Supervisor) reaskLocked(p state.PendingAction, in *shepherdInput) string {
	other, ok := s.displacedByLocked(p, in)
	if !ok {
		return ""
	}
	t := &s.told
	k := keyOf(p)
	if t.reasked[k] == in.seq {
		return ""
	}
	if t.reasked == nil {
		t.reasked = map[promptKey]uint64{}
	}
	t.reasked[k] = in.seq
	return reaskLine(s.sayOf(p.PaneID), s.sayOf(other.pane), other.pane == p.PaneID, p.SpokenSummary, s.ceoChineseLocked())
}

// reaskLine is what the user hears when their words did not answer a
// question they had not heard: why, and the question.
func reaskLine(alias, other string, samePane bool, summary string, zh bool) string {
	summary = prose.Terminate(summary)
	switch {
	case zh && samePane:
		return fmt.Sprintf("刚才的回答我没有发给%s：你说话时它换了一个问题。%s 你要怎么回答？", alias, summary)
	case zh:
		return fmt.Sprintf("刚才的回答我没有发给%s：它的问题是在你回答%s时才来的。%s 你要怎么回答？", alias, other, summary)
	case samePane:
		return fmt.Sprintf("I did not send that to %s: its question changed as you answered. %s What is your answer?", alias, summary)
	}
	return fmt.Sprintf("I did not send that to %s: its question came in while you were answering %s. %s What is your answer?", alias, other, summary)
}

// reaskTags are the tags of a re-ask push: it states p.
func reaskTags(p state.PendingAction) map[string]any {
	return map[string]any{tagPendingID: p.ID, "needs_user": true, "from": "daemon", "kind": "re-ask of a question the user had not heard"}
}

// pushReask pushes the re-ask lines queued under s.mu (a refused
// send_keys). Call it without s.mu.
func (s *Supervisor) pushReask() {
	s.mu.Lock()
	q := s.reasks
	s.reasks = nil
	s.mu.Unlock()
	for _, r := range q {
		s.pushTagged(r.pane, r.text, voice.UrgencyInterrupt, r.tags)
	}
}

// reask is a re-ask line waiting to be pushed.
type reask struct {
	pane, text string
	tags       map[string]any
}

// queueReaskLocked queues line, the re-ask of p, for pushReask. Caller
// holds s.mu.
func (s *Supervisor) queueReaskLocked(p state.PendingAction, line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	s.reasks = append(s.reasks, reask{pane: p.PaneID, text: line, tags: reaskTags(p)})
}

// typedReaskKey carries a synchronous typed-turn re-ask collector on ctx
// so TypedTurn returns a displacement re-ask in TypedReply.Text rather
// than queueing a spoken push that may have no voice listener.
type typedReaskKey struct{}

type typedReaskItem struct {
	key  promptKey
	pane string
	text string
}

type typedReaskCollector struct {
	mu    sync.Mutex
	items []typedReaskItem
}

func (c *typedReaskCollector) add(key promptKey, pane, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	c.mu.Lock()
	c.items = append(c.items, typedReaskItem{key: key, pane: pane, text: text})
	c.mu.Unlock()
}

func withTypedReasks(ctx context.Context) (context.Context, *typedReaskCollector) {
	c := &typedReaskCollector{}
	return context.WithValue(ctx, typedReaskKey{}, c), c
}

func typedReasksOf(ctx context.Context) *typedReaskCollector {
	c, _ := ctx.Value(typedReaskKey{}).(*typedReaskCollector)
	return c
}

// finishTypedReasks merges any displacement re-ask lines produced during a
// TypedTurn into the synchronous text reply and records those questions as
// told when the turn context has not been cancelled.
func (s *Supervisor) finishTypedReasks(ctx context.Context, spoken string, tr *typedReaskCollector) string {
	if tr == nil {
		return spoken
	}
	tr.mu.Lock()
	items := append([]typedReaskItem(nil), tr.items...)
	tr.mu.Unlock()
	if len(items) == 0 || ctx.Err() != nil {
		return spoken
	}
	var lines []string
	s.mu.Lock()
	for _, it := range items {
		s.toldKeyForLocked(it.key, "", "")
		if it.pane != "" {
			s.lastSubject = it.pane
		}
		if !strings.Contains(spoken, it.text) {
			lines = append(lines, it.text)
		}
	}
	s.mu.Unlock()
	if len(lines) == 0 {
		return spoken
	}
	extra := strings.Join(lines, " ")
	if strings.TrimSpace(spoken) == "" {
		return extra
	}
	return strings.TrimSpace(spoken) + " " + extra
}
