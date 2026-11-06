package supervisor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Approval rail v2 (docs/dev/roles.md "The one rail"): a question counts as
// told only when its key content played on the client (voice.EventPlayed) or
// was returned synchronously to a typed client (NoteReturnedText). Under s.mu.
type promptKey struct{ pane, sig string }

func keyOf(p state.PendingAction) promptKey {
	return promptKey{pane: p.PaneID, sig: pendingSignature(p)}
}

type toldEntry struct {
	seq, order uint64
	at         time.Time
	closedAt   time.Time
}

type toldState struct {
	told      map[promptKey]*toldEntry
	nextOrder uint64
	reasked   map[promptKey]uint64
}

func (s *Supervisor) resetToldLocked() {
	s.syncToldLocked()
	for k, e := range s.told.told {
		if !e.closedAt.IsZero() {
			delete(s.told.told, k)
		}
	}
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
	for k := range t.reasked {
		if _, ok := open[k]; !ok {
			delete(t.reasked, k)
		}
	}
}

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
}

// closedLocked records that p left the open questions (cleared from the
// slot, dropped from the list): unless its pane and prompt are still open
// under another id, it closed now. Caller holds s.mu.
func (s *Supervisor) closedLocked() {
	s.syncToldLocked()
}

func (s *Supervisor) untellLocked(k promptKey) {
	t := &s.told
	if e := t.told[k]; e != nil && e.closedAt.IsZero() {
		e.closedAt = s.cfg.Now()
	}
}

// NoteReturnedText records text returned synchronously to a typed client.
func (s *Supervisor) NoteReturnedText(text string) { s.notePlayed(text, time.Time{}) }

func (s *Supervisor) notePlayed(text string, at time.Time) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() {
		at = s.cfg.Now()
	}
	s.syncToldLocked()
	for _, q := range s.openKeysLocked() {
		if q.Held || s.heldQuestion(q.ID) {
			if !readsOut(text, q) {
				continue
			}
			q = s.promoteHeldLocked(q)
		} else if !s.statesQuestionLocked(text, q) {
			continue
		} else if readsOut(text, q) && s.paneMutedCached(q.PaneID) {
			q = s.promoteHeldLocked(q)
		}
		k := keyOf(q)
		if e := s.told.told[k]; e != nil && e.closedAt.IsZero() {
			if at.Before(e.at) {
				e.at = at
			}
			continue
		}
		if s.told.told == nil {
			s.told.told = map[promptKey]*toldEntry{}
		}
		s.told.nextOrder++
		s.told.told[k] = &toldEntry{seq: s.inputSeq, order: s.told.nextOrder, at: at}
	}
}

