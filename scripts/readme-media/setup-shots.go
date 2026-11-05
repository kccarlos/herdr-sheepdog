//go:build ignore

// setup-shots captures the web setup page (`sheepdog setup` opens it) from a
// private daemon for the README's setup.gif (scripts/readme-media/setup.sh).
// It starts a headless Chromium (Brave, Chrome, Edge) with a throwaway
// profile, walks the page (a fake key, the checks, the pairing QR) and saves
// one PNG per step in -out. The key check is answered by the tool itself, so
// a fake key shows "Valid key" and nothing is saved or sent anywhere.
//
//	go run scripts/readme-media/setup-shots.go -browser <binary> -url http://127.0.0.1:18000/setup -out DIR
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
	qrcode "github.com/skip2/go-qrcode"
)

type cdp struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	nextID int
	waits  map[int]chan json.RawMessage
	onEv   func(method string, params json.RawMessage)
}

func (c *cdp) call(ctx context.Context, method string, params any) json.RawMessage {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.waits[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		log.Fatal(err)
	}
	select {
	case r := <-ch:
		var e struct{ Error *struct{ Message string } }
		if json.Unmarshal(r, &e) == nil && e.Error != nil {
			log.Fatalf("%s: %s", method, e.Error.Message)
		}
		return r
	case <-time.After(20 * time.Second):
		log.Fatalf("%s: no reply", method)
	}
	return nil
}

func (c *cdp) read(ctx context.Context) {
	for {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.ID != nil {
			c.mu.Lock()
			ch := c.waits[*m.ID]
			delete(c.waits, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- b
			}
			continue
		}
		if c.onEv != nil {
			go c.onEv(m.Method, m.Params)
		}
	}
}

