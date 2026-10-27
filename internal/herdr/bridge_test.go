package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The bridge tests never run herdr. The "herdr binary" is this test binary
// re-executed with SHEEPDOG_FAKE_HERDR_DIR set: TestMain then plays a
// script from that directory (fakeHerdr) instead of running the tests.

const fakeHerdrEnv = "SHEEPDOG_FAKE_HERDR_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeHerdrEnv); dir != "" {
		os.Exit(fakeHerdr(dir))
	}
	os.Exit(m.Run())
}

// fakeHerdr plays <dir>/script-<attempt>.jsonl (or script.jsonl): one
// directive per line.
//
//	{"emit": {...}}            print the object as a stdout line
//	{"replay": "path"}         print every line of a recorded fixture
//	{"wait": "terminal.x"}     read stdin until a line of that type
//	{"echo_input": true}       for each terminal.input read, emit a frame of its bytes; stop at release/EOF
//	{"stderr": "text"}         write to stderr
//	{"sleep_ms": n}
//	{"exit": n}
//
// It records its argv and HERDR_* environment in args-<attempt>.json and
// every stdin line in stdin-<attempt>.jsonl.
func fakeHerdr(dir string) int {
	attempt := 1
	for {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("args-%d.json", attempt)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			attempt++
			continue
		}
		var herdrEnv []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "HERDR_") {
				herdrEnv = append(herdrEnv, kv)
			}
		}
		_ = json.NewEncoder(f).Encode(map[string]any{"args": os.Args[1:], "env": herdrEnv})
		_ = f.Close()
		break
	}
	script := filepath.Join(dir, fmt.Sprintf("script-%d.jsonl", attempt))
	if _, err := os.Stat(script); err != nil {
		script = filepath.Join(dir, "script.jsonl")
	}
	data, err := os.ReadFile(script)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake herdr: no script:", err)
		return 2
	}
	stdinLog, _ := os.Create(filepath.Join(dir, fmt.Sprintf("stdin-%d.jsonl", attempt)))
	defer func() { _ = stdinLog.Close() }()
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	readType := func() (string, map[string]any, bool) {
		if !in.Scan() {
			return "", nil, false
		}
		_, _ = stdinLog.Write(append(in.Bytes(), '\n'))
		var m map[string]any
		_ = json.Unmarshal(in.Bytes(), &m)
		t, _ := m["type"].(string)
		return t, m, true
	}
	out := bufio.NewWriter(os.Stdout)
	seq := 100
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var d map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			fmt.Fprintln(os.Stderr, "fake herdr: bad directive:", line)
			return 2
		}
		switch {
		case d["emit"] != nil:
			_, _ = out.Write(append(d["emit"], '\n'))
		case d["replay"] != nil:
			var p string
			_ = json.Unmarshal(d["replay"], &p)
			b, err := os.ReadFile(p)
			if err != nil {
				return 2
			}
			_, _ = out.Write(b)
		case d["wait"] != nil:
			_ = out.Flush()
			var want string
			_ = json.Unmarshal(d["wait"], &want)
			for {
				t, _, ok := readType()
				if !ok || t == want {
					break
				}
			}
		case d["echo_input"] != nil:
			_ = out.Flush()
			for {
				t, m, ok := readType()
				if !ok || t == "terminal.release" {
					break
				}
				if t == "terminal.input" {
					seq++
					b, _ := json.Marshal(map[string]any{"type": "terminal.frame", "seq": seq, "width": 44, "height": 30, "full": false, "bytes": m["bytes"]})
					_, _ = out.Write(append(b, '\n'))
					_ = out.Flush()
				}
			}
		case d["stderr"] != nil:
			var s string
			_ = json.Unmarshal(d["stderr"], &s)
			fmt.Fprint(os.Stderr, s)
		case d["sleep_ms"] != nil:
			_ = out.Flush()
			n, _ := strconv.Atoi(string(d["sleep_ms"]))
			time.Sleep(time.Duration(n) * time.Millisecond)
		case d["exit"] != nil:
			_ = out.Flush()
			n, _ := strconv.Atoi(string(d["exit"]))
			return n
		}
	}
	_ = out.Flush()
	return 0
}

// fake is one fake herdr setup: a directory of scripts and the options
// that run this test binary as herdr.
type fake struct {
	t   *testing.T
	dir string
}

func newFake(t *testing.T, scripts map[string]string) *fake {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &fake{t: t, dir: dir}
}