// readsOut reports whether text carries q's whole spoken summary: the
// question read out, not mentioned in passing.
func readsOut(text string, q state.PendingAction) bool {
	sum := strings.ToLower(strings.TrimRight(strings.TrimSpace(q.SpokenSummary), ".。!?！？ "))
	return sum != "" && (strings.Contains(strings.ToLower(text), sum) || countSeq(nameTokens(text), nameTokens(sum)) > 0)
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

func (s *Supervisor) statesQuestionLocked(text string, p state.PendingAction) bool {
	if !s.namedInLocked(text, p.PaneID) {
		return false
	}
	if readsOut(text, p) {
		return true
	}
	for _, a := range s.store.List() {
		if a.PaneID != p.PaneID && s.namedInLocked(text, a.PaneID) {
			return false
		}
	}
	src := p.Question
	if p.Command != "" {
		src = p.Command
		if i := strings.IndexAny(src, "'\"|><;&$`"); i >= 0 {
			src = src[:i]
		}
	}
	row, _ := s.store.Get(p.PaneID)
	names := wordSet(nameTokens(row.Alias + " " + state.Say(row) + " " + s.sayOf(p.PaneID)))
	words := wordSet(nameTokens(text))
	matched := 0
	for _, w := range nameTokens(prose.Spoken(src, 0)) {
		if isHan(w) {
			w = strings.TrimLeft(w, "是否要不请求询问允许批准确认运行执")
		}
		if len([]rune(w)) < 2 || names[w] || promptStockWords[w] || (shortNameStopWords[w] && w != "go") {
			continue
		}
		if !tokenMatches(words, w) {
			return false
		}
		matched++
	}
	return matched > 0
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

func tokenMatches(words map[string]bool, w string) bool {
	if words[w] {
		return true
	}
	for reply := range words {
		if isHan(w) && len([]rune(w)) >= 2 && strings.Contains(reply, w) {
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
	allToks := nameTokens(text)
	words := wordSet(allToks)
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
		if long && containsWords(words, toks) && !s.subsumedNameLocked(allToks, pane, toks) {
			return true
		}
	}
	return false
}

func (s *Supervisor) subsumedNameLocked(textToks []string, pane string, toks []string) bool {
	want := countSeq(textToks, toks)
	for _, a := range s.store.List() {
		for _, n := range []string{a.Alias, state.Say(a), s.sayOf(a.PaneID)} {
			if other := nameTokens(n); a.PaneID != pane && len(other) > len(toks) && countSeq(other, toks) > 0 && countSeq(textToks, other) >= want && want > 0 {
				return true
			}
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

func (s *Supervisor) clientReportsPlayback() bool {
	pr, ok := s.pusher.(interface{ PlaybackReports() bool })
	return !ok || pr.PlaybackReports()
}

// displacedLocked is the rule: the refusal reason when in may not answer
// p, "" otherwise. Caller holds s.mu.
func (s *Supervisor) displacedLocked(p state.PendingAction, in *shepherdInput) string {
	reason, _, _ := s.checkPromptLocked(p, in)
	return reason
}

func (s *Supervisor) checkPromptLocked(p state.PendingAction, in *shepherdInput) (reason string, other promptKey, kind string) {
	if p.Kind != state.PendingAnswerPrompt || in == nil || in.kind == InputTap {
		return "", promptKey{}, ""
	}
	s.syncToldLocked()
	k := keyOf(p)
	row, _ := s.store.Get(p.PaneID)
	stillSame := row.State == herdr.StateBlocked && (row.LastBlock == nil || !row.LastBlock.Answered) && s.lastPrompt[p.PaneID] == k.sig
	if e := s.told.told[k]; e != nil && (e.closedAt.IsZero() || stillSame) && !e.at.After(in.beganAt) && in.began > e.seq {
		return "", promptKey{}, ""
	}
	now := s.cfg.Now()
	var otherSeq, otherOrder uint64
	found, samePane := false, false
	otherPanes := map[string]bool{}
	for k2, q := range s.openKeysLocked() {
		if k2.pane != p.PaneID && !q.Held && !s.heldQuestion(q.ID) {
			otherPanes[k2.pane] = true
		}
	}
	for k2, e := range s.told.told {
		if k2 == k || e.at.After(in.beganAt) || e.seq >= in.began || (!e.closedAt.IsZero() && now.Sub(e.closedAt) > s.cfg.AuthoriseWindow) {
			continue
		}
		if k2.pane == p.PaneID {
			samePane = true
		} else {
			otherPanes[k2.pane] = true
		}
		if !found || e.seq > otherSeq || (e.seq == otherSeq && (e.order > otherOrder || (e.order == otherOrder && (k2.pane < other.pane || (k2.pane == other.pane && k2.sig < other.sig))))) {
			other, otherSeq, otherOrder, found = k2, e.seq, e.order, true
		}
	}
	if !samePane && s.namedInLocked(in.text, p.PaneID) {
		ambig := false
		for pane := range otherPanes {
			ambig = ambig || s.namedInLocked(in.text, pane)
		}
		if !ambig {
			return "", promptKey{}, ""
		}
	}
	switch {
	case samePane:
		return fmt.Sprintf("the user's words began before %s's new prompt was told to them, while its earlier prompt was the one they had heard", s.aliasOf(p.PaneID)), promptKey{pane: p.PaneID}, "same_pane"
	case found:
		return fmt.Sprintf("the user's words began before %s's prompt was told to them, while %s's question was the one they had heard", s.aliasOf(p.PaneID), s.aliasOf(other.pane)), other, "displaced"
	case in.noReports:
		return fmt.Sprintf("the client sends no playback reports, so %s's prompt was not told to them", s.aliasOf(p.PaneID)), promptKey{}, "no_reports"
	default:
		return fmt.Sprintf("the user's words began before %s's prompt was told to them", s.aliasOf(p.PaneID)), promptKey{}, "untold"
	}
}

// reaskLocked returns, once per input and question, the line that tells
// the user why in did not answer p and asks p again; "" when it was said
// already for this input. Caller holds s.mu.
func (s *Supervisor) reaskLocked(p state.PendingAction, in *shepherdInput) string {
	reason, other, kind := s.checkPromptLocked(p, in)
	if reason == "" {
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
	return reaskLine(s.sayOf(p.PaneID), s.sayOf(other.pane), kind, p.SpokenSummary, s.ceoChineseLocked())
}

// reaskLine is what the user hears when their words did not answer a
// question they had not heard: why, and the question.
func reaskLine(alias, other, kind, summary string, zh bool) string {
	summary = prose.Terminate(summary)
	switch {
	case zh && kind == "same_pane":
		return fmt.Sprintf("等等——你回答时%s的问题刚变了，所以我没发出去。%s 这个新问题怎么处理？", alias, summary)
	case zh && kind == "displaced":
		return fmt.Sprintf("等等——我没把刚才的回复发给%s，因为你在回答%s时它刚弹出了新问题。%s %s这边怎么处理？", alias, other, summary, alias)
	case zh && kind == "no_reports":
		return fmt.Sprintf("当前客户端不支持播放确认，单说“好”不会批准%s。%s 请说“批准%s”或“拒绝%s”，或直接点选。", alias, summary, alias, alias)
	case zh:
		return fmt.Sprintf("我还没把%s的原话读完：%s 现在请回答。", alias, summary)
	case kind == "same_pane":
		return fmt.Sprintf("Hold on — %s's prompt changed just as you answered, so I did not send that. %s What is your answer for this one?", alias, summary)
	case kind == "displaced":
		return fmt.Sprintf("Hold on — I did not send that to %s, because its prompt came in while you were answering %s. %s What should %s do?", alias, other, summary, alias)
	case kind == "no_reports":
		return fmt.Sprintf("I can't confirm audio playback on this client, so a bare yes won't approve %s. %s Say 'yes, %s' or 'no, %s', or tap to answer.", alias, summary, alias, alias)
	}
	return fmt.Sprintf("I hadn't finished reading %s's exact prompt yet: %s Go ahead and answer now.", alias, summary)
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
