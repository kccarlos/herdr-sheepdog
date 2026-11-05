package herdrcore

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

var _ herdr.Marker = (*Backend)(nil)

// The terminal labels herdr-app's clients share (herdr-app docs/labels.md):
// herdr.seen=<turn> is the last turn a person looked at, and a non-empty
// herdr.pinned pins the terminal. Both end with the terminal.
const (
	labelSeen   = "herdr.seen"
	labelPinned = "herdr.pinned"
	pinnedValue = "1"
)

// markChangeBuffer is how many pane ids MarkChanges holds for a receiver
// that is busy; past it a signal is dropped (the receiver reads AllMarks
// again, so a dropped id costs nothing).
const markChangeBuffer = 64

// marksOf reads the marks out of a terminal record. A missing or
// unparseable herdr.seen is no record: it is not turn 0.
func marksOf(t hc.Terminal) herdr.Marks {
	m := herdr.Marks{
		Turn:   t.Turn,
		Pinned: t.Labels[labelPinned] != "",
		Word:   mapState(t.Status),
		Error:  t.Status != nil && t.Status.State == "error",
		Shared: true,
	}
	if seen, err := strconv.ParseUint(t.Labels[labelSeen], 10, 64); err == nil {
		m.Seen = &seen
	}
	return m
}

// markCache is the marks of every live local terminal, kept current by
// RunMarks. It is only trusted while that subscription is live.
type markCache struct {
	mu    sync.Mutex
	live  bool
	marks map[string]herdr.Marks
	// echoes are the label states of this backend's own writes whose labels
	// events have not come back yet, oldest first, by address. An event
	// that matches the oldest is the echo of a write, not another client's.
	echoes map[string][]echo
}

// echo is the seen and pinned labels a write leaves behind.
type echo struct {
	seen   *uint64
	pinned bool
}

// maxEchoes bounds the queue of a terminal whose echoes never come (a write
// that changed nothing sends no event) while still holding a burst of writes.
const maxEchoes = 64

// RunMarks keeps the marks of every local terminal current from one
// unfiltered subscription, until ctx ends. It re-reads everything after a
// reconnect or a missed notification. While it runs, AllMarks and the
// writes read the cache instead of asking herdr-core.
func (b *Backend) RunMarks(ctx context.Context) {
	b.stream(ctx, "marks", "", hc.SubscribeFilter{}, func(ctx context.Context, sub *hc.Subscription) bool {
		_, terminals := sub.Snapshot()
		b.replaceMarks(terminals)
		defer b.setMarksLive(false)
		for {
			select {
			case <-ctx.Done():
				return false
			case n, ok := <-sub.Events():
				if !ok {
					return false
				}
				b.applyMarkEvent(ctx, n)
			}
		}
	})
}

// MarksLive reports whether the marks subscription has loaded its snapshot
// and is keeping the cache live.
func (b *Backend) MarksLive() bool {
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	return b.cache.live
}

// setMarksLive says whether the subscription behind the cache is up. The
// marks stay after it drops, so the next snapshot can tell what changed
// while it was down.
func (b *Backend) setMarksLive(live bool) {
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	b.cache.live = live
}

// replaceMarks swaps the whole cache for terminals, telling MarkChanges
// about every terminal whose marks differ from what the cache held.
func (b *Backend) replaceMarks(terminals []hc.Terminal) {
	next := make(map[string]herdr.Marks, len(terminals))
	for _, t := range terminals {
		if isPane(t) {
			next[t.Address] = marksOf(t)
		}
	}
	b.cache.mu.Lock()
	prev := b.cache.marks
	b.cache.marks, b.cache.live = next, true
	b.cache.echoes = nil // the snapshot shows every write, echoed or not
	b.cache.mu.Unlock()
	if prev == nil {
		return // the first snapshot: nothing changed, the cache was cold
	}
	for address, m := range next {
		if old, ok := prev[address]; !ok || !sameMarks(old, m) {
			b.signalMarks(address)
		}
	}
	for address := range prev {
		if _, ok := next[address]; !ok {
			b.signalMarks(address)
		}
	}
}

// applyMarkEvent folds one notification into the cache.
func (b *Backend) applyMarkEvent(ctx context.Context, n hc.Notification) {
	if n.Method == "missed" {
		b.replaceMarks(n.Terminals)
		return
	}
	if n.Method != "event" || !isLocal(n.Terminal) {
		return
	}
	switch n.Type {
	case "created", "restarted":
		if t, err := b.client.Get(ctx, n.Terminal); err == nil && isPane(t) {
			b.setMarks(n.Terminal, marksOf(t))
		}
	case "removed", "finished":
		b.dropMarks(n.Terminal)
	case "status":
		var data hc.StatusData
		if n.DecodeData(&data) == nil {
			b.updateMarks(n.Terminal, func(m herdr.Marks) herdr.Marks {
				m.Word, m.Error = mapState(data.Status), data.Status != nil && data.Status.State == "error"
				return m
			})
		}
	case "turn":
		var data hc.TurnData
		if n.DecodeData(&data) == nil {
			b.updateMarks(n.Terminal, func(m herdr.Marks) herdr.Marks {
				m.Turn = data.Turn
				return m
			})
		}
	case "labels":
		var data hc.LabelsData
		if n.DecodeData(&data) == nil {
			labelled := marksOf(hc.Terminal{Labels: data.Labels})
			b.applyLabels(n.Terminal, labelled.Seen, labelled.Pinned)
		}
	}
}