func (f *fake) opts() BridgeOptions {
	exe, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	return BridgeOptions{
		Bin: exe, Socket: "/nonexistent/test-herdr.sock", Pane: "w1:p1", Mode: BridgeControl, Cols: 44, Rows: 30,
		Retry: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, StartTimeout: 5 * time.Second,
		// The user's real HERDR_* variables must never reach the child.
		Env: append(os.Environ(), fakeHerdrEnv+"="+f.dir, "HERDR_SOCKET_PATH=/real/user/herdr.sock", "HERDR_PANE_ID=w9:p9"),
	}
}

func (f *fake) args(attempt int) (args, env []string) {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("args-%d.json", attempt)))
	if err != nil {
		f.t.Fatalf("attempt %d did not run: %v", attempt, err)
	}
	var v struct{ Args, Env []string }
	if err := json.Unmarshal(b, &v); err != nil {
		f.t.Fatal(err)
	}
	return v.Args, v.Env
}

func (f *fake) stdin(attempt int) []map[string]any {
	f.t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("stdin-%d.jsonl", attempt)))
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

type frameSink struct {
	mu     sync.Mutex
	frames []TerminalFrame
	closed chan Closed
}

func newSink() *frameSink { return &frameSink{closed: make(chan Closed, 1)} }

func (s *frameSink) wire(o *BridgeOptions) {
	o.OnFrame = func(f TerminalFrame) { s.mu.Lock(); s.frames = append(s.frames, f); s.mu.Unlock() }
	o.OnClose = func(c Closed) { s.closed <- c }
}

func (s *frameSink) get() []TerminalFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TerminalFrame(nil), s.frames...)
}

func (s *frameSink) waitClosed(t *testing.T) Closed {
	t.Helper()
	select {
	case c := <-s.closed:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not close")
		return Closed{}
	}
}

