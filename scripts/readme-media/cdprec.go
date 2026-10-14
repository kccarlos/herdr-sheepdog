//go:build ignore

// cdprec records a page in a headless Chromium (Chrome, Brave, Edge) over
// the DevTools protocol, for the README's status.gif
// (scripts/readme-media/status.sh). It starts the browser with a fake
// microphone that plays a WAV file, opens the URL, injects CSS, clicks a
// selector, and saves the screencast frames with their timestamps:
//
//	go run scripts/readme-media/cdprec.go -browser <binary> -url URL \
//	    -wav question.wav -out DIR -seconds 20 -click '#btn-connect'
//
// DIR gets frame-NNNNN.jpg and frames.txt (an ffmpeg concat list). The
// browser runs with its own throwaway profile, never the user's.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type cdp struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	nextID int
	waits  map[int]chan json.RawMessage
	events chan map[string]json.RawMessage
}

func (c *cdp) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.waits[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("%s: no reply", method)
	}
}

func (c *cdp) read(ctx context.Context) {
	for {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			close(c.events)
			return
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if raw, ok := m["id"]; ok {
			var id int
			_ = json.Unmarshal(raw, &id)
			c.mu.Lock()
			ch := c.waits[id]
			delete(c.waits, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- b
			}
			continue
		}
		c.events <- m
	}
}

