//go:build ignore

// factory-shots captures README/guide screenshots of the /factory page from a
// private demo daemon (scripts/readme-media/factory-shots.sh). It starts a
// headless Chromium with a throwaway profile, visits each view at 1280 px wide
// (plus the dependency graph at 500 px), crops each shot to content, verifies
// no private /Users paths appear on screen, and saves PNGs into -out.
//
//	go run scripts/readme-media/factory-shots.go -browser <binary> -url http://127.0.0.1:17820/factory -out assets/readme/factory
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
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
	case <-time.After(30 * time.Second):
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
			ID *int `json:"id"`
		}
		if json.Unmarshal(b, &m) != nil || m.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.waits[*m.ID]
		delete(c.waits, *m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- b
		}
	}
}

func main() {
	browser := flag.String("browser", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Chromium binary")
	baseURL := flag.String("url", "http://127.0.0.1:17820/factory", "factory page URL")
	outDir := flag.String("out", "assets/readme/factory", "output dir")
	port := flag.Int("port", 9335, "DevTools port")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	profile, err := os.MkdirTemp("", "factory-shots-profile-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(profile)

	cmd := exec.Command(*browser,
		"--headless=new",
		"--user-data-dir="+profile,
		fmt.Sprintf("--remote-debugging-port=%d", *port),
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-scrollbars",
		"--mute-audio",
		"about:blank",
	)
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
	c := &cdp{conn: conn, waits: map[int]chan json.RawMessage{}}
	go c.read(ctx)

	evalStr := func(js string) string {
		r := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": js, "awaitPromise": true, "returnByValue": true})
		var res struct {
			Result struct {
				Result           struct{ Value any }
				ExceptionDetails *struct{ Text string }
			}
		}
		if err := json.Unmarshal(r, &res); err != nil {
			log.Fatalf("unmarshal eval: %v", err)
		}
		if res.Result.ExceptionDetails != nil {
			log.Fatalf("eval failed: %s\njs: %s", res.Result.ExceptionDetails.Text, js)
		}
		if s, ok := res.Result.Result.Value.(string); ok {
			return s
		}
		return ""
	}
	wait := func(cond string) {
		for i := 0; i < 150; i++ {
			r := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": "!!(" + cond + ")", "returnByValue": true})
			var res struct {
				Result struct{ Result struct{ Value bool } }
			}
			_ = json.Unmarshal(r, &res)
			if res.Result.Result.Value {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		log.Fatalf("timed out waiting for: %s", cond)
	}
	setViewport := func(width, height int) {
		c.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
			"width": width, "height": height, "deviceScaleFactor": 1, "mobile": false,
		})
	}
	setScheme := func(scheme string) {
		c.call(ctx, "Emulation.setEmulatedMedia", map[string]any{
			"features": []map[string]string{{"name": "prefers-color-scheme", "value": scheme}},
		})
	}
	apiBase := strings.TrimRight(strings.TrimSuffix(*baseURL, "/factory"), "/")
	wantStages := map[string]string{
		"T1": "done", "T2": "done", "T3": "done", "T4": "done",
		"T5": "working", "T6": "working", "T7": "done", "T8": "done",
		"T9": "done", "T10": "working", "T11": "blocked_or_failed", "T12": "waiting_on_dep",
		"T13": "ci", "T14": "queued", "T15": "owner_answer", "T16": "waiting_on_dep",
		"T17": "waiting_on_dep", "T18": "waiting_on_dep", "T19": "done", "T20": "waiting_on_dep",
	}
	wantAgents := map[string][2]string{
		"T5":  {"opus", "sd-notes-T5-r2"},
		"T6":  {"sonnet", "sd-notes-T6-1"},
		"T10": {"sonnet", "sd-notes-T10-1"},
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	verifyNoDrift := func(name string) {
		pResp, err := httpClient.Get(apiBase + "/api/factory/projects")
		if err != nil {
			log.Fatalf("%s: GET /api/factory/projects: %v", name, err)
		}
		var pv struct {
			Projects []struct {
				ID      string `json:"id"`
				State   string `json:"state"`
				Running bool   `json:"running"`
			} `json:"projects"`
		}
		err = json.NewDecoder(pResp.Body).Decode(&pv)
		pResp.Body.Close()
		if err != nil {
			log.Fatalf("%s: decode projects: %v", name, err)
		}
		byProj := map[string]struct {
			State   string
			Running bool
		}{}
		for _, p := range pv.Projects {
			byProj[p.ID] = struct {
				State   string
				Running bool
			}{p.State, p.Running}
		}
		if n := byProj["notes"]; n.State != "running" || !n.Running {
			log.Fatalf("%s: drift detected: notes project state=%q running=%v (want running/true)", name, n.State, n.Running)
		}
		if r := byProj["recipes"]; r.State != "finished" || r.Running {
			log.Fatalf("%s: drift detected: recipes project state=%q running=%v (want finished/false)", name, r.State, r.Running)
		}

		gResp, err := httpClient.Get(apiBase + "/api/factory/projects/notes/graph")
		if err != nil {
			log.Fatalf("%s: GET /api/factory/projects/notes/graph: %v", name, err)
		}
		var gv struct {
			Tasks []struct {
				Ref        string `json:"ref"`
				Stage      string `json:"stage"`
				WhyWaiting string `json:"why_waiting"`
				Model      string `json:"model"`
				Terminal   string `json:"terminal"`
			} `json:"tasks"`
		}
		err = json.NewDecoder(gResp.Body).Decode(&gv)
		gResp.Body.Close()
		if err != nil {
			log.Fatalf("%s: decode notes graph: %v", name, err)
		}
		if len(gv.Tasks) != len(wantStages) {
			log.Fatalf("%s: drift detected: got %d tasks, want %d", name, len(gv.Tasks), len(wantStages))
		}
		seenAgents := map[string][2]string{}
		for _, t := range gv.Tasks {
			want, ok := wantStages[t.Ref]
			if !ok {
				log.Fatalf("%s: drift detected: unexpected task %s", name, t.Ref)
			}
			if t.Stage != want {
				log.Fatalf("%s: drift detected: task %s stage=%q, want %q", name, t.Ref, t.Stage, want)
			}
			if t.WhyWaiting == "Project is stopped" {
				log.Fatalf("%s: drift detected: task %s still says Project is stopped", name, t.Ref)
			}
			if t.Terminal != "" {
				seenAgents[t.Ref] = [2]string{t.Model, t.Terminal}
			}
		}
		if len(seenAgents) != len(wantAgents) {
			log.Fatalf("%s: drift detected: running agents=%v, want %v", name, seenAgents, wantAgents)
		}
		for ref, want := range wantAgents {
			if got := seenAgents[ref]; got != want {
				log.Fatalf("%s: drift detected: agent %s=%v, want %v", name, ref, got, want)
			}
		}
	}
	checkPrivacy := func(name string) {
		txt := evalStr(`document.body.innerText || ''`)
		if strings.Contains(txt, "/Users/") {
			log.Fatalf("%s: leaked /Users/ path in visible text", name)
		}
	}
	capture := func(name string, clipJS string) {
		time.Sleep(350 * time.Millisecond)
		verifyNoDrift(name)
		checkPrivacy(name)
		raw := evalStr(clipJS)
		var clip map[string]float64
		if err := json.Unmarshal([]byte(raw), &clip); err != nil {
			log.Fatalf("%s: bad clip JSON %q: %v", name, raw, err)
		}
		clip["scale"] = 1
		var s struct{ Result struct{ Data string } }
		_ = json.Unmarshal(c.call(ctx, "Page.captureScreenshot", map[string]any{
			"format": "png", "clip": clip, "captureBeyondViewport": true,
		}), &s)
		b, err := base64.StdEncoding.DecodeString(s.Result.Data)
		if err != nil {
			log.Fatalf("%s: decode png: %v", name, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			log.Fatalf("%s: png config: %v", name, err)
		}
		outPath := filepath.Join(*outDir, name+".png")
		if err := os.WriteFile(outPath, b, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("shot %-18s %dx%d\n", name+".png", cfg.Width, cfg.Height)
	}

	// pageClip captures the full viewport width from y=0 down to the bottom of
	// endSel (or .page if empty), stopping cleanly in the gap before the next
	// section when cropping mid-page.
	pageClip := func(width int, endSel string) string {
		pad := 20
		if endSel != "" && endSel != ".page" {
			pad = 7
		}
		return fmt.Sprintf(`(() => {
			window.scrollTo(0, 0);
			const end = %q ? document.querySelector(%q) : document.querySelector('.page');
			const r = end.getBoundingClientRect();
			const h = Math.ceil(r.bottom + window.scrollY + %d);
			return JSON.stringify({ x: 0, y: 0, width: %d, height: h });
		})()`, endSel, endSel, pad, width)
	}

	c.call(ctx, "Page.enable", map[string]any{})
	setViewport(1280, 1200)
	setScheme("light")
	c.call(ctx, "Page.navigate", map[string]any{"url": *baseURL})
	wait(`!document.getElementById('board').hidden && document.querySelectorAll('.ov-card').length === 2`)

	// 1. Overview (light, 1280 px)
	capture("overview", pageClip(1280, ".page"))

	// 2. Needs-you list open (light, 1280 px)
	evalStr(`document.getElementById('ni-btn').click()`)
	wait(`document.querySelector('.ni-list') !== null`)
	capture("needs-you", pageClip(1280, ".page"))
	evalStr(`document.getElementById('ni-btn').click()`)
	wait(`document.querySelector('.ni-list') === null`)

	// 3. Notes board with GRAPH (light, 1280 px)
	evalStr(`location.hash = '#notes'`)
	wait(`document.querySelector('button[data-view="graph"]') !== null`)
	evalStr(`document.querySelector('button[data-view="graph"]').click()`)
	wait(`document.querySelector('.gbox svg') !== null`)
	capture("graph-light", pageClip(1280, ".gbox"))

	// 4. Notes board with GRAPH (dark, 1280 px)
	setScheme("dark")
	capture("graph-dark", pageClip(1280, ".gbox"))

	// 5. Notes board with GRAPH at 500 px wide (light)
	setScheme("light")
	setViewport(500, 1000)
	evalStr(`window.dispatchEvent(new Event('resize'))`)
	wait(`document.querySelector('.gbox') && document.querySelector('[data-ghint]') && !document.querySelector('[data-ghint]').hidden`)
	capture("graph-narrow", pageClip(500, ".gbox"))

	// 6. Notes board list view with step bars (light, 1280 px)
	setViewport(1280, 1200)
	evalStr(`window.dispatchEvent(new Event('resize'))`)
	evalStr(`document.querySelector('button[data-view="list"]').click()`)
	wait(`document.querySelectorAll('.sbar').length >= 10 && document.querySelector('details.done') !== null`)
	evalStr(`document.querySelector('details.done').open = true`)
	agentsTxt := evalStr(`document.getElementById('agents').innerText || ''`)
	if strings.Contains(agentsTxt, "Project is stopped") || !strings.Contains(agentsTxt, "3 running") ||
		!strings.Contains(agentsTxt, "opus") || !strings.Contains(agentsTxt, "sonnet") {
		log.Fatalf("board-list: unexpected agents card text: %q", agentsTxt)
	}
	capture("board-list", pageClip(1280, "details.done"))

	// 7. Settings panel (light, 1280 px viewport, cropped around #settings)
	wait(`document.querySelectorAll('#settings .srow').length >= 5`)
	capture("settings", `(() => {
		const el = document.getElementById('settings');
		const r = el.getBoundingClientRect();
		const pad = 16;
		return JSON.stringify({
			x: Math.max(0, Math.floor(r.left + window.scrollX - pad)),
			y: Math.max(0, Math.floor(r.top + window.scrollY - pad)),
			width: Math.ceil(r.width + 2 * pad),
			height: Math.ceil(r.height + 2 * pad),
		});
	})()`)

	// 8. Task view: steps list + its log (#notes/T5, light, 1280 px)
	evalStr(`location.hash = '#notes/T5'`)
	wait(`document.querySelector('ol.steps') !== null && document.querySelectorAll('ol.log li.ll').length >= 3`)
	capture("task-view", pageClip(1280, ".page"))

	// 9. Recipes log (#recipes, light, 1280 px)
	evalStr(`location.hash = '#recipes'`)
	wait(`document.querySelector('h1') && document.querySelectorAll('ol.log li.ll').length >= 10`)
	capture("recipes-log", pageClip(1280, "#main"))

	// 10. New-project form with start checks (light, 1280 px)
	evalStr(`location.hash = ''`)
	wait(`document.querySelector('button[data-new-project]') !== null`)
	evalStr(`document.querySelector('button[data-new-project]').click()`)
	wait(`document.getElementById('np-folder') !== null && document.querySelectorAll('.sugg button').length >= 1`)
	suggs := evalStr(`[...document.querySelectorAll('.sugg button')].map((b) => b.dataset.sugg).join(', ')`)
	fmt.Printf("folder suggestions: %s\n", suggs)
	if strings.Contains(suggs, "/Users/") {
		log.Fatalf("new-project: leaked /Users/ in folder suggestions: %s", suggs)
	}
	evalStr(`document.querySelector('button[data-sugg="/tmp/sheepdog-demo/notes"]').click()`)
	evalStr(`(() => {
		const g = document.getElementById('np-goal');
		g.value = 'Add full-text search and tag filters to the notes page';
		g.dispatchEvent(new Event('input', { bubbles: true }));
	})()`)
	evalStr(`document.querySelector('button[data-np-check]').click()`)
	wait(`document.querySelectorAll('.ck-list .ck-row').length >= 5`)
	capture("new-project", pageClip(1280, ".page"))
}
