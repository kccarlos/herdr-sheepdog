package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
)

func withStdin(t *testing.T, s string) {
	t.Helper()
	orig := stdin
	stdin = strings.NewReader(s)
	t.Cleanup(func() { stdin = orig })
}

func TestDataClearAsksFirst(t *testing.T) {
	cfg, data, _ := lifeSetup(t, "")
	day := filepath.Join(data, "events", "2026-10-01.jsonl")
	if err := os.MkdirAll(filepath.Dir(day), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(day, []byte("{}\n"), 0o600)
	withStdin(t, "n\n")
	code, out, _ := runCLI("data", "clear", "--config", cfg)
	if code != 1 || !strings.Contains(out, "Delete the event log (1 day file)") || !strings.Contains(out, "nothing deleted") {
		t.Errorf("no: exit %d %q", code, out)
	}
	if _, err := os.Stat(day); err != nil {
		t.Fatal("deleted after a no")
	}
	withStdin(t, "y\n")
	if code, out, _ := runCLI("data", "clear", "--events", "--config", cfg); code != 0 || !strings.Contains(out, "deleted 1 file") {
		t.Errorf("yes: exit %d %q", code, out)
	}
	if _, err := os.Stat(day); err == nil {
		t.Error("not deleted after a yes")
	}
	if code, out, _ := runCLI("data", "clear", "--config", cfg); code != 0 || !strings.Contains(out, "nothing to delete") {
		t.Errorf("empty: exit %d %q", code, out)
	}
	_ = os.WriteFile(filepath.Join(data, "state.json"), []byte("{}"), 0o600)
	if code, _, _ := runCLI("data", "clear", "--all", "--yes", "--config", cfg); code != 0 {
		t.Errorf("--all --yes: exit %d", code)
	}
	if _, err := os.Stat(data); err == nil {
		t.Error("--all left the data dir")
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Error("--all deleted the config")
	}
}

func TestDataClearRefusesWhileRunning(t *testing.T) {
	cfg, data, _ := lifeSetup(t, "")
	lock, err := daemon.Acquire(data)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_ = lock.Write(daemon.Info{PID: os.Getpid(), URL: "http://127.0.0.1:1"})
	if code, _, errs := runCLI("data", "clear", "--all", "--yes", "--config", cfg); code != 1 || !strings.Contains(errs, "stop it first") {
		t.Errorf("exit %d %q", code, errs)
	}
}

// TestServeRuntimeLogRetentionAndRedaction runs serve in-process: the
// runtime log gets the banner and the events log.level selects, a secret
// from the environment never reaches it, and an old event day file is
// pruned at startup while a recent one stays.
func TestServeRuntimeLogRetentionAndRedaction(t *testing.T) {
	cfg, data, _ := lifeSetup(t, "[events]\nretention_days = 3\n")
	const secret = "sk-SecretThatMustNotBeLogged000"
	t.Setenv("OPENAI_API_KEY", secret)
	t.Setenv("SHEEPDOG_WEB_PORT", "0")
	old := filepath.Join(data, "events", "2020-01-01.jsonl")
	recent := filepath.Join(data, "events", time.Now().UTC().Format("2006-01-02")+".jsonl")
	for _, p := range []string{old, recent} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(p, []byte(`{"ts":"2020-01-01T00:00:00Z","type":"x"}`+"\n"), 0o600)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", cfg, "--voice", "off", "--herdr", "mock", "--brain", "off"}, stdout, stderr)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdout.String(), "event retention") {
		time.Sleep(20 * time.Millisecond)
	}
	// Print the secret the way a careless component might.
	fprintf(stderr, "debug: key is %s\n", secret)
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("serve exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the 2020 day file was not pruned")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("today's day file was pruned")
	}
	b, err := os.ReadFile(daemon.LogFile(data))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	for _, want := range []string{"listening on", "INFO daemon.start", "event retention: deleted 1 day file(s)"} {
		if !strings.Contains(log, want) {
			t.Errorf("runtime log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, secret) {
		t.Errorf("the secret reached the runtime log:\n%s", log)
	}
	if _, held := daemon.Running(data); held {
		t.Error("lock still held after serve returned")
	}
}
