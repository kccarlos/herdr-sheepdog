package factory

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ProjectLog reads the last n lines (all when n <= 0) of a project's
// log.jsonl: folder's project, or the one started project when folder is
// empty. A torn last line (an append that did not finish) is skipped.
func ProjectLog(ctx context.Context, paths Paths, folder string, n int) ([]LogLine, error) {
	id, _, err := (&Engine{Paths: paths}).findProjectLocked(ctx, folder)
	if err != nil {
		return nil, err
	}
	lines, err := readLogFile(paths.Log(id), false)
	if err != nil {
		return nil, err
	}
	return LogView(lines, n), nil
}

func readLogFile(path string, allowMissing bool) ([]LogLine, error) {
	if err := guardTestPath(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if allowMissing && os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var lines []LogLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			lines = append(lines, l)
		}
	}
	return lines, sc.Err()
}

var fullSHA = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// FormatLogLine is one log line for the owner: time, task, rule, what was
// asked, the decision and why (design.md 14). Full commit ids are cut to 7.
func FormatLogLine(l LogLine) string {
	s := fmt.Sprintf("%s  %-4s %-11s asked: %s | decision: %s | why: %s",
		l.At.Format("2006-01-02 15:04:05"), cmp.Or(l.Task, "-"), l.Rule,
		cmp.Or(strings.Join(l.Asked, ", "), "-"), cmp.Or(l.Decision, "-"), cmp.Or(l.Why, "-"))
	return fullSHA.ReplaceAllStringFunc(s, func(m string) string { return m[:7] })
}
