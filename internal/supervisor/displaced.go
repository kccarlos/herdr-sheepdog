package supervisor

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The displacement rail (audit 2026-11, finding 1; reviewed twice). The
// user hears question A and answers it, but in the seconds before the
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
//     layer then judges that line unheard or not voiced (the telling is
//     taken back);
//   - an orchestrator reply delivered to the user that states it: it names
//     the agent and a word of its prompt (toldInReply, and a late reply when
//     its push goes out). A held question (muted space) a reply states is
//     promoted only when the user's words named its agent or the reply
//     reads its question out: the user heard it now. A question a reply states while it is not
//     open, its pane still at the prompt, is told when it is installed
//     again within the window (send_keys revives it).
//
// A tool result that goes to the orchestrator tells nothing: the user has
// not heard it. When a question closes (answered, its pane left the
// prompt, replaced on its pane) its telling is kept for the window, then
// forgotten; the same prompt asked again later is a new question.
// "Started" is the input's first fragment, so a "yes" that began while A
// was the open question and ended after B blocked is still about A.
//
// When the rule refuses, the user hears why and is asked again, once per
// input (reaskLocked): the fast path answers with that line, and a refused
// send_keys pushes it. That line tells p, so the user's next answer goes
// through. Refusing a yes the user meant for the only question they heard
// is a failure too: that is why the rule is about what was told, not
// about what was installed.
//
// Outside the rule nothing changes: a question when nothing else told is
// live (the only question, or the first since the last closed longer than
// the window ago), and any answer that began after p was told. A tap names
// its question by id and is never refused here. With no voice session
// nothing is ever told by a push. The bookkeeping is in memory: after a
// restart nothing counts as told. All of it is under s.mu.

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
	// line is the push that told it ("" for a reply returned as a tool
	// result): only that line judged unheard takes the telling back.
	line string
	// closedAt is when the question left the open questions; zero while
	// open.
	closedAt time.Time
}

// toldState is what the displacement rail knows.
type toldState struct {
	// told are the questions told, open or closed within the window.
	told map[promptKey]*toldEntry
	// lines are the spoken lines handed to the voice that state questions
	// still open: the questions each tells when it goes out.
	lines map[string][]promptKey
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
}

// statedEntry is a reply's telling of a question that was not open.
type statedEntry struct {
	seq uint64
	at  time.Time
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
			t.told[k] = &toldEntry{seq: st.seq}
		}
	}
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
		} else {
			t.lines[text] = live
		}
	}
	delete(t.stated, k)
}

// toldKeyLocked records that the open question k was told to the user
// now, by line ("" for a reply returned as a tool result). Caller holds
// s.mu.
func (s *Supervisor) toldKeyLocked(k promptKey, line string) {
	t := &s.told
	if e := t.told[k]; e != nil && e.closedAt.IsZero() {
		return
	}
	if t.told == nil {
		t.told = map[promptKey]*toldEntry{}
	}
	t.told[k] = &toldEntry{seq: s.inputSeq, line: line}
}

// promptLineLocked records text as a spoken line that states the open
// question with this id, whether or not it is the voice's question now.
// Caller holds s.mu.
func (s *Supervisor) promptLineLocked(text, id string) {
	q, ok := s.openByIDLocked(id)
	if !ok || text == "" {
		return
	}
	s.addLineLocked(text, []promptKey{keyOf(q)})
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

// linePushedLocked records that the spoken line text went to the user now
// (EventPush; PushShown with no voice): the questions it states, and the
// one pendingID names, were told. A held question (muted space) is told,
// and promoted, only by a push about it (pendingID: its announcement);
// lines never list one (replyLineLocked). Caller holds s.mu.
func (s *Supervisor) linePushedLocked(text, pendingID string) {
	s.syncToldLocked()
	open := s.openKeysLocked()
	keys := append([]promptKey(nil), s.told.lines[text]...)
	var named promptKey
	if q, ok := s.openByIDLocked(pendingID); ok {
		named = keyOf(q)
		keys = append(keys, named)
	}
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
		s.toldKeyLocked(keyOf(q), text)
	}
}

// lineUnheardLocked takes back the telling of the questions text told,
// now that the voice layer judged it unheard or not voiced. Caller holds
// s.mu.
func (s *Supervisor) lineUnheardLocked(text string) {
	for _, k := range s.told.lines[text] {
		if e := s.told.told[k]; e != nil && e.closedAt.IsZero() && e.line == text {
			delete(s.told.told, k)
		}
	}
}

// PushShown records a spoken line shown to the user with no voice model
// (--voice=off: the panel shows skipped pushes): it tells what it states.
func (s *Supervisor) PushShown(text string, tags map[string]any) {
	id, _ := tags[tagPendingID].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linePushedLocked(text, id)
}

