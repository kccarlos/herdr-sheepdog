package hc_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func TestClientCallsRequestsAndResults(t *testing.T) {
	server := hctest.New(t)
	client := hc.NewClient(server.Path)
	ctx := context.Background()

	var terminal hc.Terminal
	if err := json.Unmarshal([]byte(realisticTerminal), &terminal); err != nil {
		t.Fatal(err)
	}
	server.Add(terminal)
	server.SetScreen(terminal.Address, "first line\nsecond line\n")

	hello, err := client.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Version != "0.1.15" || hello.Generation != 1 ||
		hello.Core.ID != "fake-core" || hello.Core.Name != "hctest" ||
		!contains(hello.Features, "events") || !contains(hello.Methods, "terminal.get") {
		t.Fatalf("unexpected hello: %+v", hello)
	}
	assertParams(t, server, "hello", map[string]any{"generation": float64(1)})

	list, err := client.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Terminals) != 1 || list.Terminals[0].Address != "t1" || list.HiddenFinished != 0 {
		t.Fatalf("unexpected list: %+v", list)
	}
	assertParams(t, server, "terminal.list", map[string]any{"all": true})

	got, err := client.Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "cc" || got.Turn != 1 || got.Status == nil ||
		got.Status.State != "blocked" || got.Status.Kind == nil || *got.Status.Kind != "permission" ||
		got.Foreground == nil || got.Foreground.PID != 57843 ||
		got.PID == nil || *got.PID != 57843 || got.Parent != nil || got.FinishedMS != nil {
		t.Fatalf("unexpected terminal: %+v", got)
	}
	assertParams(t, server, "terminal.get", map[string]any{"terminal": "t1"})

	read, err := client.Read(ctx, "t1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if read.Text != "first line\nsecond line\n" || !read.Complete || read.How != "screen" || read.Why != nil {
		t.Fatalf("unexpected read: %+v", read)
	}
	assertParams(t, server, "terminal.read", map[string]any{
		"terminal": "t1",
		"lines":    float64(7),
	})

	if _, err := client.Send(ctx, "t1", "continue", false); !errors.Is(err, hc.ErrBlocked) {
		t.Fatalf("blocked send error = %v, want ErrBlocked", err)
	}
	sent, err := client.Send(ctx, "t1", "continue", true)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Started == nil || !*sent.Started || sent.Status == nil || *sent.Status != "blocked" || sent.Turn != 1 {
		t.Fatalf("unexpected send: %+v", sent)
	}
	assertParams(t, server, "terminal.send", map[string]any{
		"terminal": "t1",
		"text":     "continue",
		"force":    true,
	})

	keys, err := client.Keys(ctx, "t1", "enter", "tab")
	if err != nil {
		t.Fatal(err)
	}
	if keys.Status == nil || *keys.Status != "blocked" || keys.Turn != 1 {
		t.Fatalf("unexpected keys: %+v", keys)
	}
	assertParams(t, server, "terminal.keys", map[string]any{
		"terminal": "t1",
		"keys":     []any{"enter", "tab"},
	})

	interrupted, err := client.Interrupt(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Key != "esc" || interrupted.Outcome != "stopped" {
		t.Fatalf("unexpected interrupt: %+v", interrupted)
	}
	assertParams(t, server, "terminal.interrupt", map[string]any{"terminal": "t1"})

	runParams := hc.RunParams{
		Argv:     []string{"claude"},
		Line:     "",
		Cwd:      "/tmp/work",
		Name:     "worker",
		Env:      map[string]string{"COLORTERM": "truecolor"},
		Labels:   map[string]string{"role": "worker"},
		Keep:     true,
		NoParent: true,
	}
	ran, err := client.Run(ctx, runParams)
	if err != nil {
		t.Fatal(err)
	}
	if ran.Address != "t2" || ran.Name != "worker" || ran.Cwd != "/tmp/work" ||
		ran.Labels["role"] != "worker" || !ran.Keep || ran.State != "live" {
		t.Fatalf("unexpected run result: %+v", ran)
	}
	assertParams(t, server, "terminal.run", map[string]any{
		"argv":      []any{"claude"},
		"cwd":       "/tmp/work",
		"name":      "worker",
		"env":       map[string]any{"COLORTERM": "truecolor"},
		"labels":    map[string]any{"role": "worker"},
		"keep":      true,
		"no_parent": true,
	})

	killed, err := client.Kill(ctx, ran.Address)
	if err != nil {
		t.Fatal(err)
	}
	if killed.Address != ran.Address || killed.State != "finished" ||
		killed.Reason == nil || *killed.Reason != "killed" {
		t.Fatalf("unexpected kill result: %+v", killed)
	}
	assertParams(t, server, "terminal.kill", map[string]any{"terminal": "t2"})
}