func fixture(t *testing.T) string {
	p, err := filepath.Abs("../../testdata/herdr/terminal-session/control-frames.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBridgeReplaysRecordedFramesAndReleases(t *testing.T) {
	f := newFake(t, map[string]string{"script.jsonl": fmt.Sprintf(`
{"replay": %q}
{"wait": "terminal.release"}
{"emit": {"type": "terminal.closed", "reason": "detached"}}
`, fixture(t))})
	o := f.opts()
	s := newSink()
	s.wire(&o)
	b, err := StartBridge(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(s.get()) < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	frames := s.get()
	if len(frames) != 4 {
		t.Fatalf("frames = %d, want the 4 recorded", len(frames))
	}
	if !frames[0].Full || frames[0].Seq != 1 || frames[0].Width <= 0 || frames[0].Height <= 0 {
		t.Errorf("first frame = %+v, want full seq 1", frames[0])
	}
	for i, fr := range frames {
		if !strings.HasPrefix(string(fr.Bytes), "\x1b[?2026h") {
			t.Errorf("frame %d bytes do not start with DEC 2026: %q", i, fr.Bytes[:min(12, len(fr.Bytes))])
		}
		if i > 0 && fr.Seq != frames[i-1].Seq+1 {
			t.Errorf("frame %d seq %d after %d", i, fr.Seq, frames[i-1].Seq)
		}
	}
	b.Release()
	if c := s.waitClosed(t); c.Reason != CloseReleased {
		t.Errorf("closed = %+v, want released", c)
	}
	args, env := f.args(1)
	want := "terminal session control w1:p1 --cols 44 --rows 30"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if len(env) != 1 || env[0] != "HERDR_SOCKET_PATH=/nonexistent/test-herdr.sock" {
		t.Errorf("child HERDR_* env = %v, want only the configured socket", env)
	}
}

func TestBridgeCommandsReachHerdr(t *testing.T) {
	f := newFake(t, map[string]string{"script.jsonl": `
{"emit": {"type": "terminal.frame", "seq": 1, "width": 44, "height": 30, "full": true, "bytes": "G1s/MjAyNmg="}}
{"echo_input": true}
`})
	o := f.opts()
	o.Takeover = true
	s := newSink()
	s.wire(&o)
	b, err := StartBridge(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Input([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	col, row := 3, 4
	for _, err := range []error{b.Resize(58, 30), b.Scroll(true, 2, &col, &row), b.Scroll(false, 2, nil, nil), b.Mouse("down", "", 12, 5), b.Mouse("up", "right", 12, 5)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(s.get()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fr := s.get(); len(fr) != 2 || string(fr[1].Bytes) != "ls\r" {
		t.Fatalf("echoed frames = %+v", fr)
	}
	b.Release()
	if c := s.waitClosed(t); c.Reason != CloseReleased {
		t.Errorf("closed = %+v, want released", c)
	}
	args, _ := f.args(1)
	if args[len(args)-1] != "--takeover" {
		t.Errorf("args = %v, want --takeover", args)
	}
	got := f.stdin(1)
	want := []string{
		`{"bytes":"bHMN","type":"terminal.input"}`,
		`{"cols":58,"rows":30,"type":"terminal.resize"}`,
		// A scroll is one wheel notch per line, at the given cell or at the
		// middle of the pane (58x30 after the resize).
		`{"column":3,"direction":"up","lines":1,"row":4,"type":"terminal.scroll"}`,
		`{"column":3,"direction":"up","lines":1,"row":4,"type":"terminal.scroll"}`,
		`{"column":29,"direction":"down","lines":1,"row":15,"type":"terminal.scroll"}`,
		`{"column":29,"direction":"down","lines":1,"row":15,"type":"terminal.scroll"}`,
		`{"action":"down","column":12,"row":5,"type":"terminal.mouse"}`,
		`{"action":"up","button":"right","column":12,"row":5,"type":"terminal.mouse"}`,
		`{"type":"terminal.release"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("stdin = %v", got)
	}
	for i := range want {
		b, _ := json.Marshal(got[i])
		if string(b) != want[i] {
			t.Errorf("stdin[%d] = %s, want %s", i, b, want[i])
		}
	}
	if err := b.Input([]byte("x")); !errors.Is(err, ErrBridgeClosed) {
		t.Errorf("input after release = %v, want ErrBridgeClosed", err)
	}
}

func TestBridgeRetriesReadInProgress(t *testing.T) {
	busy := `{"emit": {"type": "terminal.closed", "reason": "terminal attach failed: terminal term_1 has a read in progress; retry"}}`
	f := newFake(t, map[string]string{
		"script-1.jsonl": busy,
		"script-2.jsonl": busy,
		"script.jsonl": `
{"emit": {"type": "terminal.frame", "seq": 1, "width": 44, "height": 30, "full": true, "bytes": ""}}
{"wait": "terminal.release"}
{"emit": {"type": "terminal.closed", "reason": "detached"}}
`,
	})
	o := f.opts()
	s := newSink()
	s.wire(&o)
	b, err := StartBridge(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	f.args(3)
	b.Release()
	s.waitClosed(t)
}

func TestBridgeRefusals(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   CloseReason
	}{
		{"busy", `{"emit": {"type": "terminal.closed", "reason": "terminal attach failed: terminal term_1 already has an attached client; retry with --takeover"}}`, CloseBusy},
		{"gone", `{"emit": {"type": "terminal.closed", "reason": "terminal session controlling terminal session failed: terminal target w1:p7 not found"}}`, CloseGone},
		{"no server", "{\"stderr\": \"herdr: failed to connect to server\\n\"}\n{\"exit\": 1}", CloseHerdr},
		{"retry exhausted", `{"emit": {"type": "terminal.closed", "reason": "terminal attach failed: terminal term_1 has a read in progress; retry"}}`, CloseHerdr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, map[string]string{"script.jsonl": tc.script})
			_, err := StartBridge(context.Background(), f.opts())
			var ae *AttachError
			if !errors.As(err, &ae) || ae.Reason != tc.want {
				t.Fatalf("err = %v, want AttachError %s", err, tc.want)
			}
			if tc.name == "no server" && !strings.Contains(ae.Message, "failed to connect") {
				t.Errorf("message = %q, want herdr's stderr", ae.Message)
			}
		})
	}
}

func TestBridgeTakenOverAndGone(t *testing.T) {
	for reason, want := range map[string]CloseReason{
		"terminal attach taken over": CloseTakenOver,
		"terminal term_1 exited":     CloseGone,
		"server is shutting down":    CloseHerdr,
	} {
		f := newFake(t, map[string]string{"script.jsonl": fmt.Sprintf(`
{"emit": {"type": "terminal.frame", "seq": 1, "width": 44, "height": 30, "full": true, "bytes": ""}}
{"sleep_ms": 20}
{"emit": {"type": "terminal.closed", "reason": %q}}
`, reason)})
		o := f.opts()
		s := newSink()
		s.wire(&o)
		b, err := StartBridge(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if c := s.waitClosed(t); c.Reason != want || c.Message != reason {
			t.Errorf("%q: closed = %+v, want %s", reason, c, want)
		}
		<-b.Done()
		if err := b.Resize(10, 10); !errors.Is(err, ErrBridgeClosed) {
			t.Errorf("resize after close = %v", err)
		}
	}
}

func TestBridgeCrashIsHerdr(t *testing.T) {
	f := newFake(t, map[string]string{"script.jsonl": `
{"emit": {"type": "terminal.frame", "seq": 1, "width": 44, "height": 30, "full": true, "bytes": ""}}
{"sleep_ms": 20}
{"exit": 3}
`})
	o := f.opts()
	s := newSink()
	s.wire(&o)
	if _, err := StartBridge(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if c := s.waitClosed(t); c.Reason != CloseHerdr {
		t.Errorf("closed = %+v, want herdr", c)
	}
}

func TestBridgeObserve(t *testing.T) {
	f := newFake(t, map[string]string{"script.jsonl": `
{"emit": {"type": "terminal.frame", "seq": 1, "width": 100, "height": 20, "full": true, "bytes": ""}}
{"sleep_ms": 10000}
`})
	o := f.opts()
	o.Mode, o.Cols, o.Rows, o.Takeover = BridgeObserve, 100, 20, true
	s := newSink()
	s.wire(&o)
	b, err := StartBridge(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Input([]byte("x")); !errors.Is(err, ErrObserveOnly) {
		t.Errorf("observe input = %v", err)
	}
	start := time.Now()
	b.Release()
	if time.Since(start) > 2*time.Second {
		t.Errorf("observe release took %v", time.Since(start))
	}
	if c := s.waitClosed(t); c.Reason != CloseReleased {
		t.Errorf("closed = %+v", c)
	}
	args, _ := f.args(1)
	if got := strings.Join(args, " "); got != "terminal session observe w1:p1 --cols 100 --rows 20" {
		t.Errorf("args = %q (observe never takes over)", got)
	}
}

func TestBridgeStartTimeout(t *testing.T) {
	f := newFake(t, map[string]string{"script.jsonl": `{"sleep_ms": 5000}`})
	o := f.opts()
	o.StartTimeout = 100 * time.Millisecond
	_, err := StartBridge(context.Background(), o)
	var ae *AttachError
	if !errors.As(err, &ae) || ae.Reason != CloseHerdr {
		t.Fatalf("err = %v", err)
	}
}

func TestTerminalSupported(t *testing.T) {
	for v, want := range map[string]bool{
		"0.9.2": true, "0.9.5": true, "0.10.0": true, "1.0": true, "herdr 0.9.5": true, "0.9.3-preview.1": true, "v0.9.2": true,
		"0.9.1": false, "0.7.2": false, "": false, "dev": false, "0.9.x": false,
	} {
		if got := TerminalSupported(v); got != want {
			t.Errorf("TerminalSupported(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestResolveBin(t *testing.T) {
	if got, _ := ResolveBin("/opt/herdr"); got != "/opt/herdr" {
		t.Errorf("configured = %q", got)
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	if _, err := ResolveBin(""); err == nil {
		t.Error("want not found")
	}
	bin := filepath.Join(dir, ".local", "bin", "herdr")
	_ = os.MkdirAll(filepath.Dir(bin), 0o700)
	_ = os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700)
	if got, err := ResolveBin(""); err != nil || got != bin {
		t.Errorf("fallback = %q, %v", got, err)
	}
	onPath := filepath.Join(dir, "herdr")
	_ = os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0o700)
	if got, _ := ResolveBin(""); got != onPath {
		t.Errorf("PATH = %q", got)
	}
}

func TestBridgeScrollIsOneWheelNotchPerLineUpToTheCap(t *testing.T) {
	f := newFake(t, map[string]string{"script.jsonl": `
{"emit": {"type": "terminal.frame", "seq": 1, "width": 44, "height": 30, "full": true, "bytes": "G1s/MjAyNmg="}}
{"echo_input": true}
`})
	o := f.opts() // 44x30
	s := newSink()
	s.wire(&o)
	b, err := StartBridge(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Scroll(true, MaxWheelNotches+500, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Scroll(true, 0, nil, nil); err == nil {
		t.Error("a scroll of no lines should be refused")
	}
	b.Release()
	s.waitClosed(t)
	got := f.stdin(1)
	if len(got) != MaxWheelNotches+1 { // the notches, then the release
		t.Fatalf("stdin has %d lines, want %d notches and a release", len(got), MaxWheelNotches)
	}
	enc, _ := json.Marshal(got[0])
	if want := `{"column":22,"direction":"up","lines":1,"row":15,"type":"terminal.scroll"}`; string(enc) != want {
		t.Errorf("first notch = %s, want %s (middle of 44x30)", enc, want)
	}
}
