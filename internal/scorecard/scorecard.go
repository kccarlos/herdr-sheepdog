// Package scorecard computes docs/iteration-2.md's "Scorecard metrics"
// from a recorded event log: latency (time to first word, time to
// answer, brain turn duration and prompt tokens), the ear-metrics
// quality of every spoken line (internal/earmetrics), repeat-ask rate,
// lost-utterance rate, the brain-line drop rate, Ctrl-C sends, "what does
// that mean"-class follow-ups and duplicate speech. It is read-only and
// measurement-only: nothing here changes daemon behaviour, and nothing
// here writes to the event log it reads.
//
// The event log (internal/eventlog, docs/architecture.md "Event types")
// is one JSONL file per UTC day under <data_dir>/events/. Its schema has
// drifted across days (fields added, a few removed) and, per that doc,
// "Every ... call's type" is the ground truth, not a fixed contract, so
// every decode in this package is best-effort: a missing or
// unexpectedly-shaped field decodes to its zero value rather than an
// error, and a malformed JSON line is skipped rather than aborting the
// read (mirrors internal/eventlog/history.go's readEvents).
package scorecard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// dayFile matches an event log day file's name, the same pattern
// internal/eventlog/history.go uses.
var dayFile = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)

// Load reads every day file in dir whose date falls in [from, to]
// (inclusive; an empty bound is open on that side, "" dates meaning
// YYYY-MM-DD), oldest day first, and returns their events concatenated in
// file order. A missing directory is treated as empty, not an error, so
// pointing --events-dir at a day that never happened is harmless.
func Load(dir, from, to string) ([]eventlog.Event, error) {
	days, err := listDays(dir)
	if err != nil {
		return nil, err
	}
	var out []eventlog.Event
	for _, day := range days {
		if from != "" && day < from {
			continue
		}
		if to != "" && day > to {
			continue
		}
		evs, err := readDayFile(filepath.Join(dir, day+".jsonl"))
		if err != nil {
			return nil, fmt.Errorf("scorecard: read %s: %w", day, err)
		}
		out = append(out, evs...)
	}
	return out, nil
}

// listDays returns the "YYYY-MM-DD" day stems of dir's day files, sorted
// oldest first. A directory that does not exist yields no days.
func listDays(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var days []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := dayFile.FindStringSubmatch(e.Name()); m != nil {
			days = append(days, m[1])
		}
	}
	sort.Strings(days)
	return days, nil
}

// readDayFile reads one JSONL file leniently: a line that fails to parse
// as an eventlog.Event (a torn write, a hand-edited fixture typo) is
// skipped rather than failing the whole read, matching
// internal/eventlog/history.go's readEvents. Blank lines are skipped.
func readDayFile(path string) ([]eventlog.Event, error) {
	f, err := os.Open(path) //nolint:gosec // path is built from a caller-supplied directory, not user input over a trust boundary
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []eventlog.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev eventlog.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

// decode unmarshals raw into a T, returning the zero value on any error
// (missing fields, a field of an unexpected type from an older day) so a
// single odd line never aborts a report. Callers should only read the
// fields they need from T; docs/architecture.md notes the same field name
// can carry a different JSON type across event kinds (e.g. "acted" is a
// count on brain.turn but a bool on brain.fallback), so a payload struct
// here is scoped to one event type, never shared.
func decode[T any](raw json.RawMessage) T {
	var v T
	if len(raw) == 0 {
		return v
	}
	_ = json.Unmarshal(raw, &v)
	return v
}

// Session is one daemon run's slice of the log: the events from a
// daemon.start (inclusive) up to the next one (exclusive), or to the end
// of the loaded range. Splitting by session (the --session flag) avoids
// averaging latency and ear metrics across restarts that have nothing to
// do with each other.
type Session struct {
	// Label identifies the session for the report: the daemon.start
	// event's timestamp (RFC3339), or "unsplit" for the whole range, or
	// "pre-daemon.start" for a leading run of events with no daemon.start
	// (a truncated day file, or a log that predates the id/daemon.start
	// convention, as testdata/e2e/2026-09-17-e2e.jsonl does).
	Label  string
	Events []eventlog.Event
}

// SplitSessions splits events at every daemon.start event.
func SplitSessions(events []eventlog.Event) []Session {
	var sessions []Session
	cur := -1 // index of the session being filled; -1 until one is opened
	for _, ev := range events {
		if ev.Source == "daemon" && ev.Type == "daemon.start" {
			sessions = append(sessions, Session{Label: ev.TS.Format("2006-01-02T15:04:05Z")})
			cur = len(sessions) - 1
		} else if cur == -1 {
			sessions = append(sessions, Session{Label: "pre-daemon.start"})
			cur = len(sessions) - 1
		}
		// Re-index sessions[cur] on every append (rather than caching a
		// *Session) since appending a new session above can reallocate
		// the backing array and strand a pointer into the old one.
		sessions[cur].Events = append(sessions[cur].Events, ev)
	}
	return sessions
}