// applyLabels folds a labels event into the cache. The echo of this
// backend's own write changes nothing and signals nothing: the cache already
// holds what the write left, and holds more while later writes are still
// to be echoed. Any other event is another client's write.
func (b *Backend) applyLabels(address string, seen *uint64, pinned bool) {
	b.cache.mu.Lock()
	old, known := b.cache.marks[address]
	if !known {
		b.cache.mu.Unlock()
		return
	}
	next := old
	next.Seen, next.Pinned = seen, pinned
	if queue := b.cache.echoes[address]; len(queue) > 0 {
		for i := range queue {
			if queue[i].matches(seen, pinned) {
				if i == len(queue)-1 {
					delete(b.cache.echoes, address)
					b.cache.marks[address] = next // the last of our writes: the labels as they are
				} else {
					b.cache.echoes[address] = queue[i+1:]
				}
				b.cache.mu.Unlock()
				return
			}
		}
	}
	b.cache.marks[address] = next
	b.cache.mu.Unlock()
	if !sameSeen(old.Seen, seen) || old.Pinned != pinned {
		b.signalMarks(address)
	}
}

func (e echo) matches(seen *uint64, pinned bool) bool {
	return sameSeen(e.seen, seen) && e.pinned == pinned
}

// expectEcho records that a write leaving seen and pinned is about to be
// echoed. It reports false when the terminal is not in the cache, which
// needs no echo (nothing is cached to protect).
func (b *Backend) expectEcho(address string, e echo) bool {
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	if _, ok := b.cache.marks[address]; !ok || !b.cache.live {
		return false
	}
	if b.cache.echoes == nil {
		b.cache.echoes = map[string][]echo{}
	}
	queue := append(b.cache.echoes[address], e)
	if len(queue) > maxEchoes {
		queue = queue[len(queue)-maxEchoes:]
	}
	b.cache.echoes[address] = queue
	return true
}

// cancelEcho drops the newest expected echo: its write failed.
func (b *Backend) cancelEcho(address string) {
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	if queue := b.cache.echoes[address]; len(queue) > 1 {
		b.cache.echoes[address] = queue[:len(queue)-1]
	} else {
		delete(b.cache.echoes, address)
	}
}

// setMarks caches the marks of a terminal, known or not, and signals
// MarkChanges when they are new or differ.
func (b *Backend) setMarks(address string, next herdr.Marks) {
	b.cache.mu.Lock()
	if b.cache.marks == nil {
		b.cache.mu.Unlock()
		return
	}
	old, known := b.cache.marks[address]
	b.cache.marks[address] = next
	b.cache.mu.Unlock()
	if !known || !sameMarks(old, next) {
		b.signalMarks(address)
	}
}

// updateMarks applies change to the cached marks of a terminal the cache
// knows. It signals MarkChanges when the turn, seen or pinned changed; a
// new Word alone is cached without a signal, because the Watcher reports
// the state change itself.
func (b *Backend) updateMarks(address string, change func(herdr.Marks) herdr.Marks) {
	b.cache.mu.Lock()
	old, known := b.cache.marks[address]
	if !known {
		b.cache.mu.Unlock()
		return
	}
	next := change(old)
	b.cache.marks[address] = next
	b.cache.mu.Unlock()
	if old.Turn != next.Turn || !sameSeen(old.Seen, next.Seen) || old.Pinned != next.Pinned {
		b.signalMarks(address)
	}
}

func (b *Backend) dropMarks(address string) {
	b.cache.mu.Lock()
	_, known := b.cache.marks[address]
	delete(b.cache.marks, address)
	b.cache.mu.Unlock()
	if known {
		b.signalMarks(address)
	}
}

func (b *Backend) signalMarks(address string) {
	select {
	case b.changes <- address:
	default:
	}
}

func sameSeen(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameMarks(a, b herdr.Marks) bool {
	return a.Turn == b.Turn && sameSeen(a.Seen, b.Seen) && a.Pinned == b.Pinned && a.Word == b.Word && a.Error == b.Error
}

// MarkChanges reports terminals whose marks changed outside this backend's
// own writes: another Herdr client wrote a label, or a turn ended. It only
// sees changes while RunMarks runs.
func (b *Backend) MarkChanges() <-chan string { return b.changes }

// AllMarks returns the marks of every live local terminal, by address.
func (b *Backend) AllMarks(ctx context.Context) (map[string]herdr.Marks, error) {
	b.cache.mu.Lock()
	if b.cache.live {
		out := make(map[string]herdr.Marks, len(b.cache.marks))
		for address, m := range b.cache.marks {
			out[address] = m
		}
		b.cache.mu.Unlock()
		return out, nil
	}
	b.cache.mu.Unlock()
	list, err := b.client.List(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]herdr.Marks, len(list.Terminals))
	for _, t := range list.Terminals {
		if isPane(t) {
			out[t.Address] = marksOf(t)
		}
	}
	return out, nil
}