func TestCallErrorsAndKindMatching(t *testing.T) {
	server := hctest.New(t)
	client := hc.NewClient(server.Path)

	_, err := client.Get(context.Background(), "missing")
	if !errors.Is(err, hc.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
	var serverErr *hc.Error
	if !errors.As(err, &serverErr) {
		t.Fatalf("Get error type = %T, want *hc.Error", err)
	}
	if serverErr.Code != -32000 || serverErr.Kind != "not_found" || serverErr.Hint == "" {
		t.Fatalf("unexpected server error: %+v", serverErr)
	}
	if !strings.Contains(serverErr.Error(), serverErr.Message) ||
		!strings.Contains(serverErr.Error(), serverErr.Hint) {
		t.Fatalf("Error() = %q, want message and hint", serverErr.Error())
	}

	tests := []struct {
		kind     string
		sentinel error
	}{
		{"not_found", hc.ErrNotFound},
		{"blocked", hc.ErrBlocked},
		{"finished", hc.ErrFinished},
		{"unavailable", hc.ErrUnavailable},
		{"name_taken", hc.ErrNameTaken},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			err := &hc.Error{Code: -32000, Message: tt.kind, Kind: tt.kind}
			if !errors.Is(err, tt.sentinel) {
				t.Fatalf("errors.Is(%+v, %v) = false", err, tt.sentinel)
			}
			for _, other := range tests {
				if other.sentinel != tt.sentinel && errors.Is(err, other.sentinel) {
					t.Fatalf("%s matched %v", tt.kind, other.sentinel)
				}
			}
		})
	}

	var out json.RawMessage
	err = client.Call(context.Background(), "unknown.method", struct{}{}, &out)
	if !errors.As(err, &serverErr) || serverErr.Code != -32601 {
		t.Fatalf("unknown method error = %v, want JSON-RPC -32601", err)
	}
}

func TestCallDeadSocketFailsFastAndUsefully(t *testing.T) {
	dir, err := os.MkdirTemp("", "hct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "missing.sock")
	client := hc.NewClient(path)
	client.Timeout = 500 * time.Millisecond
	start := time.Now()
	_, err = client.Get(context.Background(), "t1")
	if err == nil {
		t.Fatal("Get against a dead socket succeeded")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("dead socket took %s", elapsed)
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "terminal.get") {
		t.Fatalf("dead socket error = %q, want method and socket path", err)
	}
}

func assertParams(t *testing.T, server *hctest.Server, method string, want map[string]any) {
	t.Helper()
	requests := server.Requests()
	if len(requests) == 0 {
		t.Fatal("no requests recorded")
	}
	last := requests[len(requests)-1]
	if last.Method != method {
		t.Fatalf("last request method = %q, want %q", last.Method, method)
	}
	var got map[string]any
	if err := json.Unmarshal(last.Params, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s params:\n got: %#v\nwant: %#v", method, got, want)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

const realisticTerminal = `{
  "address":"t1",
  "argv":["claude"],
  "cols":200,
  "created_ms":1793388781121,
  "cwd":"/tmp/w",
  "die_with_parent":false,
  "exit_code":null,
  "finished_ms":null,
  "foreground":{"argv":["claude"],"cwd":"/private/tmp/w","name":"2.1.314","pid":57843},
  "identity":"ghostty",
  "keep":false,
  "labels":{},
  "name":"cc",
  "notify":true,
  "parent":null,
  "pid":57843,
  "reason":null,
  "reported":true,
  "rm":false,
  "rows":120,
  "screen":"exact",
  "signal":null,
  "state":"live",
  "status":{"app":"claude-code","kind":"permission","msg":"approve Bash: curl -sI https://example.com","state":"blocked"},
  "status_ms":1793388886403,
  "template":null,
  "title":"Claude Code",
  "turn":1
}`
