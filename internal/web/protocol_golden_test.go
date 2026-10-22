package web

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// Protocol goldens (docs/dev/audit-2026-10-21.md, "Shared Go↔Swift
// protocol fixtures"): this test writes what the daemon puts on the wire
// into testdata/protocol/, and the iPhone app's ProtocolGoldenTests
// decode the same files, so a Go-side change that the app cannot read
// fails the Swift suite. The files are compared here on every run: change
// the wire and run
//
//	PROTOCOL_GOLDEN_UPDATE=1 go test ./internal/web -run TestProtocolGoldens
//
// (not a SHEEPDOG_* name: TestMain clears those)
//
// then commit testdata/protocol with the change.
//
//   - ws/server, ws/client: one /ws message per file, built from wsproto
//     values (docs/dev/ws-contract.md).
//   - term/server, term/client: the /term control messages
//     (docs/dev/term-contract.md).
//   - rest: /api/hello and /api/state answered by a test daemon, with
//     every time and the TLS pin replaced by fixed values.
const goldenDir = "../../testdata/protocol"

func TestProtocolGoldens(t *testing.T) {
	files := map[string][]byte{}
	put := func(name string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files[name] = b
	}

	// /ws, daemon to app.
	put("ws/server/status.json", wsproto.Status(wsproto.VoiceConnected, false, ""))
	put("ws/server/status-closed.json", wsproto.Status(wsproto.VoiceClosed, false, wsproto.ReasonProtocolTooOld))
	put("ws/server/transcript-in.json", wsproto.Transcript(wsproto.DirIn, "what is auth doing", true))
	put("ws/server/transcript-out.json", wsproto.Transcript(wsproto.DirOut, "Auth is running the tests.", false))
	put("ws/server/error.json", wsproto.Error("audio: the voice session is closed"))
	put("ws/server/error-account.json", wsproto.AccountError("The Gemini key ran out of quota.", "quota", "voice", "gemini", "Wait for the quota to reset, or add a key."))
	put("ws/server/flush.json", wsproto.Flush())
	put("ws/server/pong.json", wsproto.Pong())
	put("ws/server/bot-ready.json", wsproto.BotReady(wsproto.ProtocolVersion, wsproto.MinProtocolVersion, "v0.2.0", []string{
		wsproto.FeaturePlayback, wsproto.FeatureSpeaking, wsproto.FeatureSilence, wsproto.FeatureProvider, wsproto.FeatureHistory,
		wsproto.FeaturePush, wsproto.FeatureModels, wsproto.FeatureScreen, wsproto.FeatureScreenANSI, wsproto.FeatureAgentActions,
		wsproto.FeatureCards, wsproto.FeatureTerm, wsproto.FeatureFeedback,
	}))
	put("ws/server/screen.json", wsproto.ScreenFrame(wsproto.Screen{
		PaneID: "w1:p2", Alias: "auth", Spoken: "Auth fix", Kind: "codex", State: "working", StateSince: "2026-10-21T09:00:00Z",
		Text: "\x1b[1;32m•\x1b[0m Running go test ./...", Lines: 1, Hash: "77d1a90c3e2b4f05", ANSI: true,
	}))
	put("ws/server/screen-error.json", wsproto.ScreenError("w1:p9", "no such agent", true))
	put("ws/server/card-approval.json", wsproto.CardFrame(wsproto.Card{
		ID: "c-1", Kind: wsproto.CardKindApproval, At: "2026-10-21T09:00:00Z", Title: "Auth wants to run a command",
		Agent: "auth", Spoken: "Auth fix", PaneID: "w1:p2", PendingID: "p-1", Question: "Run go test ./...?", Command: "go test ./...",
		Options: []wsproto.CardOption{{Index: 1, Label: "Yes", Keys: []string{"1"}, Default: true}, {Index: 2, Label: "No", Keys: []string{"esc"}}},
	}))
	put("ws/server/card-markdown.json", wsproto.CardFrame(wsproto.Card{
		ID: "c-2", Kind: wsproto.CardKindCard, At: "2026-10-21T09:00:00Z", Title: "What changed", Markdown: "- `auth.go`: the token check",
	}))

	// /ws, app to daemon: what the app sends.
	put("ws/client/client-ready.json", wsproto.ClientReady(wsproto.ProtocolVersion, wsproto.ClientInfo{Name: "sheepdog-ios", Version: "0.2.0 (12)", Platform: "ios"}))
	put("ws/client/mute.json", wsproto.Mute(true))
	put("ws/client/silence.json", wsproto.Silence(false))
	put("ws/client/speaking.json", wsproto.Speaking(true))
	put("ws/client/playback.json", wsproto.Playback(wsproto.PlaybackDrained, 2400))
	put("ws/client/drain.json", wsproto.Drain("2026-10-21T09:00:00.250Z", 0, 1800, 2000, "dry"))
	put("ws/client/cancel.json", wsproto.Cancel())
	put("ws/client/text.json", wsproto.SimulateText("what is auth doing"))
	ping := wsproto.Ping()
	gated, peak, underruns, drains, buffer, stretched, rate := 120, 3400, 1, 4, 180, 2300, 0.97
	ping.GatedFrames, ping.Peak, ping.Underruns, ping.Drains, ping.BufferMs, ping.StretchedMs, ping.Rate = &gated, &peak, &underruns, &drains, &buffer, &stretched, &rate
	ping.AudioState = "running"
	put("ws/client/ping.json", ping)
	put("ws/client/screen-subscribe.json", wsproto.ScreenSubscribe("w1:p2"))
	put("ws/client/screen-unsubscribe.json", wsproto.ScreenUnsubscribe())
	put("ws/client/visibility.json", wsproto.Visibility(true, true))

	// /term control messages.
	one, col, row := 1, 12, 5
	put("term/client/term-hello.json", termproto.Message{Type: termproto.TypeHello, Protocol: &one, Client: &termproto.ClientInfo{Name: "Sheepdog", Version: "0.2.0", Platform: "iOS 26.1"}})
	put("term/client/term-open.json", termproto.Message{Type: termproto.TypeOpen, Ch: 1, Target: &termproto.Target{PaneID: "w1:p2"}, Mode: termproto.ModeControl, Cols: 58, Rows: 30})
	put("term/client/term-open-shell.json", termproto.Message{Type: termproto.TypeOpen, Ch: 3, Target: &termproto.Target{Shell: &termproto.Shell{Cwd: "/Users/me/app"}}, Mode: termproto.ModeControl, Cols: 58, Rows: 30})
	put("term/client/term-resize.json", termproto.Message{Type: termproto.TypeResize, Ch: 1, Cols: 44, Rows: 28})
	put("term/client/term-scroll.json", termproto.Message{Type: termproto.TypeScroll, Ch: 1, Dir: "down", Lines: 3, Col: &col, Row: &row})
	put("term/client/term-mouse.json", termproto.Message{Type: termproto.TypeMouse, Ch: 1, Action: "up", Button: "left", Col: &col, Row: &row})
	put("term/client/term-close.json", termproto.Message{Type: termproto.TypeClose, Ch: 1})
	put("term/server/term-ready.json", termproto.Ready(true))
	put("term/server/term-opened.json", termproto.Opened(1, "w1:p2", termproto.ModeControl, 58, 30, "agent"))
	put("term/server/term-closed.json", termproto.Closed(1, "released"))
	put("term/server/term-error.json", termproto.Error(1, "no control channel"))
	put("term/server/term-free.json", termproto.Free(2, "w1:p2"))

	// REST: a test daemon with one agent waiting on a question and one
	// working.
	h := newHarness(t, harnessOpts{voice: true, brain: true})
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "kind": "codex", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	trackWorking(t, h, "fake:2", "docs", "• Writing the README")
	files["rest/hello.json"] = normalizeGolden(t, getRaw(t, h, "/api/hello"))
	files["rest/state.json"] = normalizeGolden(t, getRaw(t, h, "/api/state"))

	for name, b := range files {
		var out bytes.Buffer
		if err := json.Indent(&out, b, "", "  "); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out.WriteByte('\n')
		files[name] = out.Bytes()
	}
	if os.Getenv("PROTOCOL_GOLDEN_UPDATE") != "" {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
		for name, b := range files {
			p := filepath.Join(goldenDir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	var onDisk []string
	_ = filepath.WalkDir(goldenDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(goldenDir, p)
			onDisk = append(onDisk, filepath.ToSlash(rel))
		}
		return nil
	})
	var want []string
	for name := range files {
		want = append(want, name)
	}
	slices.Sort(onDisk)
	slices.Sort(want)
	if !slices.Equal(onDisk, want) {
		t.Errorf("testdata/protocol has %v, want %v (run with PROTOCOL_GOLDEN_UPDATE=1)", onDisk, want)
	}
	for _, name := range want {
		got, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			continue
		}
		if !bytes.Equal(got, files[name]) {
			t.Errorf("testdata/protocol/%s is stale; the wire now says:\n%s\n(run with PROTOCOL_GOLDEN_UPDATE=1)", name, files[name])
		}
	}
}

