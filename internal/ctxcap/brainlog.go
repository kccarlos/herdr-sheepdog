package ctxcap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// BrainSteps is how many orchestrator steps the log keeps in memory (and loads
// back from the previous run at startup).
const BrainSteps = 200

// RequestsSuffix names a run's request log: <data_dir>/brain/<run>.requests.jsonl.
const RequestsSuffix = ".requests.jsonl"

// maxLine bounds one request line read back from disk.
const maxLine = 64 << 20

// Usage is what the provider reported for a step's call, when it did.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
}

// BrainStep is one model call of the orchestrator: the exact request, rendered
// and mapped, with the turn it belongs to.
type BrainStep struct {
	// Step numbers every model call of the daemon's runs, 1-based and
	// increasing across restarts (the log continues from what it loaded).
	Step int `json:"step"`
	// Run is the daemon run (brain.Config.RunID) and Segment the
	// transcript segment (it changes on a "New conversation").
	Run     string `json:"run"`
	Segment string `json:"segment"`
	// Turn is the orchestrator turn, TurnStep the model call within it.
	Turn     int       `json:"turn"`
	TurnStep int       `json:"turn_step"`
	Kind     string    `json:"kind"`
	Trigger  string    `json:"trigger,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	TS       time.Time `json:"ts"`
	// Format is the body's shape: "gemini" ({model, contents, config} as
	// the daemon built it for the SDK) or "openai" (the exact chat
	// completions request).
	Format string `json:"format"`
	// Forced marks an answer-only step (no tools).
	Forced bool `json:"forced,omitempty"`
	// WindowTokens is the budget (brain.window_tokens): it bounds the
	// transcript window; the system prompt and tools come on top.
	WindowTokens int `json:"window_tokens"`
	// Tokens is the request's estimated size (4 characters a token);
	// Usage what the provider counted, when the call returned.
	Tokens int    `json:"tokens"`
	Usage  *Usage `json:"usage,omitempty"`
	Error  string `json:"error,omitempty"`
	DurMS  int64  `json:"dur_ms"`
	Snapshot
}

// Meta returns the step without its text, sections and body.
func (s BrainStep) Meta() BrainStep {
	s.Snapshot = Snapshot{}
	return s
}

// BrainLog keeps the orchestrator's last BrainSteps requests in memory and
// appends every one to its run's requests file. Safe for concurrent use.
type BrainLog struct {
	mu     sync.Mutex
	steps  []BrainStep // oldest first, at most BrainSteps
	next   int
	f      *os.File
	path   string
	err    error
	notify func(BrainStep)
}

// OpenBrainLog opens dir/<run>.requests.jsonl for appending and loads
// the last BrainSteps steps of the newest earlier run's file (so a
// restart keeps them); dir "" keeps the log in memory only. notify, when
// set, is called outside the lock with every recorded step's metadata.
func OpenBrainLog(dir, run string, notify func(BrainStep)) (*BrainLog, error) {
	l := &BrainLog{next: 1, notify: notify}
	if dir == "" {
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ctxcap: %w", err)
	}
	l.path = filepath.Join(dir, run+RequestsSuffix)
	// This run's own file first (a reopened run id), else the newest
	// earlier one.
	for _, p := range previousFiles(dir, l.path) {
		steps, err := readTail(p, BrainSteps)
		if err != nil || len(steps) == 0 {
			continue
		}
		l.steps = steps
		l.next = steps[len(steps)-1].Step + 1
		break
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ctxcap: %w", err)
	}
	l.f = f
	return l, nil
}

// previousFiles lists the request files to load from, best first: own
// (when it exists), then every other one, newest name first (run ids are
// start times).
func previousFiles(dir, own string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*"+RequestsSuffix))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	var out []string
	if _, err := os.Stat(own); err == nil {
		out = append(out, own)
	}
	for _, m := range matches {
		if m != own {
			out = append(out, m)
		}
	}
	return out
}

// readTail decodes the last n steps of a requests file.
func readTail(path string, n int) ([]BrainStep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), maxLine)
	var lines [][]byte
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		lines = append(lines, line)
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if err := sc.Err(); err != nil && len(lines) == 0 {
		return nil, err
	}
	out := make([]BrainStep, 0, len(lines))
	for _, line := range lines {
		var s BrainStep
		if json.Unmarshal(line, &s) == nil && s.Step > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

// Path is the file the log appends to ("" in memory).
func (l *BrainLog) Path() string { return l.path }

// Record numbers s, keeps it and appends it to the file; it returns the
// numbered step. A write error is kept (Err) and does not stop the log.
func (l *BrainLog) Record(s BrainStep) BrainStep {
	l.mu.Lock()
	s.Step = l.next
	l.next++
	l.steps = append(l.steps, s)
	if len(l.steps) > BrainSteps {
		l.steps = append([]BrainStep(nil), l.steps[len(l.steps)-BrainSteps:]...)
	}
	if l.f != nil {
		line, err := json.Marshal(s)
		if err == nil {
			_, err = l.f.Write(append(line, '\n'))
		}
		if err != nil && l.err == nil {
			l.err = err
		}
	}
	notify := l.notify
	l.mu.Unlock()
	if notify != nil {
		notify(s.Meta())
	}
	return s
}

// Err is the first write error, if any.
func (l *BrainLog) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// ErrNoStep is returned by Get for a step the log does not hold.
var ErrNoStep = errors.New("ctxcap: no such step")

// Get returns step n (n <= 0: the latest) and the one before it in the
// log (ok false when there is none).
func (l *BrainLog) Get(n int) (cur, prev BrainStep, hasPrev bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.steps) == 0 {
		return BrainStep{}, BrainStep{}, false, ErrNoStep
	}
	i := len(l.steps) - 1
	if n > 0 {
		i = sort.Search(len(l.steps), func(i int) bool { return l.steps[i].Step >= n })
		if i == len(l.steps) || l.steps[i].Step != n {
			return BrainStep{}, BrainStep{}, false, ErrNoStep
		}
	}
	if i > 0 {
		return l.steps[i], l.steps[i-1], true, nil
	}
	return l.steps[i], BrainStep{}, false, nil
}

// Steps returns the metadata of every step held, oldest first.
func (l *BrainLog) Steps() []BrainStep {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]BrainStep, len(l.steps))
	for i, s := range l.steps {
		out[i] = s.Meta()
	}
	return out
}

// Len is how many steps are held.
func (l *BrainLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.steps)
}

// Close closes the file.
func (l *BrainLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// DiffBrain is the diff of cur against prev (hasPrev false: everything
// is added). A different run or transcript segment between them means a
// restart or a reset came between: what is gone was dropped by it.
func DiffBrain(cur, prev BrainStep, hasPrev bool) Diff {
	if !hasPrev {
		return DiffSnapshots(0, Snapshot{}, cur.Snapshot, false)
	}
	reset := prev.Run != cur.Run || prev.Segment != cur.Segment
	return DiffSnapshots(prev.Step, prev.Snapshot, cur.Snapshot, reset)
}
