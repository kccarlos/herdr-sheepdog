package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestVoicePageFilesAndContentTypes checks that every file the /voice page
// loads is embedded and served with a type the browser accepts: worklets
// and the page module must be text/javascript or addModule() refuses them.
func TestVoicePageFilesAndContentTypes(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	cases := []struct {
		path, ctype, contains string
	}{
		{"/voice", "text/html", `<script type="module" src="/voice/app.js">`},
		{"/voice/", "text/html", `id="btn-connect"`},
		{"/voice/index.html", "text/html", "sheepdog voice"},
		{"/voice/app.js", "text/javascript", "registerProcessor"},
		{"/voice/audio-util.js", "text/javascript", "export class Player"},
		{"/voice/audio-capture.worklet.js", "text/javascript", "registerProcessor('sheepdog-capture'"},
		{"/voice/audio-playback.worklet.js", "text/javascript", "registerProcessor('sheepdog-playback'"},
		{"/voice/style.css", "text/css", "prefers-color-scheme"},
	}
	for _, tc := range cases {
		resp, err := http.Get(h.http.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", tc.path, resp.StatusCode)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, tc.ctype) {
			t.Errorf("GET %s: Content-Type %q, want %s", tc.path, ct, tc.ctype)
		}
		if resp.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q", tc.path, resp.Header.Get("Cache-Control"))
		}
		if tc.path == "/voice/app.js" {
			// app.js loads the worklets; it does not register processors itself.
			if strings.Contains(string(body), tc.contains) {
				t.Errorf("app.js registers a processor: worklet code leaked into the page script")
			}
			for _, want := range []string{"audio-capture.worklet.js", "audio-playback.worklet.js", `type: 'cancel'`, `type: 'mute'`, `type: 'ping'`} {
				if !strings.Contains(string(body), want) {
					t.Errorf("app.js does not contain %q", want)
				}
			}
			continue
		}
		if !strings.Contains(string(body), tc.contains) {
			t.Errorf("GET %s: body does not contain %q", tc.path, tc.contains)
		}
	}
	// The worklets import the shared module by a relative path that must
	// resolve under /voice/.
	for _, p := range []string{"/voice/audio-capture.worklet.js", "/voice/audio-playback.worklet.js"} {
		resp, err := http.Get(h.http.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.Contains(string(body), "from './audio-util.js'") {
			t.Errorf("%s does not import ./audio-util.js", p)
		}
	}
}

func TestDevTestdataRoute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "question.wav"), []byte("RIFFfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	get := func(h *harness, path string) (int, string, string) {
		t.Helper()
		resp, err := http.Get(h.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
	}

	t.Run("off by default", func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		if code, _, _ := get(h, "/testdata/question.wav"); code != http.StatusNotFound {
			t.Errorf("dev route off: GET /testdata/question.wav = %d, want 404", code)
		}
	})

	t.Run("on", func(t *testing.T) {
		h := newHarness(t, harnessOpts{devTestdata: dir})
		code, ctype, body := get(h, "/testdata/question.wav")
		if code != http.StatusOK || body != "RIFFfake" {
			t.Errorf("GET /testdata/question.wav = %d %q", code, body)
		}
		if ctype != "audio/wav" {
			t.Errorf("Content-Type = %q", ctype)
		}
		for _, p := range []string{"/testdata/notes.txt", "/testdata/missing.wav", "/testdata/.hidden.wav", "/testdata/..%2Fquestion.wav", "/testdata/"} {
			if code, _, b := get(h, p); code != http.StatusNotFound {
				t.Errorf("GET %s = %d %q, want 404", p, code, b)
			}
		}
	})
}

// TestClientStatsEvent checks the periodic web.client.stats event carries
// the frame counters (used to prove a muted page sends nothing).
func TestClientStatsEvent(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, statsInterval: 50 * time.Millisecond})
	c := dialWS(t, h)
	c.expect("status")
	for i := 0; i < 3; i++ {
		c.sendAudio(make([]byte, 640))
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range h.log.Tail(0) {
			if ev.Type != "web.client.stats" {
				continue
			}
			var p map[string]any
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if n, _ := p["frames_in"].(float64); n >= 3 {
				if _, ok := p["muted_drops"]; !ok {
					t.Errorf("stats payload lacks muted_drops: %s", ev.Payload)
				}
				_ = c.conn.Close(websocket.StatusNormalClosure, "done")
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no web.client.stats with frames_in >= 3; types: %v", h.types())
}
