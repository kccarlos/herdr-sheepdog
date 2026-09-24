// Package workerlog reads a worker's own session transcript — the JSONL
// file Claude Code or Codex writes as it works — so the brain can hear
// what the worker wrote for a human instead of relaying its screen
// (docs/iteration-2.md WP4, docs/roles.md "Worker history").
//
// The session comes from herdr's agent_session (internal/herdr
// AgentSession, kept on the state row). Files can be many megabytes, so
// a Reader remembers a byte offset per pane and every Read scans only
// the lines appended since the previous one; a first read, or one that
// fell far behind, starts at a tail window instead of the top. Nothing
// here is ever handed to a model whole: a Digest carries the last
// assistant message, the instructions and a tool/file summary, and the
// caller caps and scrubs them.
//
// Everything read is data, never instructions: the brain sees it inside
// provenance delimiters (internal/brain wrapWorker).
package workerlog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Reasons a session cannot be read (the reason of the supervisor's
// worker.session_missing event).
const (
	// ReasonNotReported: herdr has not reported a session for the pane.
	ReasonNotReported = "not_reported"
	// ReasonUnsupported: the agent kind keeps no transcript this package
	// can read (agy).
	ReasonUnsupported = "unsupported_kind"
	// ReasonNotFound: no transcript file matches the session.
	ReasonNotFound = "not_found"
	// ReasonUnreadable: the file exists but could not be opened or read.
	ReasonUnreadable = "unreadable"
)

// MissingError is why a session's transcript could not be read.
type MissingError struct {
	Reason string
	Err    error
}

func (e *MissingError) Error() string {
	if e.Err != nil {
		return "workerlog: " + e.Reason + ": " + e.Err.Error()
	}
	return "workerlog: " + e.Reason
}

func (e *MissingError) Unwrap() error { return e.Err }

// Reason returns the MissingError reason of err, or ReasonUnreadable for
// any other error ("" for nil).
func Reason(err error) string {
	if err == nil {
		return ""
	}
	var m *MissingError
	if errors.As(err, &m) {
		return m.Reason
	}
	return ReasonUnreadable
}

func missing(reason string, err error) error { return &MissingError{Reason: reason, Err: err} }

// Session identifies a worker's session: herdr's agent_session plus the
// pane's agent kind and working directory.
type Session struct {
	// Agent is the pane's kind label (claude, codex, agy).
	Agent string
	// Source is the reporting integration ("herdr:claude").
	Source string
	// Kind is "id" or "path" (herdr.SessionKindID / SessionKindPath).
	Kind  string
	Value string
	// Cwd is the pane's working directory (Claude keeps its transcripts
	// in a directory named after it); may be empty.
	Cwd string
}

// Format is a transcript format.
type Format string

// The transcript formats.
const (
	FormatClaude Format = "claude"
	FormatCodex  Format = "codex"
)

// format picks the transcript format from the reporting integration,
// falling back to the pane's kind label.
func (s Session) format() Format {
	for _, v := range []string{s.Source, s.Agent} {
		v = strings.ToLower(v)
		switch {
		case strings.Contains(v, "claude"):
			return FormatClaude
		case strings.Contains(v, "codex"):
			return FormatCodex
		}
	}
	return ""
}

// Limits of one read.
const (
	// TailWindow is where a first read starts from the end of a file, and
	// where a read that fell further behind than MaxBacklog resumes: the
	// last turns are what matters, never the whole history.
	TailWindow = 2 << 20
	// MaxBacklog is the most a read scans past its offset before it skips
	// to the tail window.
	MaxBacklog = 8 << 20
	// MaxLine is the longest line parsed; a longer one (a huge tool
	// result) is skipped unread.
	MaxLine = 4 << 20
	// MaxFiles is how many touched files a Digest keeps.
	MaxFiles = 10
	// MaxInstructions is how many instructions a Digest keeps (the last).
	MaxInstructions = 5
	// maxText caps each message kept in memory before the caller clips it.
	maxText = 8 << 10
)

// Digest is what one Read found in the lines appended since the
// previous one.
type Digest struct {
	Format Format
	// Path is the transcript file.
	Path string
	// Bytes is how much was scanned; Skipped how much was jumped over to
	// reach the tail window.
	Bytes, Skipped int64
	// LastAssistant is the worker's last message for a human, from this
	// read or, when this read has none, the previous one.
	LastAssistant string
	// Fresh is set when LastAssistant was written since the previous read.
	Fresh bool
	// Instructions are the user messages it received in this read, oldest
	// first, at most MaxInstructions.
	Instructions []string
	// Tools counts tool calls by name, and Files lists the base names of
	// edited files (first touched first, at most MaxFiles), both since the
	// last instruction in this read: what the worker did for it.
	Tools map[string]int
	Files []string
}

// Instruction is the last instruction of the read, or "".
func (d Digest) Instruction() string {
	if len(d.Instructions) == 0 {
		return ""
	}
	return d.Instructions[len(d.Instructions)-1]
}

// Merge folds next, a later read of the same pane, into d: together they
// describe everything since the read before d. A new instruction in next
// starts the work over, as within one read.
func (d *Digest) Merge(next Digest) {
	d.Bytes += next.Bytes
	d.Skipped += next.Skipped
	if next.Path != "" {
		d.Path = next.Path
	}
	if next.Fresh || d.LastAssistant == "" {
		d.LastAssistant = next.LastAssistant
	}
	d.Fresh = d.Fresh || next.Fresh
	if len(next.Instructions) > 0 {
		d.Instructions = append(d.Instructions, next.Instructions...)
		if over := len(d.Instructions) - MaxInstructions; over > 0 {
			d.Instructions = d.Instructions[over:]
		}
		d.Tools, d.Files = nil, nil
	}
	for name, n := range next.Tools {
		if d.Tools == nil {
			d.Tools = map[string]int{}
		}
		d.Tools[name] += n
	}
	for _, f := range next.Files {
		if len(d.Files) >= MaxFiles {
			break
		}
		seen := false
		for _, g := range d.Files {
			seen = seen || g == f
		}
		if !seen {
			d.Files = append(d.Files, f)
		}
	}
}

// Options locates the transcript roots; empty fields take the defaults.
type Options struct {
	// ClaudeProjects is Claude Code's projects directory (default
	// $CLAUDE_CONFIG_DIR/projects or ~/.claude/projects).
	ClaudeProjects string
	// CodexSessions is Codex's sessions directory (default
	// $CODEX_HOME/sessions or ~/.codex/sessions).
	CodexSessions string
}

// Reader reads transcripts incrementally, one cursor per pane. It is
// safe for concurrent use.
type Reader struct {
	opts Options

	mu      sync.Mutex
	cursors map[string]*cursor
}

type cursor struct {
	session       string // Session.Value the offset belongs to
	path          string
	offset        int64
	lastAssistant string
}

// New returns a Reader over opts' roots.
func New(opts Options) *Reader {
	home, _ := os.UserHomeDir()
	if opts.ClaudeProjects == "" {
		base := os.Getenv("CLAUDE_CONFIG_DIR")
		if base == "" {
			base = filepath.Join(home, ".claude")
		}
		opts.ClaudeProjects = filepath.Join(base, "projects")
	}
	if opts.CodexSessions == "" {
		base := os.Getenv("CODEX_HOME")
		if base == "" {
			base = filepath.Join(home, ".codex")
		}
		opts.CodexSessions = filepath.Join(base, "sessions")
	}
	return &Reader{opts: opts, cursors: map[string]*cursor{}}
}

// Locate returns the transcript file of sess.
//
// Claude Code: the reported path when Kind is "path"; else
// <projects>/<slug>/<id>.jsonl, the slug being the working directory
// with "/" and "." replaced by "-" (verified against ~/.claude/projects
// on 2026-09-24), falling back to any project directory holding
// <id>.jsonl (ids are UUIDs) when the cwd is unknown or was renamed.
// Codex: <sessions>/YYYY/MM/DD/rollout-<timestamp>-<id>.jsonl.
func (r *Reader) Locate(sess Session) (string, error) {
	if sess.Value == "" {
		return "", missing(ReasonNotReported, nil)
	}
	f := sess.format()
	if f == "" {
		return "", missing(ReasonUnsupported, fmt.Errorf("agent %q", sess.Agent))
	}
	if sess.Kind == "path" {
		if fileExists(sess.Value) {
			return sess.Value, nil
		}
		return "", missing(ReasonNotFound, nil)
	}
	if strings.ContainsAny(sess.Value, `/\*?[`) {
		return "", missing(ReasonNotFound, fmt.Errorf("session id %q is not a plain id", sess.Value))
	}
	switch f {
	case FormatClaude:
		if sess.Cwd != "" {
			p := filepath.Join(r.opts.ClaudeProjects, ClaudeSlug(sess.Cwd), sess.Value+".jsonl")
			if fileExists(p) {
				return p, nil
			}
		}
		if m, _ := filepath.Glob(filepath.Join(r.opts.ClaudeProjects, "*", sess.Value+".jsonl")); len(m) > 0 {
			return m[0], nil
		}
	case FormatCodex:
		if m, _ := filepath.Glob(filepath.Join(r.opts.CodexSessions, "*", "*", "*", "*-"+sess.Value+".jsonl")); len(m) > 0 {
			return m[len(m)-1], nil
		}
	}
	return "", missing(ReasonNotFound, nil)
}

// ClaudeSlug is the directory name Claude Code keeps a working
// directory's sessions under: every "/" and "." becomes "-"
// ("/Users/kc/x/.claude/wt" -> "-Users-kc-x--claude-wt").
func ClaudeSlug(cwd string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Forget drops the pane's cursor (the pane closed).
func (r *Reader) Forget(pane string) {
	r.mu.Lock()
	delete(r.cursors, pane)
	r.mu.Unlock()
}

// Read scans the lines of sess's transcript appended since the pane's
// previous Read and summarises them. A session other than the one the
// cursor was on, or a file that shrank, starts over from the tail
// window. A line not yet terminated by a newline is left for the next
// read.
func (r *Reader) Read(pane string, sess Session) (Digest, error) {
	path, err := r.Locate(sess)
	if err != nil {
		return Digest{}, err
	}
	fh, err := os.Open(path)
	if err != nil {
		return Digest{}, missing(ReasonUnreadable, err)
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return Digest{}, missing(ReasonUnreadable, err)
	}
	size := st.Size()

	r.mu.Lock()
	cur := r.cursors[pane]
	if cur == nil || cur.session != sess.Value || cur.path != path || cur.offset > size {
		cur = &cursor{session: sess.Value, path: path}
		r.cursors[pane] = cur
	}
	start, lastAssistant := cur.offset, cur.lastAssistant
	r.mu.Unlock()

	d := Digest{Format: sess.format(), Path: path}
	align := false
	fresh := start == 0 && size > TailWindow
	if fresh || size-start > MaxBacklog {
		skipTo := size - TailWindow
		d.Skipped = skipTo - start
		start, align = skipTo, true
	}
	acc := &accumulator{format: d.Format}
	consumed, err := scan(io.NewSectionReader(fh, start, size-start), align, acc.line)
	if err != nil {
		return Digest{}, missing(ReasonUnreadable, err)
	}
	d.Bytes = consumed
	acc.fill(&d)
	d.Fresh = d.LastAssistant != ""
	if d.LastAssistant == "" {
		d.LastAssistant = lastAssistant
	}

	r.mu.Lock()
	if r.cursors[pane] == cur {
		cur.offset = start + consumed
		cur.lastAssistant = d.LastAssistant
	}
	r.mu.Unlock()
	return d, nil
}

// scan calls fn for every complete line of rd and returns how many bytes
// the complete lines took; a trailing line with no newline yet is not
// counted, so the next read parses it whole. With align, the first line
// (the tail of one cut by a seek) is counted but not parsed. Lines longer
// than MaxLine are counted and skipped, holding at most MaxLine bytes.
func scan(rd io.Reader, align bool, fn func([]byte)) (int64, error) {
	br := bufio.NewReaderSize(rd, 64<<10)
	var consumed, n int64
	var line []byte
	for {
		part, err := br.ReadSlice('\n')
		n += int64(len(part))
		if n <= MaxLine {
			line = append(line, part...)
		}
		switch {
		case err == nil:
			if !align && n <= MaxLine {
				fn(bytes.TrimSpace(line))
			}
			align = false
			consumed += n
			line, n = line[:0], 0
		case errors.Is(err, bufio.ErrBufferFull):
			// The line goes on.
		case errors.Is(err, io.EOF):
			return consumed, nil
		default:
			return consumed, err
		}
	}
}