// getRaw returns the body of GET path.
func getRaw(t *testing.T, h *harness, path string) []byte {
	t.Helper()
	resp, err := http.Get(h.http.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %v", path, resp.StatusCode, err)
	}
	return b
}

var (
	reGoldenTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$`)
	// rePendingID is a pending action's id, which carries the clock.
	rePendingID = regexp.MustCompile(`^ap-[0-9a-z]+-(\d+)$`)
)

// normalizeGolden makes a live answer reproducible: every RFC 3339 time
// becomes one fixed instant, a pending action's id loses its clock part,
// and the TLS pin (a fresh key per test daemon) is a fixed string. Keys keep the order of the Go structs.
func normalizeGolden(t *testing.T, b []byte) []byte {
	t.Helper()
	fixed := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out bytes.Buffer
	var walk func(key string) error
	walk = func(key string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			out.WriteString(v.String())
			end := json.Delim('}')
			if v == '[' {
				end = ']'
			}
			first := true
			for dec.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				k := ""
				if v == '{' {
					kt, err := dec.Token()
					if err != nil {
						return err
					}
					k = kt.(string)
					kb, _ := json.Marshal(k)
					out.Write(kb)
					out.WriteByte(':')
				}
				if err := walk(k); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			out.WriteString(end.String())
		case string:
			switch {
			case key == "pin":
				v = "sha256:pinned-at-pairing"
			case reGoldenTime.MatchString(v):
				v = fixed
			case rePendingID.MatchString(v):
				v = rePendingID.ReplaceAllString(v, "ap-golden-$1")
			}
			sb, _ := json.Marshal(v)
			out.Write(sb)
		case json.Number:
			out.WriteString(v.String())
		case bool:
			if v {
				out.WriteString("true")
			} else {
				out.WriteString("false")
			}
		case nil:
			out.WriteString("null")
		}
		return nil
	}
	if err := walk(""); err != nil {
		t.Fatalf("normalize: %v\n%s", err, b)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("normalize: empty")
	}
	return out.Bytes()
}
