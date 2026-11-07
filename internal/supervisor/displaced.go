package supervisor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Approval rail v2 (docs/dev/roles.md "The one rail"): whole played voice runs
// after pending_id push (EventPlayed) or returned text (NoteReturnedText). Under s.mu.
// drainReportLag is the client's 300 ms underrun window before drained (audio-util.js, PlaybackQueue.swift).
const drainReportLag = 300 * time.Millisecond

type promptKey struct{ pane, sig string }

func keyOf(p state.PendingAction) promptKey {
	return promptKey{pane: p.PaneID, sig: pendingSignature(p)}
}

type toldEntry struct {
	seq, order uint64
	at         time.Time
	closedAt   time.Time
	handed     time.Time // the hand-over this entry's order came from
}

type toldState struct {
	handed    map[string]toldEntry
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
	for id := range t.handed {
		if _, _, ok := s.questionByID(id); !ok {
			delete(t.handed, id)
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

func (s *Supervisor) untellLocked(k promptKey) {
	t := &s.told
	if e := t.told[k]; e != nil && e.closedAt.IsZero() {
		e.closedAt = s.cfg.Now()
	}
	if q, ok := s.openKeysLocked()[k]; ok {
		delete(t.handed, q.ID)
	}
}

func (s *Supervisor) noteHanded(id string) {
	s.mu.Lock()
	s.noteHandedLocked(id)
	s.mu.Unlock()
}

func (s *Supervisor) noteHandedLocked(id string) {
	s.syncToldLocked()
	if _, _, open := s.questionByID(id); open {
		if s.told.handed == nil {
			s.told.handed = map[string]toldEntry{}
		}
		s.told.handed[id] = toldEntry{at: s.cfg.Now(), seq: s.inputSeq}
	}
}

// NoteReturnedText records text returned synchronously to a typed client.
func (s *Supervisor) NoteReturnedText(text string) { s.notePlayed(voice.EventPlayed{Text: text}, true) }

func (s *Supervisor) notePlayed(ev voice.EventPlayed, typed bool) {
	if strings.TrimSpace(ev.Text) == "" || (!typed && !s.clientReportsPlayback()) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at, start := s.cfg.Now(), s.cfg.Now()
	if in := s.lastInputLocked(); typed && in != nil && in.kind == InputTyped {
		start = in.beganAt
	} else if !typed && !ev.End.IsZero() {
		if start = ev.Start; start.IsZero() {
			start = ev.End
		}
		if at = ev.End.Add(-drainReportLag); at.Before(start) {
			at = start
		}
	}
	s.syncToldLocked()
	s.told.nextOrder++
	for _, q := range s.openKeysLocked() {
		seq := s.inputSeq
		if q.Held || s.heldQuestion(q.ID) || (typed && s.paneMutedCached(q.PaneID)) {
			if !typed || s.promptSinceLocked(q).After(start) || !readsOut(ev.Text, q) {
				continue
			}
			q = s.promoteHeldLocked(q)
		} else if h, ok := s.told.handed[q.ID]; typed && s.promptSinceLocked(q).After(start) || (!typed && (!ok || start.Before(h.at))) || !s.namedInLocked(ev.Text, q.PaneID) {
			continue
		} else if !typed && ev.End.After(start) {
			seq = h.seq
		}
		k := keyOf(q)
		if e := s.told.told[k]; e != nil && e.closedAt.IsZero() {
			if at.Before(e.at) {
				e.at = at
			}
			e.seq = min(e.seq, seq)
			if h := s.told.handed[q.ID]; h.at.After(e.handed) {
				e.handed, e.order = h.at, s.told.nextOrder
			}
			continue
		}
		if s.told.told == nil {
			s.told.told = map[promptKey]*toldEntry{}
		}
		s.told.told[k] = &toldEntry{seq: seq, order: s.told.nextOrder, at: at, handed: s.told.handed[q.ID].at}
	}
}

// readsOut reports whether text carries q's whole spoken summary: the
// question read out, not mentioned in passing.
func readsOut(text string, q state.PendingAction) bool {
	sum := strings.ToLower(strings.TrimRight(strings.TrimSpace(q.SpokenSummary), ".。!?！？ "))
	return sum != "" && (strings.Contains(strings.ToLower(text), sum) || countSeq(nameTokens(text), nameTokens(sum)) > 0)
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

// nameTokens are the lower-case runs of letters and digits of text, of
// any length (each Han character its own token): "fix-claude's" is fix, claude, s.
func nameTokens(text string) []string {
	var out []string
	var token []rune
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
		if unicode.Is(unicode.Han, r) {
			flush()
			out = append(out, string(r))
			continue
		}
		token = append(token, r)
	}
	flush()
	return out
}

// namedInLocked reports whether text names pane's agent by exact match on its
// alias or spoken name. Caller holds s.mu.
func (s *Supervisor) namedInLocked(text, pane string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	row, ok := s.store.Get(pane)
	if !ok {
		return false
	}
	allToks := nameTokens(text)
	for _, n := range []string{row.Alias, state.Say(row), s.sayOf(pane)} {
		if toks := nameTokens(n); len(toks) > 0 && countSeq(allToks, toks) > 0 && !s.subsumedNameLocked(allToks, pane, toks) {
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
	cur := s.told.told[k]
	pTold := cur != nil && cur.closedAt.IsZero() && !cur.at.After(in.beganAt) && in.began > cur.seq
	now := s.cfg.Now()
	var otherSeq, otherOrder uint64
	found, samePane, ambigRun := false, false, false
	open := s.openKeysLocked()
	otherPanes := map[string]bool{}
	for k2, q := range open {
		if k2.pane != p.PaneID && !q.Held && !s.heldQuestion(q.ID) {
			otherPanes[k2.pane] = true
		}
	}
	for k2, e := range s.told.told {
		if k2 == k || e.at.After(in.beganAt) || e.seq >= in.began || (!e.closedAt.IsZero() && now.Sub(e.closedAt) > s.cfg.AuthoriseWindow) {
			continue
		}
		if q := open[k2]; pTold && k2.pane != p.PaneID && !q.Held && !s.heldQuestion(q.ID) && e.order >= cur.order {
			ambigRun = true
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
	if pTold && !ambigRun {
		return "", promptKey{}, ""
	}
	if _, _, bare := bareAnswer(p, in.text); !samePane && !bare && s.namedInLocked(in.text, p.PaneID) {
		ambig := false
		for pane := range otherPanes {
			ambig = ambig || s.namedInLocked(in.text, pane)
		}
		if !ambig {
			return "", promptKey{}, ""
		}
	}
	switch {
	case pTold:
		return fmt.Sprintf("another open prompt (%s) was told to them alongside %s's, so a bare answer is ambiguous", s.aliasOf(other.pane), s.aliasOf(p.PaneID)), other, "ambiguous"
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
	case zh && kind == "ambiguous":
		return fmt.Sprintf("%s和%s都在等你回答，所以我没发出去。%s 请点名回答%s或%s，或直接点选。", alias, other, summary, alias, other)
	case zh && kind == "same_pane":
		return fmt.Sprintf("等等——你回答时%s的问题刚变了，所以我没发出去。%s 这个新问题怎么处理？", alias, summary)
	case zh && kind == "displaced":
		return fmt.Sprintf("等等——我没把刚才的回复发给%s，因为你在回答%s时它刚弹出了新问题。%s %s这边怎么处理？", alias, other, summary, alias)
	case zh && kind == "no_reports":
		return fmt.Sprintf("当前客户端不支持播放确认，单说“好”不会批准%s。%s 请说“批准%s”或“拒绝%s”，或直接点选。", alias, summary, alias, alias)
	case zh:
		return fmt.Sprintf("我还没把%s的原话读完：%s 现在请回答。", alias, summary)
	case kind == "ambiguous":
		return fmt.Sprintf("Both %s and %s are waiting for your answer, so I did not send that. %s Say 'yes, %s' or 'yes, %s', or tap to answer.", alias, other, summary, alias, other)
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
	pane string
	text string
}

type typedReaskCollector struct {
	mu    sync.Mutex
	items []typedReaskItem
}

func (c *typedReaskCollector) add(pane, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	c.mu.Lock()
	c.items = append(c.items, typedReaskItem{pane: pane, text: text})
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
