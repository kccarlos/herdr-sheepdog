package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wav"
)

func writeWAV(t *testing.T, path string, rate int, n int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w, err := wav.NewWriter(f, rate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceTestFlagsAndValidation(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.wav")
	bad := filepath.Join(dir, "bad.wav")
	writeWAV(t, good, 16000, 3200)
	writeWAV(t, bad, 44100, 3200)
	// No keys, no config file.
	for _, k := range []string{"GEMINI_API_KEY", "GEMINI_API_KEY_1", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3"} {
		t.Setenv(k, "")
	}
	noConfig := filepath.Join(dir, "none.toml")

	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"help", []string{"-h"}, 0, "usage: sheepdog voice-test"},
		{"nothing to do", []string{"--config", noConfig}, 2, "need at least one of"},
		{"bad urgency", []string{"--config", noConfig, "--text", "x", "--push-urgency", "loud"}, 2, "unknown urgency"},
		{"no key", []string{"--config", noConfig, "--text", "hi"}, 2, "no Gemini API key"},
		{"wrong rate", []string{"--config", noConfig, "--in", bad}, 2, "sample rate 44100"},
		{"missing file", []string{"--config", noConfig, "--in", filepath.Join(dir, "nope.wav")}, 2, "no such file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if tc.name == "wrong rate" || tc.name == "missing file" {
				t.Setenv("GEMINI_API_KEY", "not-a-real-key")
			}
			code := run(context.Background(), append([]string{"voice-test"}, tc.args...), &out, &errb)
			if code != tc.code {
				t.Fatalf("exit %d, want %d; stderr=%s", code, tc.code, errb.String())
			}
			if !strings.Contains(errb.String(), tc.want) {
				t.Fatalf("stderr %q does not contain %q", errb.String(), tc.want)
			}
		})
	}
	if _, err := readInputWAV(good); err != nil {
		t.Errorf("good wav rejected: %v", err)
	}
	if u, err := parseUrgency("Interrupt"); err != nil || u.String() != "interrupt" {
		t.Errorf("parseUrgency = %v, %v", u, err)
	}
}

func TestUsageMentionsVoiceTest(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"help"}, &out, &out); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "voice-test") {
		t.Error("usage does not list voice-test")
	}
}
