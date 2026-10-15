package logging

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RotatingFile is an append-only log file rotated by size: when a write
// would take it past MaxBytes, path becomes path.1 (path.1 becomes
// path.2, …, up to MaxFiles old files; the oldest is deleted) and a new
// path is started. Every line gets a UTC timestamp prefix. It is safe
// for concurrent use; write errors are swallowed (a full disk must not
// take the daemon down).
type RotatingFile struct {
	path     string
	maxBytes int64
	maxFiles int
	now      func() time.Time

	mu      sync.Mutex
	f       *os.File
	size    int64
	midLine bool
}

// OpenRotating opens (creating it and its directory) the log at path.
// maxBytes <= 0 means 10 MiB; maxFiles < 1 means 1 old file kept.
func OpenRotating(path string, maxBytes int64, maxFiles int) (*RotatingFile, error) {
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	if maxFiles < 1 {
		maxFiles = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	r := &RotatingFile{path: path, maxBytes: maxBytes, maxFiles: maxFiles, now: time.Now}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size, r.midLine = f, st.Size(), false
	return nil
}

// Path is the current log file.
func (r *RotatingFile) Path() string { return r.path }

// rotate shifts path.N-1 → path.N … path → path.1 and reopens path.
func (r *RotatingFile) rotate() {
	_ = r.f.Close()
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.maxFiles))
	for i := r.maxFiles - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	_ = os.Rename(r.path, r.path+".1")
	if err := r.open(); err != nil {
		r.f = nil
	}
}

// Write implements io.Writer, prefixing each new line with its time.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return len(p), nil
		}
	}
	wasMid := r.midLine
	var out bytes.Buffer
	stamp := r.now().UTC().Format("2006-01-02T15:04:05.000Z ")
	rest := p
	for len(rest) > 0 {
		if !r.midLine {
			out.WriteString(stamp)
		}
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			out.Write(rest)
			r.midLine = true
			break
		}
		out.Write(rest[:i+1])
		r.midLine = false
		rest = rest[i+1:]
	}
	if r.size > 0 && r.size+int64(out.Len()) > r.maxBytes && !wasMid {
		endMid := r.midLine
		r.rotate()
		if r.f == nil {
			return len(p), nil
		}
		r.midLine = endMid
	}
	n, _ := r.f.Write(out.Bytes())
	r.size += int64(n)
	return len(p), nil
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
