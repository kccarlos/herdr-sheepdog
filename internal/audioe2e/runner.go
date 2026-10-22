package audioe2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wav"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// Frame is the streaming frame: 40 ms of 16 kHz PCM16.
const (
	FrameSamples = Rate / 25
	frameDur     = 40 * time.Millisecond
	// The apps' silence gate (docs/dev/ws-contract.md "Silence gate").
	gateFloor   = 8
	gateHoldDur = 300 * time.Millisecond
	gatePreroll = 100 * time.Millisecond
)

// Options configure a run.
type Options struct {
	// BaseURL is the daemon, e.g. http://127.0.0.1:7799.
	BaseURL string
	// CacheDir holds the rendered speech.
	CacheDir string
	// FixtureDir is testdata/herdr (screen fixtures for inject_blocked).
	FixtureDir string
	// Provider is the voice provider the daemon runs (from /api/hello).
	Provider string
	// DefaultVoice is the `say` voice.
	DefaultVoice string
	// Save, when set, writes <Save>/<scenario>.in.wav and .out.wav.
	Save string
	// Log receives progress lines.
	Log io.Writer
	// FloorDBFS adds a white-noise floor at this level to every frame,
	// like a real microphone's self-noise (0 = digital silence, which
	// makes the apps' silence gate close between utterances).
	FloorDBFS float64
	// RecordAudioTimeline, when set, fills Result.AudioChunks with every
	// server→client binary frame's arrival offset and size — the raw
	// input for an offline jitter-buffer replay (docs/dev/history/audio-e2e-results.md).
	RecordAudioTimeline bool
}

