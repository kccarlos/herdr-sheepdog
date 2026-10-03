package workerlog

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The worker's session title (docs/roles.md "Agent names"): the short
// name its own CLI gives the conversation, which the daemon reads as a
// fact so it can tell two agents of one kind in one project apart by
// ear ("README Claude"). Read-only, like everything here.
//
// Claude Code appends {"type":"ai-title","aiTitle":…} lines to the
// session transcript as it renames the conversation, and
// {"type":"custom-title","customTitle":…} when the user renamed it
// (/rename); both recur every few dozen lines, so the tail window holds
// the current one. A custom title wins over an AI one.
//
// Codex keeps thread names out of the rollout file: it appends
// {"id":…,"thread_name":…} lines to session_index.jsonl next to its
// sessions directory, the last line for an id being the current name
// (verified against ~/.codex on 2026-10-03).

// maxIndexBytes is how much of the end of Codex's session index a title
// read scans (the index holds one short line per rename).
const maxIndexBytes = 4 << 20

// Title returns the worker's current session title, "" when it has
// none yet. The errors are those of Locate (and ReasonUnreadable).
func (r *Reader) Title(sess Session) (string, error) {
	switch sess.format() {
	case FormatCodex:
		return r.codexTitle(sess)
	case FormatClaude:
		return r.claudeTitle(sess)
	}
	if sess.Value == "" {
		return "", missing(ReasonNotReported, nil)
	}
	return "", missing(ReasonUnsupported, nil)
}

// claudeTitle scans the transcript's tail window for the last custom
// and AI titles.
func (r *Reader) claudeTitle(sess Session) (string, error) {
	path, err := r.Locate(sess)
	if err != nil {
		return "", err
	}
	var custom, ai string
	err = scanTail(path, TailWindow, func(line []byte) {
		if !bytes.Contains(line, []byte(`-title"`)) {
			return
		}
		var l struct {
			Type        string `json:"type"`
			AITitle     string `json:"aiTitle"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &l) != nil {
			return
		}
		switch {
		case l.Type == "custom-title" && strings.TrimSpace(l.CustomTitle) != "":
			custom = strings.TrimSpace(l.CustomTitle)
		case l.Type == "ai-title" && strings.TrimSpace(l.AITitle) != "":
			ai = strings.TrimSpace(l.AITitle)
		}
	})
	if err != nil {
		return "", err
	}
	if custom != "" {
		return custom, nil
	}
	return ai, nil
}

// codexTitle reads the session's last thread name from the index.
func (r *Reader) codexTitle(sess Session) (string, error) {
	if sess.Value == "" {
		return "", missing(ReasonNotReported, nil)
	}
	id := sess.Value
	if sess.Kind == "path" {
		// rollout-<timestamp>-<id>.jsonl: the id is a UUID, 36
		// characters before the extension.
		base := strings.TrimSuffix(filepath.Base(id), ".jsonl")
		if len(base) < 36 {
			return "", missing(ReasonNotFound, nil)
		}
		id = base[len(base)-36:]
	}
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return "", missing(ReasonNotFound, nil)
	}
	if !fileExists(r.opts.CodexIndex) {
		return "", missing(ReasonNotFound, nil)
	}
	var title string
	needle := []byte(`"` + id + `"`)
	err := scanTail(r.opts.CodexIndex, maxIndexBytes, func(line []byte) {
		if !bytes.Contains(line, needle) {
			return
		}
		var l struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &l) == nil && l.ID == id && strings.TrimSpace(l.ThreadName) != "" {
			title = strings.TrimSpace(l.ThreadName)
		}
	})
	return title, err
}

// scanTail calls fn for every complete line of the last window bytes of
// path (the first, cut line of a window that starts mid-file skipped).
func scanTail(path string, window int64, fn func([]byte)) error {
	fh, err := os.Open(path)
	if err != nil {
		return missing(ReasonUnreadable, err)
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return missing(ReasonUnreadable, err)
	}
	var start int64
	if st.Size() > window {
		start = st.Size() - window
	}
	data, err := io.ReadAll(io.NewSectionReader(fh, start, st.Size()-start))
	if err != nil {
		return missing(ReasonUnreadable, err)
	}
	lines := bytes.Split(data, []byte("\n"))
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	for _, l := range lines {
		if l = bytes.TrimSpace(l); len(l) > 0 && l[0] == '{' {
			fn(l)
		}
	}
	return nil
}
