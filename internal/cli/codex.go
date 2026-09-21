package cli

import (
	"encoding/json"
	"strings"
)

// codexEvent is the subset of one `codex exec --json` JSONL line Runner
// needs (verified 2026-09-21, codex-cli 0.154.0): `thread.started` carries
// the thread id a later `codex exec resume` takes, and the answer is the
// text of the last `agent_message` item (docs/cli-query-tools.md §1).
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

// parseCodexJSONL extracts the answer and the thread id from codex's
// JSONL stdout. ok is false when no line parsed as an event, in which
// case the caller falls back to the raw text (a template without
// --json). Lines that do not parse — a boundary the output cap cut in
// half — are skipped, so the same function reads the kept head and the
// kept tail.
func parseCodexJSONL(stdout string) (answer, threadID string, ok bool) {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type == "" {
			continue
		}
		ok = true
		switch {
		case ev.Type == "thread.started" && ev.ThreadID != "":
			threadID = ev.ThreadID
		case ev.Item.Type == "agent_message" && ev.Item.Text != "":
			answer = ev.Item.Text
		}
	}
	return strings.TrimSpace(answer), threadID, ok
}
