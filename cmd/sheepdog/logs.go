package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
)

func printLogsUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog logs [-f] [-n N] [--config PATH]")
	fprintln(w, "")
	fprintln(w, "Prints the end of the runtime log, <data_dir>/logs/sheepdog.log")
	fprintln(w, "(~/.local/share/sheepdog/logs/sheepdog.log by default). Secrets are redacted")
	fprintln(w, "before they are written. Rotated at log.max_size_mb into sheepdog.log.1 ….")
	fprintln(w, "")
	fprintln(w, "  -n N   lines to print (default 100)")
	fprintln(w, "  -f     keep printing new lines until Ctrl-C")
}

// followPoll is how often logs -f checks the file.
var followPoll = 250 * time.Millisecond

// runLogs implements `sheepdog logs`.
func runLogs(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printLogsUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	n := fs.Int("n", 100, "lines")
	follow := fs.Bool("f", false, "follow")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	l, code, ok := loadLifecycle(ctx, "logs", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	path := daemon.LogFile(l.cfg.DataDir)
	if _, err := os.Stat(path); err != nil {
		fprintf(stderr, "No log yet: %s does not exist, so sheepdog has not run here yet.\n", path)
		if old := oldServiceLog(); old != "" {
			fprintf(stderr, "An older sheepdog wrote its log to %s.\n", old)
		}
		if !*follow {
			fprintln(stderr, "Start it with: sheepdog start")
			return 0
		}
		fprintln(stderr, "Waiting for it to appear (Ctrl-C to stop)…")
	} else {
		_, _ = stdout.Write(tailLines(path, *n))
	}
	if stdio := daemon.StdioFile(l.cfg.DataDir); fileSize(stdio) > 0 && !*follow {
		fprintf(stderr, "\n(the daemon also printed output outside the log, e.g. a crash: %s)\n", stdio)
	}
	if !*follow {
		return 0
	}
	followFile(ctx, path, stdout)
	return 0
}

// oldServiceLog is the launchd agent's log of sheepdog versions before
// the runtime log, when it exists.
func oldServiceLog() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, "Library", "Logs", "sheepdog", "serve.log")
	if fileSize(p) > 0 {
		return p
	}
	return ""
}

// tailLines is the last n lines of path (read from at most the last
// 4 MB).
func tailLines(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	const window = 4 << 20
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := st.Size() - window
	if off < 0 {
		off = 0
	}
	b := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
		return nil
	}
	if n <= 0 {
		return nil
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // a partial first line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return bytes.Join(lines, nil)
}

// followFile prints what is appended to path until ctx ends, starting at
// its current end; a rotation (the file replaced or shrunk) starts over
// at the new file's beginning.
func followFile(ctx context.Context, path string, w io.Writer) {
	var off int64
	var cur os.FileInfo
	if st, err := os.Stat(path); err == nil {
		off, cur = st.Size(), st
	}
	buf := make([]byte, 64<<10)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(followPoll):
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		if cur == nil || !os.SameFile(cur, st) || st.Size() < off {
			off, cur = 0, st
		}
		if st.Size() == off {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		for {
			k, err := f.ReadAt(buf, off)
			if k > 0 {
				_, _ = w.Write(buf[:k])
				off += int64(k)
			}
			if err != nil || k == 0 {
				break
			}
		}
		_ = f.Close()
	}
}