// toldInReply records an orchestrator reply delivered to the user now (a
// tool result the voice speaks, or a typed turn's text): the open
// questions it states were told. A held question (muted space) it states
// is told, and promoted, only when the user's newest words named its
// agent or the reply reads its question out (its whole spoken summary): a
// status reply that mentions it in passing leaves it held. A
// question it states that is not open (its pane still at the prompt) is
// told if it is installed again within the window (installedLocked).
func (s *Supervisor) toldInReply(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncToldLocked()
	var asked string
	if in := s.lastInputLocked(); in != nil {
		asked = in.text
	}
	for _, q := range s.statedInLocked(text) {
		if q.Held || s.heldQuestion(q.ID) {
			if !s.namedInLocked(asked, q.PaneID) && !readsOut(text, q) {
				continue
			}
			q = s.promoteHeldLocked(q)
		}
		s.toldKeyLocked(keyOf(q), "")
	}
	for _, q := range s.statedNotOpenLocked(text) {
		if s.told.stated == nil {
			s.told.stated = map[promptKey]statedEntry{}
		}
		s.told.stated[keyOf(q)] = statedEntry{seq: s.inputSeq, at: s.cfg.Now()}
	}
}

// readsOut reports whether text carries q's whole spoken summary: the
// question read out, not mentioned in passing.
func readsOut(text string, q state.PendingAction) bool {
	sum := strings.ToLower(strings.TrimRight(strings.TrimSpace(q.SpokenSummary), ".。!?！？ "))
	return sum != "" && strings.Contains(strings.ToLower(text), sum)
}

// replyLineLocked records text, a late reply about to be pushed, as a
// line that tells the open questions it states. A held question is left
// out: a late reply does not promote it. Caller holds s.mu.
func (s *Supervisor) replyLineLocked(text string) {
	var keys []promptKey
	for _, q := range s.statedInLocked(text) {
		if q.Held || s.heldQuestion(q.ID) {
			continue
		}
		keys = append(keys, keyOf(q))
	}
	s.addLineLocked(text, keys)
}

// promptStockWords are words of a prompt too common to say which prompt
// a reply states.
var promptStockWords = map[string]bool{
	"asks": true, "ask": true, "asking": true, "wants": true, "permission": true, "approve": true, "approval": true,
	"allow": true, "run": true, "running": true, "following": true, "command": true, "proceed": true, "should": true,
	"could": true, "may": true, "let": true, "waiting": true, "question": true, "prompt": true, "yes": true, "don't": true,
	"again": true, "commands": true, "start": true, "who": true, "how": true, "will": true, "not": true, "are": true, "have": true,
}

// statedInLocked are the open questions text states: it names the
// question's agent and shares a word (three letters or more, not the
// agent's name or a stock word) with its prompt. A reply that names an
// agent without saying what it asks tells nothing. Caller holds s.mu.
func (s *Supervisor) statedInLocked(text string) []state.PendingAction {
	words := wordSet(menuWords(text))
	var out []state.PendingAction
	for _, q := range s.openKeysLocked() {
		if s.statesLocked(words, q) {
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
	words := wordSet(menuWords(text))
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
		if s.statesLocked(words, q) {
			out = append(out, q)
		}
	}
	return out
}

// statesLocked reports whether words (a reply's) state q: they name its
// agent and share a word with its prompt. Caller holds s.mu.
func (s *Supervisor) statesLocked(words map[string]bool, q state.PendingAction) bool {
	row, ok := s.store.Get(q.PaneID)
	if !ok {
		return false
	}
	names := map[string]bool{}
	named := false
	for _, n := range []string{row.Alias, state.Say(row), s.sayOf(q.PaneID)} {
		nw := menuWords(n)
		for _, w := range nw {
			names[w] = true
		}
		if len(nw) > 0 && containsWords(words, nw) {
			named = true
		}
	}
	if !named {
		return false
	}
	for _, w := range menuWords(q.Command + " " + q.Question + " " + q.SpokenSummary) {
		if words[w] && !names[w] && !promptStockWords[w] && !menuStopWords[w] {
			return true
		}
	}
	return false
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
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// namedInLocked reports whether text (the user's words) names pane's
// agent: every token of its alias or spoken name is in it. A name with no
// token of three letters or more ("go", "ui") never counts, so everyday
// words are not taken for it. Caller holds s.mu.
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
		toks := nameTokens(n)
		long := false
		for _, w := range toks {
			long = long || len([]rune(w)) >= 3
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
	if e := s.told.told[k]; e != nil && e.closedAt.IsZero() && in.began > e.seq {
		return promptKey{}, false
	}
	now := s.cfg.Now()
	var other promptKey
	var otherSeq uint64
	found, samePane := false, false
	otherPanes := map[string]bool{}
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
		if !found || e.seq > otherSeq {
			other, otherSeq, found = k2, e.seq, true
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
