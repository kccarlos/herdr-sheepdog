package eventlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Replay reads a JSONL event file back strictly (tests only; the
// daemon reads its files with the lenient readEvents). Blank lines are skipped; a
// malformed line is an error naming its number. Seq is assigned in file
// order starting at 1; a line with no id (written before ids existed)
// gets nextID of the previous line's.
func Replay(r io.Reader) ([]Event, error) {
	var out []Event
	var last int64
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return out, fmt.Errorf("eventlog: line %d: %w", n, err)
		}
		ev.Seq = uint64(len(out) + 1)
		if ev.ID == 0 {
			ev.ID = nextID(last, ev.TS)
			ev.encoded = nil // re-encoded with the derived id
		} else {
			ev.encoded = append([]byte(nil), line...)
		}
		last = ev.ID
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("eventlog: read: %w", err)
	}
	return out, nil
}