func main() {
	browser := flag.String("browser", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Chromium binary")
	url := flag.String("url", "", "page")
	wav := flag.String("wav", "", "WAV the fake microphone plays (headless Chromium on macOS may ignore it; -file is the reliable way)")
	out := flag.String("out", "frames", "output dir")
	seconds := flag.Float64("seconds", 20, "how long to record after the click")
	click := flag.String("click", "", "CSS selector to click once loaded")
	css := flag.String("css", "", "CSS to inject")
	width := flag.Int("width", 800, "viewport width (CSS px)")
	height := flag.Int("height", 520, "viewport height (CSS px)")
	port := flag.Int("port", 9333, "DevTools port")
	fileSel := flag.String("file-selector", "#file-input", "file input that -file goes into")
	file := flag.String("file", "", "file to set on -file-selector, -file-at seconds after the click")
	fileAt := flag.Float64("file-at", 3, "seconds after the click to set -file")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	profile, err := os.MkdirTemp("", "cdprec-profile-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(profile)
	args := []string{"--headless=new", 
		"--user-data-dir=" + profile,
		fmt.Sprintf("--remote-debugging-port=%d", *port),
		"--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream",
		"--autoplay-policy=no-user-gesture-required", "--mute-audio",
		"--no-first-run", "--no-default-browser-check", "--hide-scrollbars",
		fmt.Sprintf("--window-size=%d,%d", *width, *height),
	}
	if *wav != "" {
		abs, _ := filepath.Abs(*wav)
		args = append(args, "--use-file-for-fake-audio-capture="+abs+os.Getenv("CDPREC_WAV_SUFFIX"))
	}
	cmd := exec.Command(*browser, append(args, "about:blank")...)
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*seconds+60)*time.Second)
	defer cancel()
	var wsURL string
	for i := 0; i < 100 && wsURL == ""; i++ {
		time.Sleep(200 * time.Millisecond)
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", *port))
		if err != nil {
			continue
		}
		var targets []struct{ Type, WebSocketDebuggerURL string }
		_ = json.NewDecoder(resp.Body).Decode(&targets)
		resp.Body.Close()
		for _, t := range targets {
			if t.Type == "page" {
				wsURL = t.WebSocketDebuggerURL
				break
			}
		}
	}
	if wsURL == "" {
		log.Fatal("no DevTools page target")
	}
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		log.Fatal(err)
	}
	conn.SetReadLimit(64 << 20)
	c := &cdp{conn: conn, waits: map[int]chan json.RawMessage{}, events: make(chan map[string]json.RawMessage, 256)}
	go c.read(ctx)

	must := func(method string, params any) json.RawMessage {
		r, err := c.call(ctx, method, params)
		if err != nil {
			log.Fatalf("%s: %v", method, err)
		}
		return r
	}
	must("Page.enable", map[string]any{})
	must("Emulation.setDeviceMetricsOverride", map[string]any{"width": *width, "height": *height, "deviceScaleFactor": 2, "mobile": false})
	must("Emulation.setEmulatedMedia", map[string]any{"features": []map[string]string{{"name": "prefers-color-scheme", "value": "light"}}})
	must("Page.navigate", map[string]any{"url": *url})
	time.Sleep(2 * time.Second)
	if *css != "" {
		js := fmt.Sprintf(`(() => { const s = document.createElement('style'); s.textContent = %q; document.head.appendChild(s); })()`, *css)
		must("Runtime.evaluate", map[string]any{"expression": js})
	}

	list, err := os.Create(filepath.Join(*out, "frames.txt"))
	if err != nil {
		log.Fatal(err)
	}
	defer list.Close()
	must("Page.startScreencast", map[string]any{"format": "jpeg", "quality": 92, "everyNthFrame": 1, "maxWidth": *width * 2, "maxHeight": *height * 2})
	start := time.Now()
	clicked := false
	var clickedAt time.Time
	fileSet := *file == ""
	n := 0
	var lastName string
	var lastAt float64
	deadline := time.After(time.Duration(*seconds*1000+1000) * time.Millisecond)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-tick.C:
			if !clicked && time.Since(start) > 1500*time.Millisecond && *click != "" {
				must("Runtime.evaluate", map[string]any{"expression": fmt.Sprintf(`document.querySelector(%q).click()`, *click)})
				clicked = true
				clickedAt = time.Now()
				fmt.Printf("click %.2f\n", time.Since(start).Seconds())
			}
			if clicked && !fileSet && time.Since(clickedAt).Seconds() >= *fileAt {
				abs, _ := filepath.Abs(*file)
				var doc struct {
					Result struct{ Root struct{ NodeID int } }
				}
				_ = json.Unmarshal(must("DOM.getDocument", map[string]any{}), &doc)
				var q struct {
					Result struct{ NodeID int }
				}
				_ = json.Unmarshal(must("DOM.querySelector", map[string]any{"nodeId": doc.Result.Root.NodeID, "selector": *fileSel}), &q)
				must("DOM.setFileInputFiles", map[string]any{"files": []string{abs}, "nodeId": q.Result.NodeID})
				fileSet = true
				fmt.Printf("file %.2f\n", time.Since(start).Seconds())
			}
		case ev, ok := <-c.events:
			if !ok {
				break loop
			}
			var method string
			_ = json.Unmarshal(ev["method"], &method)
			if method != "Page.screencastFrame" {
				continue
			}
			var p struct {
				Data      string
				SessionID int
			}
			_ = json.Unmarshal(ev["params"], &p)
			go func(id int) { _, _ = c.call(ctx, "Page.screencastFrameAck", map[string]any{"sessionId": id}) }(p.SessionID)
			img, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				continue
			}
			at := time.Since(start).Seconds()
			name := fmt.Sprintf("frame-%05d.jpg", n)
			n++
			if err := os.WriteFile(filepath.Join(*out, name), img, 0o644); err != nil {
				log.Fatal(err)
			}
			if lastName != "" {
				fmt.Fprintf(list, "file '%s'\nduration %.3f\n", lastName, at-lastAt)
			}
			lastName, lastAt = name, at
		}
	}
	if lastName != "" {
		end := time.Since(start).Seconds()
		fmt.Fprintf(list, "file '%s'\nduration %.3f\nfile '%s'\n", lastName, end-lastAt, lastName)
	}
	r := must("Runtime.evaluate", map[string]any{"expression": `document.getElementById('transcript') ? document.getElementById('transcript').innerText : ''`, "returnByValue": true})
	var res struct {
		Result struct {
			Result struct{ Value string }
		}
	}
	_ = json.Unmarshal(r, &res)
	fmt.Printf("frames %d\ntranscript:\n%s\n", n, strings.TrimSpace(res.Result.Result.Value))
}