// Timed is one recorded item at an offset from the scenario start.
type Timed struct {
	AtMS int64  `json:"at_ms"`
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

// AudioChunk is one binary /ws frame's arrival, as seen by the browser: the
// offset from the scenario start and its byte length. Captured for every
// frame (not just burst starts, unlike Timeline's "audio" entries) so the
// sequence can be replayed offline against the page's jitter-buffer code
// to count underruns at a given prebuffer (docs/dev/history/audio-e2e-results.md,
// the audible-gaps investigation).
type AudioChunk struct {
	AtMS  int64 `json:"at_ms"`
	Bytes int   `json:"bytes"`
}

// Event is one daemon event seen on /events during the scenario.
type Event struct {
	AtMS    int64          `json:"at_ms"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Result is one scenario's outcome.
type Result struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Pass     bool     `json:"pass"`
	Failures []string `json:"failures,omitempty"`

	InTranscript  string   `json:"in_transcript"`
	OutTranscript string   `json:"out_transcript"`
	Tools         []string `json:"tools,omitempty"`
	AudioOutBytes int      `json:"audio_out_bytes"`
	Flushes       int      `json:"flushes"`
	// NoiseTranscripts are input transcripts that arrived while only
	// noise or silence was being streamed (hallucinations).
	NoiseTranscripts []string `json:"noise_transcripts,omitempty"`

	// Latencies from the end of the last speech segment (ms; -1 = never).
	EOSToFirstIn    int64 `json:"eos_to_first_in_ms"`
	EOSToTool       int64 `json:"eos_to_tool_ms"`
	EOSToFirstAudio int64 `json:"eos_to_first_audio_ms"`
	EOSToFirstOut   int64 `json:"eos_to_first_out_ms"`

	// GPT-Live bookkeeping seen during the scenario.
	SessionsOpened int     `json:"sessions_opened"`
	SessionsClosed []any   `json:"sessions_closed,omitempty"`
	BilledTotalS   float64 `json:"billed_total_s"`

	// EOSMS is the end of the last speech (ms from the start), the
	// origin of the latencies and of the absent windows.
	EOSMS int64 `json:"eos_ms"`
	// Turns are a multi-turn scenario's per-turn outcomes.
	Turns []TurnResult `json:"turns,omitempty"`

	DurMS    int64   `json:"dur_ms"`
	Timeline []Timed `json:"timeline"`
	Events   []Event `json:"events"`
	// AudioChunks is the full per-frame arrival timeline of server→client
	// binary frames (see AudioChunk); only populated when the caller asks
	// (Options.RecordAudioTimeline), since it can be large.
	AudioChunks []AudioChunk `json:"audio_chunks,omitempty"`
}

// chunk is a rendered segment.
type chunk struct {
	samples    []int16
	speech     bool
	afterReply bool
}

// recorder collects what the daemon sends.
type recorder struct {
	mu          sync.Mutex
	t0          time.Time
	timeline    []Timed
	audioChunks []AudioChunk // only appended to when recordTimeline is set
	audio       int
	outAudio    []byte
	firstAt     time.Time // first reply audio
	lastAudio   time.Time
	lastOut     time.Time
	flushes     int
	events      []Event
	inText      strings.Builder
	outText     strings.Builder
	// streamingNoise is true while only noise/silence is streamed.
	streamingNoise   bool
	noiseTranscripts []string
	// recordTimeline mirrors Options.RecordAudioTimeline.
	recordTimeline bool
}

func (r *recorder) at(t time.Time) int64 { return t.Sub(r.t0).Milliseconds() }

// Run plays one scenario against the daemon.
func Run(ctx context.Context, sc Scenario, o Options) (Result, error) {
	res := Result{Name: sc.Name, Provider: o.Provider, EOSToFirstIn: -1, EOSToTool: -1, EOSToFirstAudio: -1, EOSToFirstOut: -1}
	// Every turn's segments render as one stream (one noise bed, one SNR
	// reference); turnOf says which turn each chunk belongs to.
	turns := sc.turns()
	flat := sc
	flat.Segments = nil
	var turnOf []int
	for i, t := range turns {
		flat.Segments = append(flat.Segments, t.Segments...)
		for range t.Segments {
			turnOf = append(turnOf, i)
		}
	}
	chunks, err := render(ctx, flat, o)
	if err != nil {
		return res, err
	}
	rec := &recorder{t0: time.Now(), recordTimeline: o.RecordAudioTimeline}
	sctx, stopSSE := context.WithCancel(ctx)
	defer stopSSE()
	sseReady := make(chan struct{})
	go streamEvents(sctx, o, rec, sseReady)
	select {
	case <-sseReady:
	case <-time.After(5 * time.Second):
		return res, fmt.Errorf("/events did not connect")
	}

	conn, err := dialWS(ctx, o)
	if err != nil {
		return res, err
	}
	defer func() { _ = conn.CloseNow() }()
	readDone := make(chan struct{})
	go readWS(ctx, conn, rec, readDone)
	send := func(m wsproto.Message) error {
		b, _ := m.Encode()
		return conn.Write(ctx, websocket.MessageText, b)
	}
	if err := send(wsproto.Mute(false)); err != nil {
		return res, err
	}
	// Let whatever the previous scenario left in flight finish (a late
	// reply would otherwise be scored here): wait until the daemon has
	// been quiet for 1.5 s (at most 12 s), with the client gate closed.
	if err := send(wsproto.Silence(true)); err != nil {
		return res, err
	}
	settleEnd := time.Now().Add(12 * time.Second)
	for time.Now().Before(settleEnd) {
		rec.mu.Lock()
		last := rec.lastAudio
		if rec.lastOut.After(last) {
			last = rec.lastOut
		}
		rec.mu.Unlock()
		if time.Since(last) >= 1500*time.Millisecond && time.Since(rec.t0) >= 1500*time.Millisecond {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The scenario starts now: drop what the settle saw.
	rec.mu.Lock()
	rec.t0 = time.Now()
	// Keep the voice session's own open/close from the settle window:
	// a GPT-Live idle close that lands while the scenario settles is
	// still this scenario's to observe. Everything else belongs to the
	// previous one.
	var kept []Event
	for _, ev := range rec.events {
		if strings.HasPrefix(ev.Type, "voice.session_") {
			ev.AtMS = -1 // seen while settling
			kept = append(kept, ev)
		}
	}
	rec.timeline, rec.events = nil, kept
	rec.audio, rec.outAudio, rec.flushes = 0, nil, 0
	rec.audioChunks = nil
	rec.firstAt, rec.lastAudio, rec.lastOut = time.Time{}, time.Time{}, time.Time{}
	rec.inText.Reset()
	rec.outText.Reset()
	rec.noiseTranscripts = nil
	rec.mu.Unlock()
	for _, st := range sc.Setup {
		if err := setup(ctx, st, o); err != nil {
			return res, fmt.Errorf("setup: %w", err)
		}
	}
	g0 := true

	// The client's capture pipeline: the silence gate in front of /ws.
	g := &gate{closed: g0, send: send, write: func(pcm []byte) error { return conn.Write(ctx, websocket.MessageBinary, pcm) }}
	var inWAV []int16
	var eos time.Time
	var speechStart time.Time
	var turnSpeech, turnEOS time.Time // the current turn's
	tick := time.NewTicker(frameDur)
	defer tick.Stop()
	spoke := false
	play := func(c chunk) error {
		// A transcript before any speech is a hallucination (after
		// speech, transcripts legitimately trail it).
		spoke = spoke || c.speech
		rec.mu.Lock()
		rec.streamingNoise = !spoke
		rec.mu.Unlock()
		for i := 0; i < len(c.samples); i += FrameSamples {
			j := min(i+FrameSamples, len(c.samples))
			frame := c.samples[i:j]
			<-tick.C
			if c.speech && speechStart.IsZero() {
				speechStart = time.Now()
			}
			if c.speech && turnSpeech.IsZero() {
				turnSpeech = time.Now()
			}
			if err := g.push(frame); err != nil {
				return err
			}
			inWAV = append(inWAV, frame...)
			if c.speech && Peak(frame) >= 300 {
				eos = time.Now()
				turnEOS = eos
			}
		}
		if c.speech {
			// What follows answers this speech: an announcement played
			// before it is not the reply.
			rec.mu.Lock()
			rec.firstAt = time.Time{}
			rec.mu.Unlock()
		}
		return nil
	}
	bed := bedFor(sc, chunks)
	idle := func(d time.Duration) error {
		// Nothing to say: the room (the bed) or digital silence.
		n := int(d / frameDur)
		samples := make([]int16, n*FrameSamples)
		if bed != nil {
			samples = bed(len(samples))
		}
		return play(chunk{samples: samples})
	}

	if o.FloorDBFS < 0 {
		for i := range chunks {
			n, _ := NoiseSamples("white", len(chunks[i].samples), int64(i)+500)
			chunks[i].samples = Mix(chunks[i].samples, n, dbfsToRMS(o.FloorDBFS))
		}
		inner := bed
		floorSeed := int64(900)
		bed = func(n int) []int16 {
			base := make([]int16, n)
			if inner != nil {
				base = inner(n)
			}
			floorSeed++
			fl, _ := NoiseSamples("white", n, floorSeed)
			return Mix(base, fl, dbfsToRMS(o.FloorDBFS))
		}
	}
	var cancelOnce sync.Once
	var windows []turnWindow
	ci := 0
	for ti, turn := range turns {
		turnStart := time.Now()
		turnSpeech, turnEOS = time.Time{}, time.Time{}
		for ; ci < len(chunks) && turnOf[ci] == ti; ci++ {
			c := chunks[ci]
			if c.afterReply {
				deadline := time.Now().Add(25 * time.Second)
				for {
					rec.mu.Lock()
					first := rec.firstAt
					rec.mu.Unlock()
					if !first.IsZero() && time.Since(first) >= time.Duration(sc.SpeakAfterReplyMS)*time.Millisecond {
						break
					}
					if time.Now().After(deadline) {
						break
					}
					if err := idle(frameDur); err != nil {
						return res, err
					}
				}
			}
			if err := play(c); err != nil {
				return res, err
			}
		}

		// The turn's tail: listen, cancelling if asked, ending once the
		// reply is over.
		tail := time.Duration(turn.TailMS) * time.Millisecond
		if tail <= 0 {
			tail = 10 * time.Second
		}
		quiet := time.Duration(turn.QuietMS) * time.Millisecond
		if quiet <= 0 {
			quiet = 2500 * time.Millisecond
		}
		end := time.Now().Add(tail)
		for time.Now().Before(end) {
			rec.mu.Lock()
			first, lastA, lastO := rec.firstAt, rec.lastAudio, rec.lastOut
			rec.mu.Unlock()
			if sc.CancelAfterMS > 0 && !first.IsZero() && time.Since(first) >= time.Duration(sc.CancelAfterMS)*time.Millisecond {
				cancelOnce.Do(func() {
					rec.mu.Lock()
					rec.timeline = append(rec.timeline, Timed{AtMS: rec.at(time.Now()), Kind: "cancel_sent"})
					rec.mu.Unlock()
					_ = send(wsproto.Cancel())
				})
			}
			if !turn.FullTail && !first.IsZero() && time.Since(lastA) >= quiet && (lastO.IsZero() || time.Since(lastO) >= quiet) {
				break
			}
			if err := idle(frameDur); err != nil {
				return res, err
			}
		}
		w := turnWindow{start: rec.at(turnStart), speechStart: rec.at(turnSpeech), spoke: !turnSpeech.IsZero()}
		if turnEOS.IsZero() {
			w.eos = rec.at(time.Now())
		} else {
			w.eos = rec.at(turnEOS)
		}
		if n := len(windows); n > 0 {
			windows[n-1].end = w.start
		}
		windows = append(windows, w)
	}
	time.Sleep(700 * time.Millisecond) // trailing events
	_ = conn.Close(websocket.StatusNormalClosure, "audio-e2e done")
	stopSSE()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	res.DurMS = time.Since(rec.t0).Milliseconds()
	res.InTranscript = strings.TrimSpace(rec.inText.String())
	res.OutTranscript = strings.TrimSpace(rec.outText.String())
	res.AudioOutBytes = rec.audio
	res.Flushes = rec.flushes
	res.NoiseTranscripts = rec.noiseTranscripts
	res.Timeline = rec.timeline
	res.Events = rec.events
	res.AudioChunks = rec.audioChunks
	if eos.IsZero() {
		eos = time.Now() // no speech: latencies stay -1 below
	}
	eosMS := rec.at(eos)
	res.EOSMS = eosMS
	res.EOSToFirstIn, res.EOSToTool, res.EOSToFirstAudio, res.EOSToFirstOut = latencies(rec.timeline, rec.events, rec.at(speechStart), eosMS, !speechStart.IsZero())
	for _, ev := range rec.events {
		switch ev.Type {
		case "voice.tool_call":
			name, _ := ev.Payload["name"].(string)
			args, _ := ev.Payload["args"].(map[string]any)
			text, _ := args["text"].(string)
			res.Tools = append(res.Tools, strings.TrimSpace(name+" "+quoteIf(text)))
		case "voice.session_open":
			res.SessionsOpened++
		case "voice.session_close":
			res.SessionsClosed = append(res.SessionsClosed, map[string]any{"reason": ev.Payload["reason"], "dur_ms": ev.Payload["dur_ms"], "billed_s": ev.Payload["billed_s"]})
			if v, ok := ev.Payload["billed_total_s"].(float64); ok {
				res.BilledTotalS = v
			}
		}
	}
	if o.Save != "" {
		_ = os.MkdirAll(o.Save, 0o755)
		_ = writeWAV(filepath.Join(o.Save, sc.Name+".in.wav"), ToPCM(inWAV), Rate)
		_ = writeWAV(filepath.Join(o.Save, sc.Name+".out.wav"), rec.outAudio, wsproto.ServerSampleRate)
	}
	res.Failures = check(sc.Expect, &res, o.Provider)
	var turnFails []string
	res.Turns, turnFails = checkTurns(sc, &res, windows, o.Provider)
	res.Failures = append(res.Failures, turnFails...)
	res.Pass = len(res.Failures) == 0
	return res, nil
}

func quoteIf(s string) string {
	if s == "" {
		return ""
	}
	return fmt.Sprintf("%q", s)
}

// render turns the segments into sample chunks, the bed mixed in.
func render(ctx context.Context, sc Scenario, o Options) ([]chunk, error) {
	var chunks []chunk
	var speech []int16
	for _, seg := range sc.Segments {
		var c chunk
		switch {
		case seg.Say != "":
			v := seg.Voice
			if v == "" {
				v = o.DefaultVoice
			}
			s, err := Say(ctx, seg.Say, v, o.CacheDir)
			if err != nil {
				return nil, err
			}
			c = chunk{samples: s, speech: seg.Speech == nil || *seg.Speech}
			speech = append(speech, s...)
		case seg.SilenceMS > 0:
			c = chunk{samples: make([]int16, seg.SilenceMS*Rate/1000)}
		case seg.NoiseMS > 0:
			kind := seg.NoiseKind
			if kind == "" && sc.Noise != nil {
				kind = sc.Noise.Kind
			}
			n, err := NoiseSamples(kind, seg.NoiseMS*Rate/1000, 99)
			if err != nil {
				return nil, err
			}
			db := seg.NoiseDBFS
			if db == 0 {
				db = -45
			}
			c = chunk{samples: Mix(make([]int16, len(n)), n, dbfsToRMS(db))}
		default:
			return nil, fmt.Errorf("scenario %s: empty segment", sc.Name)
		}
		c.afterReply = seg.AfterReply
		chunks = append(chunks, c)
	}
	if sc.Noise != nil {
		level := dbfsToRMS(sc.Noise.DBFS)
		if sc.Noise.SNRDB != nil && len(speech) > 0 {
			level = VoicedRMS(speech) / math.Pow(10, *sc.Noise.SNRDB/20)
		}
		total := 0
		for _, c := range chunks {
			total += len(c.samples)
		}
		bed, err := NoiseSamples(sc.Noise.Kind, total, sc.Noise.Seed)
		if err != nil {
			return nil, err
		}
		off := 0
		for i := range chunks {
			n := len(chunks[i].samples)
			chunks[i].samples = Mix(chunks[i].samples, bed[off:off+n], level)
			off += n
		}
	}
	return chunks, nil
}

// bedFor returns a generator of more bed noise for the idle/tail frames
// (nil without a bed: digital silence).
func bedFor(sc Scenario, chunks []chunk) func(n int) []int16 {
	if sc.Noise == nil {
		return nil
	}
	level := dbfsToRMS(sc.Noise.DBFS)
	if sc.Noise.SNRDB != nil {
		var speech []int16
		for _, c := range chunks {
			if c.speech {
				speech = append(speech, c.samples...)
			}
		}
		if len(speech) > 0 {
			level = VoicedRMS(speech) / math.Pow(10, *sc.Noise.SNRDB/20)
		}
	}
	seed := sc.Noise.Seed + 1000
	return func(n int) []int16 {
		seed++
		noise, _ := NoiseSamples(sc.Noise.Kind, n, seed)
		return Mix(make([]int16, n), noise, level)
	}
}

// gate is the client's silence gate: frames whose peak stays below the
// floor for 300 ms close it (silence on, no frames); the next loud frame
// reopens it (silence off) after the last 100 ms of withheld audio.
type gate struct {
	send     func(wsproto.Message) error
	write    func([]byte) error
	closed   bool
	quietFor time.Duration
	preroll  [][]int16
}

func (g *gate) push(frame []int16) error {
	loud := Peak(frame) >= gateFloor
	if g.closed {
		g.preroll = append(g.preroll, frame)
		if len(g.preroll) > int(gatePreroll/frameDur) {
			g.preroll = g.preroll[1:]
		}
		if !loud {
			return nil
		}
		g.closed = false
		g.quietFor = 0
		if err := g.send(wsproto.Silence(false)); err != nil {
			return err
		}
		for _, f := range g.preroll {
			if err := g.write(ToPCM(f)); err != nil {
				return err
			}
		}
		g.preroll = nil
		return nil
	}
	if loud {
		g.quietFor = 0
	} else {
		g.quietFor += frameDur
		if g.quietFor >= gateHoldDur {
			g.closed = true
			return g.send(wsproto.Silence(true))
		}
	}
	return g.write(ToPCM(frame))
}

func dialWS(ctx context.Context, o Options) (*websocket.Conn, error) {
	u := "ws" + strings.TrimPrefix(o.BaseURL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		return nil, fmt.Errorf("dial /ws: %w", err)
	}
	conn.SetReadLimit(1 << 22)
	return conn, nil
}

func readWS(ctx context.Context, conn *websocket.Conn, rec *recorder, done chan struct{}) {
	defer close(done)
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		now := time.Now()
		rec.mu.Lock()
		if typ == websocket.MessageBinary {
			if rec.audio == 0 || now.Sub(rec.lastAudio) > 1500*time.Millisecond {
				rec.timeline = append(rec.timeline, Timed{AtMS: rec.at(now), Kind: "audio"})
			}
			if rec.recordTimeline {
				rec.audioChunks = append(rec.audioChunks, AudioChunk{AtMS: rec.at(now), Bytes: len(data)})
			}
			if rec.firstAt.IsZero() {
				rec.firstAt = now
			}
			rec.audio += len(data)
			rec.outAudio = append(rec.outAudio, data...)
			rec.lastAudio = now
			rec.mu.Unlock()
			continue
		}
		m, err := wsproto.Parse(data)
		if err != nil {
			rec.mu.Unlock()
			continue
		}
		switch m.Type {
		case wsproto.TypeTranscript:
			if m.Text == "" && m.Dir == wsproto.DirIn && m.Final != nil && *m.Final {
				// An utterance ended (GPT-Live's empty final): keep the
				// next one's words apart.
				if rec.inText.Len() > 0 {
					rec.inText.WriteString(" ")
				}
			}
			if m.Text != "" {
				rec.timeline = append(rec.timeline, Timed{AtMS: rec.at(now), Kind: m.Dir, Text: m.Text})
				if m.Dir == wsproto.DirIn {
					rec.inText.WriteString(m.Text)
					if rec.streamingNoise {
						rec.noiseTranscripts = append(rec.noiseTranscripts, m.Text)
					}
				} else {
					rec.outText.WriteString(m.Text)
					rec.lastOut = now
				}
			}
		case wsproto.TypeFlush:
			rec.flushes++
			rec.timeline = append(rec.timeline, Timed{AtMS: rec.at(now), Kind: "flush"})
		case wsproto.TypeStatus:
			rec.timeline = append(rec.timeline, Timed{AtMS: rec.at(now), Kind: "status", Text: strings.TrimSpace(m.Voice + " " + m.Reason + " " + m.Provider)})
		case wsproto.TypeError:
			rec.timeline = append(rec.timeline, Timed{AtMS: rec.at(now), Kind: "error", Text: m.Message})
		}
		rec.mu.Unlock()
	}
}

// streamEvents follows /events and records the events of this scenario.
func streamEvents(ctx context.Context, o Options, rec *recorder, ready chan struct{}) {
	u := o.BaseURL + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	close(ready)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var ev struct {
			TS      time.Time      `json:"ts"`
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(line[6:], &ev) != nil || ev.Type == "" {
			continue
		}
		rec.mu.Lock()
		if !ev.TS.Before(rec.t0.Add(-200 * time.Millisecond)) {
			rec.events = append(rec.events, Event{AtMS: rec.at(ev.TS), Type: ev.Type, Payload: ev.Payload})
		}
		rec.mu.Unlock()
	}
}

func setup(ctx context.Context, st Setup, o Options) error {
	if st.WaitMS > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(st.WaitMS) * time.Millisecond):
		}
	}
	if b := st.InjectBlocked; b != nil {
		screen, err := os.ReadFile(filepath.Join(o.FixtureDir, b.ScreenFixture))
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]any{"pane_id": b.PaneID, "alias": b.Alias, "kind": b.Kind, "screen": string(screen)})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/inject/blocked", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("inject blocked: %d %s", resp.StatusCode, out)
		}
	}
	return nil
}

// Hello reads /api/hello (the provider under test).
func Hello(ctx context.Context, baseURL string) (provider, model string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/hello", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct {
		VoiceProvider string `json:"voice_provider"`
		VoiceModel    string `json:"voice_model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return "", "", err
	}
	return h.VoiceProvider, h.VoiceModel, nil
}

func writeWAV(path string, pcm []byte, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w, err := wav.NewWriter(f, rate)
	if err != nil {
		return err
	}
	if _, err := w.Write(pcm); err != nil {
		return err
	}
	return w.Close()
}