func main() {
	browser := flag.String("browser", "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", "Chromium binary")
	url := flag.String("url", "", "the setup page")
	out := flag.String("out", "shots", "output dir")
	port := flag.Int("port", 9334, "DevTools port")
	width := flag.Int("width", 760, "viewport width (CSS px)")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	profile, err := os.MkdirTemp("", "setup-shots-profile-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(profile)
	cmd := exec.Command(*browser, "--headless=new", "--user-data-dir="+profile, fmt.Sprintf("--remote-debugging-port=%d", *port),
		"--no-first-run", "--no-default-browser-check", "--hide-scrollbars", "--mute-audio", "about:blank")
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	c := &cdp{conn: conn, waits: map[int]chan json.RawMessage{}}
	c.onEv = func(method string, params json.RawMessage) {
		if method != "Fetch.requestPaused" {
			return
		}
		var p struct {
			RequestId string
			Request   struct{ Url string }
		}
		_ = json.Unmarshal(params, &p)
		// The answers are made up: any key is "valid" (the real check asks the
		// provider), and the pairing code is a QR of the project's address, so
		// no real address or one-time code is on screen.
		ctype, body := "application/json", []byte(`{"status":"ok","detail":"valid"}`)
		switch {
		case strings.Contains(p.Request.Url, "voice-models"):
			body = []byte(`{"models":["gemini-3.8-live","gemini-3.5-flash-live"],"default":"gemini-3.8-live"}`)
		case strings.Contains(p.Request.Url, "qr.svg"):
			png, err := qrcode.Encode("https://github.com/kccarlos/herdr-sheepdog", qrcode.Medium, 220)
			if err != nil {
				log.Fatal(err)
			}
			ctype, body = "image/png", png
		}
		c.call(ctx, "Fetch.fulfillRequest", map[string]any{"requestId": p.RequestId, "responseCode": 200,
			"responseHeaders": []map[string]string{{"name": "Content-Type", "value": ctype}}, "body": base64.StdEncoding.EncodeToString(body)})
	}
	go c.read(ctx)

	eval := func(js string) {
		r := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": js, "awaitPromise": true, "returnByValue": true})
		var e struct {
			Result struct{ ExceptionDetails *struct{ Text string } }
		}
		if json.Unmarshal(r, &e) == nil && e.Result.ExceptionDetails != nil {
			log.Fatalf("eval failed: %s: %s", e.Result.ExceptionDetails.Text, js)
		}
	}
	wait := func(cond string) {
		for i := 0; i < 100; i++ {
			var r struct {
				Result struct{ Result struct{ Value bool } }
			}
			_ = json.Unmarshal(c.call(ctx, "Runtime.evaluate", map[string]any{"expression": "!!(" + cond + ")", "returnByValue": true}), &r)
			if r.Result.Result.Value {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		log.Fatalf("timed out waiting for: %s", cond)
	}
	// shot saves the elements matching sel (their union, with a margin) as name.png.
	shot := func(name, sel string, height int) {
		var r struct {
			Result struct{ Result struct{ Value string } }
		}
		// The clip is as wide as the page's column, starts a little above the
		// first element (0: the top of the page) and is height CSS px tall.
		_ = json.Unmarshal(c.call(ctx, "Runtime.evaluate", map[string]any{"returnByValue": true, "expression": fmt.Sprintf(`(() => {
			const m = document.querySelector('main').getBoundingClientRect();
			const top = Math.min(...[...document.querySelectorAll(%q)].map((e) => e.getBoundingClientRect().top)) + scrollY;
			return JSON.stringify({ x: m.left, y: %q === 'top' ? 0 : Math.max(0, top - 12), width: m.width, height: %d });
		})()`, sel, sel, height)}), &r)
		var clip map[string]float64
		if err := json.Unmarshal([]byte(r.Result.Result.Value), &clip); err != nil {
			log.Fatalf("%s: no clip for %s", name, sel)
		}
		clip["scale"] = 1
		var s struct{ Result struct{ Data string } }
		_ = json.Unmarshal(c.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "clip": clip, "captureBeyondViewport": true}), &s)
		b, _ := base64.StdEncoding.DecodeString(s.Result.Data)
		if err := os.WriteFile(filepath.Join(*out, name+".png"), b, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("shot", name)
	}

	c.call(ctx, "Page.enable", map[string]any{})
	c.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": *width, "height": 1400, "deviceScaleFactor": 1, "mobile": false})
	c.call(ctx, "Emulation.setEmulatedMedia", map[string]any{"features": []map[string]string{{"name": "prefers-color-scheme", "value": "light"}}})
	c.call(ctx, "Fetch.enable", map[string]any{"patterns": []map[string]string{{"urlPattern": "*/api/setup/validate*"}, {"urlPattern": "*/api/setup/voice-models*"}, {"urlPattern": "*/api/pair/qr.svg*"}}})
	c.call(ctx, "Page.navigate", map[string]any{"url": *url})
	wait(`document.querySelector('#start-grid') && document.querySelector('#start-grid').children.length > 0`)
	time.Sleep(time.Second)
	// Nothing real on screen: the page shows the private daemon's paths.
	eval(`document.querySelectorAll('code').forEach((e) => { e.textContent = e.textContent.replace(/\/tmp\/[^ ]*\/\.config\/sheepdog/, '~/.config/sheepdog'); })`)
	eval(`const st = document.createElement('style'); st.textContent = '#addresses, #remote-hint { display: none }'; document.head.appendChild(st)`)
	time.Sleep(300 * time.Millisecond)
	shot("1-models", "top", 640)
	// A fake key, "checked" by the stub above.
	eval(`document.querySelector('#gemini-key').focus()`)
	c.call(ctx, "Input.insertText", map[string]any{"text": "AIzaSyFAKE-readme-example-key-0000000"})
	wait(`document.querySelector('#gemini-result').textContent.includes('Valid')`)
	time.Sleep(500 * time.Millisecond)
	shot("2-key", "#voice-set", 640)
	// Phone access on: the pairing QR (a throwaway daemon's one-time code).
	eval(`document.querySelector('#phone-enable').click()`)
	wait(`document.querySelector('#qr') && document.querySelector('#qr').complete && document.querySelector('#qr').naturalWidth > 0 && !document.querySelector('#phone-on').classList.contains('hidden')`)
	time.Sleep(500 * time.Millisecond)
	shot("3-pair", "#step-phone", 640)
	var bg struct {
		Result struct{ Result struct{ Value string } }
	}
	_ = json.Unmarshal(c.call(ctx, "Runtime.evaluate", map[string]any{"returnByValue": true, "expression": "getComputedStyle(document.body).backgroundColor"}), &bg)
	fmt.Println("background", bg.Result.Result.Value)
}
