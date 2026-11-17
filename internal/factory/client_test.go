package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
)

func TestClientStaysOffWhenNoProjectStarted(t *testing.T) {
	dataDir := t.TempDir()
	c := NewClient(ClientOptions{
		DataDir: dataDir,
		Home:    dataDir,
		Getenv:  func(string) string { return "" },
	})
	if c.Active() {
		t.Fatal("Client.Active() = true on empty data_dir, want false")
	}

	ctx := context.Background()
	projects, err := c.Projects(ctx)
	if err != nil || len(projects.Projects) != 0 {
		t.Fatalf("Projects: %+v, err=%v", projects, err)
	}
	asks, err := c.Asks(ctx, AsksFilter{Open: true})
	if err != nil || len(asks.Asks) != 0 {
		t.Fatalf("Asks: %+v, err=%v", asks, err)
	}
	rules, err := c.Rules(ctx, "")
	if err != nil || len(rules.Rules) != 0 {
		t.Fatalf("Rules: %+v, err=%v", rules, err)
	}
	alerts, err := c.Alerts(ctx, "")
	if err != nil || len(alerts.Alerts) != 0 {
		t.Fatalf("Alerts: %+v, err=%v", alerts, err)
	}
	if _, err := os.Stat(EnginesDir(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("EnginesDir(%s) was created while inactive", dataDir)
	}
}

func TestClientCommandsAndErrorEnvelope(t *testing.T) {
	fx := loadContractFixture(t)
	var gotArgs [][]string

	c := NewClient(ClientOptions{
		DataDir: t.TempDir(),
		Getenv:  func(string) string { return "" },
		Exec: func(_ context.Context, args ...string) ([]byte, string, int, error) {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			switch {
			case reflect.DeepEqual(args, []string{"owner", "projects", "--json"}):
				return fx.Valid.Projects, "", 0, nil
			case reflect.DeepEqual(args, []string{"owner", "asks", "--project", "beacon", "--open", "--json"}):
				raw, _ := json.Marshal(map[string]any{"protocol": 1, "asks": fx.Valid.AsksByKind[3:4]})
				return raw, "", 0, nil
			case reflect.DeepEqual(args, []string{"owner", "answer", "ask002", "--option", "1", "--proof", "prf-112233445566", "--json"}):
				return fx.Valid.Actions.Answer, "", 0, nil
			case reflect.DeepEqual(args, []string{"owner", "answer", "ask002", "--option", "1", "--proof", "prf-bad", "--json"}):
				raw, _ := json.Marshal(ErrorEnvelope{
					Protocol: 1,
					OK:       false,
					Code:     CodeProofRejected,
					Error:    "daemon rejected proof",
				})
				return raw, "", 1, nil
			case reflect.DeepEqual(args, []string{"owner", "say", "beacon", "Ship offline sync", "--json"}):
				return fx.Valid.Actions.Say, "", 0, nil
			default:
				t.Fatalf("unexpected CLI args: %v", args)
				return nil, "", 1, nil
			}
		},
	})

	ctx := context.Background()
	proj, err := c.Projects(ctx)
	if err != nil || len(proj.Projects) != 1 || proj.Projects[0].ID != "beacon" {
		t.Fatalf("Projects: %+v, err=%v", proj, err)
	}

	asks, err := c.Asks(ctx, AsksFilter{Project: "beacon", Open: true})
	if err != nil || len(asks.Asks) != 1 || asks.Asks[0].ID != "ask004" {
		t.Fatalf("Asks: %+v, err=%v", asks, err)
	}

	ans, err := c.AnswerIndex(ctx, "ask002", 1, "prf-112233445566")
	if err != nil || ans.Ask.ID != "ask002" {
		t.Fatalf("AnswerIndex: %+v, err=%v", ans, err)
	}

	_, err = c.AnswerIndex(ctx, "ask002", 1, "prf-bad")
	if !errors.Is(err, ErrProofRejected) || ErrorCode(err) != CodeProofRejected {
		t.Fatalf("AnswerIndex(rejected) err = %v (code=%q), want ErrProofRejected", err, ErrorCode(err))
	}

	say, err := c.Say(ctx, "beacon", "Ship offline sync")
	if err != nil || say.Coordinator != "term-4" {
		t.Fatalf("Say: %+v, err=%v", say, err)
	}
	if len(gotArgs) != 5 {
		t.Fatalf("got %d exec calls, want 5", len(gotArgs))
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func TestClientLatchesErrOwnerUnsupported(t *testing.T) {
	dataDir := t.TempDir()
	fakeHome := t.TempDir()
	pinned := PinnedPythonVersion()
	pyHome := createManagedPythonStub(t, dataDir, pinned)
	invocationsFile := filepath.Join(t.TempDir(), "invocations.log")

	script := fmt.Sprintf("#!/bin/sh\necho x >> %q\necho \"herdr-task: error: argument cmd: invalid choice: 'owner'\" >&2\nexit 2\n", invocationsFile)
	if err := os.WriteFile(filepath.Join(pyHome, "bin", "python3"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	asksDir := filepath.Join(dataDir, "tasks", "asks")
	if err := os.MkdirAll(asksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Legacy pre-SDF-21 Shepherdr ask record (no "status" field).
	legacyAsk := `{"id":"a1b2c3","question":"Ship?","options":["Yes","No"],"recommend":"Yes"}`
	if err := os.WriteFile(filepath.Join(asksDir, "a1b2c3.json"), []byte(legacyAsk), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewClient(ClientOptions{
		DataDir: dataDir,
		Home:    fakeHome,
		TempDir: t.TempDir(),
		Getenv:  func(string) string { return "" },
		Engine:  EngineSource{FS: herdrsheepdog.Factory, Version: "v0.2.0"},
		RunCommand: func(context.Context, string, []string, ...string) CommandOutput {
			return CommandOutput{Stdout: pinned + "\n" + pyHome + "\n"}
		},
	})

	ctx := context.Background()
	for i := range 4 {
		_, _, err := c.AsksPartial(ctx, AsksFilter{Open: true})
		if !errors.Is(err, ErrOwnerUnsupported) {
			t.Fatalf("call %d AsksPartial err = %v, want ErrOwnerUnsupported", i, err)
		}
	}
	// Adding another legacy ask file or calling Projects must still hit the latched digest without spawning Python.
	if err := os.WriteFile(filepath.Join(asksDir, "d4e5f6.json"), []byte(legacyAsk), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Asks(ctx, AsksFilter{Open: true}); !errors.Is(err, ErrOwnerUnsupported) {
		t.Fatalf("Asks err = %v, want ErrOwnerUnsupported", err)
	}
	if _, err := c.Projects(ctx); !errors.Is(err, ErrOwnerUnsupported) {
		t.Fatalf("Projects err = %v, want ErrOwnerUnsupported", err)
	}
	if got := countLines(t, invocationsFile); got != 1 {
		t.Fatalf("python invocations = %d, want 1", got)
	}
}

func TestClientOpenAsksSkipsClosedStoreAndCachesStoreDigest(t *testing.T) {
	fx := loadContractFixture(t)
	dataDir := t.TempDir()
	fakeHome := t.TempDir()
	pinned := PinnedPythonVersion()
	pyHome := createManagedPythonStub(t, dataDir, pinned)
	invocationsFile := filepath.Join(t.TempDir(), "invocations.log")
	mockStdoutFile := filepath.Join(t.TempDir(), "mock-stdout.json")

	script := fmt.Sprintf("#!/bin/sh\necho x >> %q\ncat %q\n", invocationsFile, mockStdoutFile)
	if err := os.WriteFile(filepath.Join(pyHome, "bin", "python3"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	asksDir := filepath.Join(dataDir, "tasks", "asks")
	if err := os.MkdirAll(asksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Step 1: Only closed asks in <tasksDir>/asks -> 0 Python spawns and no engine unpack.
	if err := os.WriteFile(filepath.Join(asksDir, "ask000.json"), []byte(`{"id":"ask000","status":"answered"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asksDir, "ask009.json"), []byte(`{"id":"ask009","status":"cancelled"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewClient(ClientOptions{
		DataDir: dataDir,
		Home:    fakeHome,
		TempDir: t.TempDir(),
		Getenv:  func(string) string { return "" },
		Engine:  EngineSource{FS: herdrsheepdog.Factory, Version: "v0.2.0"},
		RunCommand: func(context.Context, string, []string, ...string) CommandOutput {
			return CommandOutput{Stdout: pinned + "\n" + pyHome + "\n"}
		},
	})

	ctx := context.Background()
	for range 3 {
		resp, rejected, err := c.AsksPartial(ctx, AsksFilter{Open: true})
		if err != nil || len(rejected) != 0 || len(resp.Asks) != 0 {
			t.Fatalf("closed-only AsksPartial = (%+v, %+v, %v), want empty", resp, rejected, err)
		}
	}
	if got := countLines(t, invocationsFile); got != 0 {
		t.Fatalf("python invocations on closed-only store = %d, want 0", got)
	}
	if _, err := os.Stat(EnginesDir(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("EnginesDir(%s) was unpacked while all asks were closed", dataDir)
	}

	// Step 2: Add an open ask -> first poll spawns Python once; repeated polls hit the content digest cache.
	var ask1 Ask
	if err := json.Unmarshal(fx.Valid.AsksByKind[3], &ask1); err != nil {
		t.Fatal(err)
	}
	ask1.ID = "ask001"
	ask1.Question = "Keep retry budget A?"
	ask1Bytes, _ := json.Marshal(ask1)
	env1Bytes, _ := json.Marshal(map[string]any{"protocol": 1, "asks": []Ask{ask1}})
	if err := os.WriteFile(mockStdoutFile, env1Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asksDir, "ask001.json"), ask1Bytes, 0o644); err != nil {
		t.Fatal(err)
	}

	for i := range 4 {
		resp, rejected, err := c.AsksPartial(ctx, AsksFilter{Open: true})
		if err != nil || len(rejected) != 0 || len(resp.Asks) != 1 || resp.Asks[0].Question != "Keep retry budget A?" {
			t.Fatalf("poll %d AsksPartial = (%+v, %+v, %v)", i, resp, rejected, err)
		}
	}
	if got := countLines(t, invocationsFile); got != 1 {
		t.Fatalf("python invocations after 4 identical polls = %d, want 1", got)
	}

	// Step 3: Same-byte-length edit on disk invalidates the SHA-256 content digest immediately.
	ask2 := ask1
	ask2.Question = "Keep retry budget B?"
	ask2Bytes, _ := json.Marshal(ask2)
	if len(ask2Bytes) != len(ask1Bytes) {
		t.Fatalf("expected equal byte lengths for content-digest test: %d vs %d", len(ask2Bytes), len(ask1Bytes))
	}
	env2Bytes, _ := json.Marshal(map[string]any{"protocol": 1, "asks": []Ask{ask2}})
	if err := os.WriteFile(mockStdoutFile, env2Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asksDir, "ask001.json"), ask2Bytes, 0o644); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		resp, _, err := c.AsksPartial(ctx, AsksFilter{Open: true})
		if err != nil || len(resp.Asks) != 1 || resp.Asks[0].Question != "Keep retry budget B?" {
			t.Fatalf("updated poll %d AsksPartial = (%+v, %v)", i, resp, err)
		}
	}
	if got := countLines(t, invocationsFile); got != 2 {
		t.Fatalf("python invocations after same-size edit + 3 polls = %d, want 2", got)
	}

	// Step 4: Answering ask001 transitions all asks to closed -> returns empty without another Python spawn.
	if err := os.WriteFile(filepath.Join(asksDir, "ask001.json"), []byte(`{"id":"ask001","status":"answered"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, _, err := c.AsksPartial(ctx, AsksFilter{Open: true})
	if err != nil || len(resp.Asks) != 0 {
		t.Fatalf("post-answer AsksPartial = (%+v, %v), want empty", resp, err)
	}
	if got := countLines(t, invocationsFile); got != 2 {
		t.Fatalf("python invocations after ask closed = %d, want 2", got)
	}
}
