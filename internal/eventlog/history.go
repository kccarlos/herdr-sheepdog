package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Reading the log back by id, across day files and daemon restarts: an
// SSE client resuming from Last-Event-ID (Since) and the conversation
// history the web layer rebuilds (Days, ReadDay).

// DefaultReplayMax bounds one Since call: a client that missed more than
// this gets the newest DefaultReplayMax events after its id.
const DefaultReplayMax = 2000

// dayFile matches a day file's name.
var dayFile = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)

// listDays returns the days of dir's files, oldest first.
func listDays(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var days []string
	for _, e := range entries {
		if !e.IsDir() && dayFile.MatchString(e.Name()) {
			days = append(days, e.Name()[:len("2006-01-02")])
		}
	}
	sort.Strings(days)
	return days, nil
}

// readEvents reads JSONL events leniently: a malformed line (a torn write
// before a crash) is skipped rather than hiding everything after it. Ids
// are derived for lines written before ids existed, as Replay does.
func readEvents(r io.Reader) ([]Event, error) {
	var out []Event
	var last int64
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		ev.Seq = uint64(len(out) + 1)
		if ev.ID == 0 {
			ev.ID = nextID(last, ev.TS)
		} else {
			ev.encoded = append([]byte(nil), line...)
		}
		last = ev.ID
		out = append(out, ev)
	}
	return out, sc.Err()
}

// lastIDOnDisk is the id of the newest line in dir (0 when there is none).
func lastIDOnDisk(dir string) int64 {
	days, _ := listDays(dir)
	for i := len(days) - 1; i >= 0; i-- {
		if id := lastIDInFile(filepath.Join(dir, days[i]+".jsonl")); id > 0 {
			return id
		}
	}
	return 0
}

// lastIDInFile reads the tail of path for its last line's id.
func lastIDInFile(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	const tail = 256 << 10
	off := max(0, st.Size()-tail)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return 0
	}
	if off > 0 {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return 0
		}
		buf = buf[i+1:]
	}
	evs, _ := readEvents(bytes.NewReader(buf))
	if len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].ID
}

// Days lists the days ("2006-01-02") that have a file, oldest first; nil
// for an in-memory log.
func (l *Log) Days() ([]string, error) {
	if l.dir == "" {
		return nil, nil
	}
	return listDays(l.dir)
}

// ReadDay reads one day's file back, oldest first, after flushing what is
// buffered so the current day is complete. A missing day is empty.
func (l *Log) ReadDay(day string) ([]Event, error) {
	if l.dir == "" {
		return nil, nil
	}
	_ = l.Flush()
	f, err := os.Open(filepath.Join(l.dir, day+".jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readEvents(f)
}

// Since returns the events with an id greater than after, oldest first:
// at most maxN (DefaultReplayMax when maxN <= 0), the newest ones when
// more qualify, with truncated set. The in-memory ring answers when it
// reaches back to after; otherwise the day files from after's day on do
// (an in-memory log has only the ring).
func (l *Log) Since(after int64, maxN int) (events []Event, truncated bool, err error) {
	if maxN <= 0 {
		maxN = DefaultReplayMax
	}
	l.mu.Lock()
	covered := l.ringLen > 0 && l.ring[l.ringHead].ID <= after
	if covered || l.dir == "" {
		size := len(l.ring)
		for i := 0; i < l.ringLen; i++ {
			if ev := l.ring[(l.ringHead+i)%size]; ev.ID > after {
				events = append(events, ev)
			}
		}
		l.mu.Unlock()
		if len(events) > maxN {
			events, truncated = events[len(events)-maxN:], true
		}
		return events, truncated, nil
	}
	l.mu.Unlock()
	days, err := l.Days()
	if err != nil {
		return nil, false, err
	}
	// A day early: an id can run slightly ahead of its line's ts (max(now,
	// last+1)), so a line from just before midnight may carry an id past it.
	from := dayOf(time.UnixMicro(after).Add(-24 * time.Hour))
	for _, day := range days {
		if day < from {
			continue
		}
		evs, err := l.ReadDay(day)
		if err != nil {
			return nil, false, err
		}
		for _, ev := range evs {
			if ev.ID > after {
				events = append(events, ev)
			}
		}
		if len(events) > 2*maxN {
			events, truncated = append([]Event(nil), events[len(events)-maxN:]...), true
		}
	}
	if len(events) > maxN {
		events, truncated = events[len(events)-maxN:], true
	}
	return events, truncated, nil
}