// currentMarks is the marks a write decides on: the cache while RunMarks
// is live, else a read.
func (b *Backend) currentMarks(ctx context.Context, address string) (herdr.Marks, error) {
	if isLocal(address) {
		b.cache.mu.Lock()
		m, ok := b.cache.marks[address]
		live := b.cache.live
		b.cache.mu.Unlock()
		if live && ok {
			return m, nil
		}
	}
	t, err := b.pane(ctx, address)
	if err != nil {
		return herdr.Marks{}, err
	}
	return marksOf(t), nil
}

// pane reads a live local terminal; any other address is no pane.
func (b *Backend) pane(ctx context.Context, address string) (hc.Terminal, error) {
	if !isLocal(address) {
		return hc.Terminal{}, fmt.Errorf("%w: %s is not a local terminal", herdr.ErrPaneNotFound, address)
	}
	t, err := b.client.Get(ctx, address)
	if err != nil {
		return hc.Terminal{}, translate(err)
	}
	if !isPane(t) {
		return hc.Terminal{}, fmt.Errorf("%w: terminal %s is not live", herdr.ErrPaneNotFound, address)
	}
	return t, nil
}

// MarkSeen raises herdr.seen to min(turn, the terminal's turn), only when
// that is above the label or the label is missing. It writes only that key.
func (b *Backend) MarkSeen(ctx context.Context, paneID string, turn uint64) (herdr.Marks, error) {
	return b.writeMarks(ctx, paneID, func(m herdr.Marks) (map[string]string, bool) {
		seen, write := m.SeenWrite(turn)
		return map[string]string{labelSeen: strconv.FormatUint(seen, 10)}, write
	})
}

// MarkUnread writes herdr.seen = turn - 1, the one write that lowers it.
func (b *Backend) MarkUnread(ctx context.Context, paneID string) (herdr.Marks, error) {
	return b.writeMarks(ctx, paneID, func(m herdr.Marks) (map[string]string, bool) {
		seen, write := m.UnreadWrite()
		return map[string]string{labelSeen: strconv.FormatUint(seen, 10)}, write
	})
}

// SetPinned writes herdr.pinned=1, or an empty value to remove it.
func (b *Backend) SetPinned(ctx context.Context, paneID string, pinned bool) (herdr.Marks, error) {
	return b.writeMarks(ctx, paneID, func(m herdr.Marks) (map[string]string, bool) {
		value := ""
		if pinned {
			value = pinnedValue
		}
		return map[string]string{labelPinned: value}, m.Pinned != pinned
	})
}

// writeMarks runs one label write: decide from the current marks, write
// only the keys that change, and cache the answer. Writes are serialised so
// two raise-only writes cannot cross; the cache is set before the call, so
// the labels event that echoes it is not taken for another client's write.
func (b *Backend) writeMarks(ctx context.Context, address string, decide func(herdr.Marks) (map[string]string, bool)) (herdr.Marks, error) {
	b.markWrite.Lock()
	defer b.markWrite.Unlock()
	current, err := b.currentMarks(ctx, address)
	if err != nil {
		return herdr.Marks{}, err
	}
	labels, write := decide(current)
	if !write {
		return current, nil
	}
	expected := current
	if v, ok := labels[labelSeen]; ok {
		n, _ := strconv.ParseUint(v, 10, 64)
		expected.Seen = &n
	}
	if v, ok := labels[labelPinned]; ok {
		expected.Pinned = v != ""
	}
	echoing := b.expectEcho(address, echo{seen: expected.Seen, pinned: expected.Pinned})
	b.updateMarksQuiet(address, expected)
	t, err := b.client.Label(ctx, address, labels)
	b.logEvent("herdr.label", address, map[string]any{"labels": labels, "error": errString(err)})
	if err != nil {
		if echoing {
			b.cancelEcho(address)
		}
		b.updateMarksQuiet(address, current)
		return herdr.Marks{}, translate(err)
	}
	m := marksOf(t)
	b.updateMarksQuiet(address, m)
	return m, nil
}

// updateMarksQuiet sets the cached marks of a terminal the cache knows,
// without telling MarkChanges: the caller's own write.
func (b *Backend) updateMarksQuiet(address string, m herdr.Marks) {
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	if _, ok := b.cache.marks[address]; ok {
		b.cache.marks[address] = m
	}
}

// startedSeen makes a terminal sheepdog starts an agent in count as seen up
// to now: it takes herdr.seen = its turn when the label is missing, so the
// agent is not unread before its first turn. A terminal that has the label
// keeps it.
func (b *Backend) startedSeen(ctx context.Context, address string) {
	// The write logs itself; a terminal that is gone has nothing to mark.
	if m, err := b.currentMarks(ctx, address); err == nil && m.Seen == nil {
		_, _ = b.MarkSeen(ctx, address, m.Turn)
	}
}
