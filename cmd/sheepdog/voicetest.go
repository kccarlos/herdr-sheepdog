package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/echotools"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wav"
)

const (
	inRate     = 16000
	outRate    = 24000
	frameDur   = 20 * time.Millisecond
	frameBytes = inRate * 2 / 50 // 20 ms of PCM16 mono
)

func printVoiceTestUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog voice-test [flags]")
	fprintln(w, "")
	fprintln(w, "Streams a 16 kHz PCM16 mono WAV into a Gemini Live session in real time (20 ms")
	fprintln(w, "frames), with the five voice tools stubbed by echo handlers, and writes every")
	fprintln(w, "24 kHz audio chunk the model returns to a WAV file. Session events are printed")
	fprintln(w, "to stderr as JSON lines; a summary goes to stdout on exit. Needs GEMINI_API_KEY")
	fprintln(w, "(or GEMINI_API_KEY_1..9, or gemini.api_keys in the config); with gemini.backend")
	fprintln(w, "= \"vertex\" (SHEEPDOG_GEMINI_BACKEND=vertex) VERTEX_API_KEY and VERTEX_PROJECT instead.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH       config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --in FILE           input WAV (16 kHz, PCM16, mono); silence is streamed after it ends")
	fprintln(w, "  --out FILE          output WAV (24 kHz) for the model's audio")
	fprintln(w, "  --text TEXT         simulated utterance sent at --text-after")
	fprintln(w, "  --text-after D      when to send --text (default 1s)")
	fprintln(w, "  --push TEXT         push sent at --push-after with --push-urgency")
	fprintln(w, "  --push-after D      when to send --push (default 3s)")
	fprintln(w, "  --push-urgency U    idle | interrupt | silent (default idle)")
	fprintln(w, "  --drop-after D      forcibly close the connection at D to exercise resumption")
	fprintln(w, "  --max D             hard stop (default 45s)")
	fprintln(w, "  --idle D            exit after this much quiet following the last turn (default 6s)")
	fprintln(w, "  --aliases LIST      comma-separated agent aliases (default auth,docs)")
}

// voiceTestOpts are the parsed flags.
type voiceTestOpts struct {
	configPath, in, out, text, push, pushUrgency, aliases string
	textAfter, pushAfter, dropAfter, maxDur, idle         time.Duration
}

func parseVoiceTestFlags(args []string, stderr io.Writer) (voiceTestOpts, int, bool) {
	fs := flag.NewFlagSet("voice-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printVoiceTestUsage(stderr) }
	var o voiceTestOpts
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.in, "in", "", "input WAV")
	fs.StringVar(&o.out, "out", "", "output WAV")
	fs.StringVar(&o.text, "text", "", "simulated utterance")
	fs.DurationVar(&o.textAfter, "text-after", time.Second, "when to send --text")
	fs.StringVar(&o.push, "push", "", "push text")
	fs.DurationVar(&o.pushAfter, "push-after", 3*time.Second, "when to push")
	fs.StringVar(&o.pushUrgency, "push-urgency", "idle", "push urgency")
	fs.DurationVar(&o.dropAfter, "drop-after", 0, "force a disconnect at this time")
	fs.DurationVar(&o.maxDur, "max", 45*time.Second, "hard stop")
	fs.DurationVar(&o.idle, "idle", 6*time.Second, "quiet period before exit")
	fs.StringVar(&o.aliases, "aliases", "auth,docs", "agent aliases")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.in == "" && o.text == "" && o.push == "" {
		fprintln(stderr, "sheepdog voice-test: need at least one of --in, --text, --push")
		return o, 2, false
	}
	return o, 0, true
}

func parseUrgency(s string) (voice.Urgency, error) {
	switch strings.ToLower(s) {
	case "idle", "":
		return voice.UrgencyIdle, nil
	case "interrupt":
		return voice.UrgencyInterrupt, nil
	case "silent":
		return voice.UrgencySilent, nil
	}
	return 0, fmt.Errorf("unknown urgency %q", s)
}

// runVoiceTest implements `sheepdog voice-test`.
func runVoiceTest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseVoiceTestFlags(args, stderr)
	if !run {
		return code
	}
	urgency, err := parseUrgency(o.pushUrgency)
	if err != nil {
		fprintf(stderr, "sheepdog voice-test: %v\n", err)
		return 2
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	liveKeys, vertex, missing := app.GeminiLive(cfg.Gemini)
	if missing != "" {
		fprintf(stderr, "sheepdog voice-test: no Gemini API key (backend %s: set %s)\n", cfg.Gemini.Backend, missing)
		return 2
	}

	var pcmIn []byte
	if o.in != "" {
		pcmIn, err = readInputWAV(o.in)
		if err != nil {
			fprintf(stderr, "sheepdog voice-test: %v\n", err)
			return 2
		}
	}
	var outW *wav.Writer
	if o.out != "" {
		f, err := os.Create(o.out)
		if err != nil {
			fprintf(stderr, "sheepdog voice-test: %v\n", err)
			return 1
		}
		defer func() { _ = f.Close() }()
		outW, err = wav.NewWriter(f, outRate)
		if err != nil {
			fprintf(stderr, "sheepdog voice-test: %v\n", err)
			return 1
		}
	}

	var aliases []string
	for _, a := range strings.Split(o.aliases, ",") {
		if a = strings.TrimSpace(a); a != "" {
			aliases = append(aliases, a)
		}
	}
	logger := logging.NewJSONLLogger(stderr)
	sess, err := gemini.New(gemini.Config{
		APIKeys: liveKeys,
		Vertex:  vertex,
		Model:   cfg.Gemini.Model,
		Voice:   cfg.Gemini.Voice,
		Aliases: aliases,
		Handler: echotools.Handler(),
	}, logger)
	if err != nil {
		fprintf(stderr, "sheepdog voice-test: %v\n", err)
		return 1
	}
	fprintf(stderr, "sheepdog voice-test: %s in=%s out=%s\n", cfg, o.in, o.out)

	ctx, cancel := context.WithTimeout(ctx, o.maxDur)
	defer cancel()
	if err := sess.Start(ctx); err != nil {
		fprintf(stderr, "sheepdog voice-test: start: %v\n", err)
		if errors.Is(err, gemini.ErrAllKeysExhausted) {
			return 3
		}
		return 1
	}

	t := &voiceTest{o: o, sess: sess, out: outW, stderr: stderr, start: time.Now(), urgency: urgency}
	code = t.run(ctx, pcmIn)
	cancel()
	t.streamWG.Wait() // stop feeding audio before the session goes away
	_ = sess.Close()
	if outW != nil {
		if err := outW.Close(); err != nil {
			fprintf(stderr, "sheepdog voice-test: out wav: %v\n", err)
			code = 1
		}
	}
	t.summary(stdout)
	return code
}

// readInputWAV loads and validates the input file.
func readInputWAV(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rate, pcm, err := wav.Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if rate != inRate {
		return nil, fmt.Errorf("%s: sample rate %d Hz, want %d", path, rate, inRate)
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("%s: no samples", path)
	}
	return pcm, nil
}

// voiceTest is the per-run state of the subcommand.
type voiceTest struct {
	o       voiceTestOpts
	sess    *gemini.Session
	out     *wav.Writer
	stderr  io.Writer
	start   time.Time
	urgency voice.Urgency

	streamWG sync.WaitGroup
	errMu    sync.Mutex // errs is appended from the stream goroutine too

	// statistics for the summary
	audioBytes    int64
	transcriptIn  []string
	transcriptOut []string
	toolCalls     []string
	interrupted   int
	resumed       int
	errs          []string
	exitReason    string
}

func (t *voiceTest) addErr(msg string) {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	t.errs = append(t.errs, msg)
}

func (t *voiceTest) run(ctx context.Context, pcmIn []byte) int {
	events := t.sess.Events()
	audio := t.sess.Audio()
	streamDone := make(chan struct{})
	t.streamWG.Add(1)
	go func() { defer t.streamWG.Done(); t.stream(ctx, pcmIn, streamDone) }()

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var (
		textSent, pushSent, dropDone bool
		lastTurn                     time.Time
		inputDone                    bool
		turnsSeen                    int
	)
	for {
		select {
		case <-ctx.Done():
			t.exitReason = "max reached"
			return 0
		case <-streamDone:
			inputDone = true
			streamDone = nil
		case chunk, ok := <-audio:
			if !ok {
				t.exitReason = "audio closed"
				return 1
			}
			t.audioBytes += int64(len(chunk))
			if t.out != nil {
				if _, err := t.out.Write(chunk); err != nil {
					t.exitReason = "write error"
					t.addErr(err.Error())
					return 1
				}
			}
		case ev, ok := <-events:
			if !ok {
				t.exitReason = "session closed"
				return 1
			}
			t.printEvent(ev)
			switch e := ev.(type) {
			case voice.EventTurnComplete:
				lastTurn = time.Now()
				turnsSeen++
			case voice.EventStatus:
				if e.Status == voice.StatusClosed {
					t.exitReason = "voice closed: " + e.Reason
					return 1
				}
			}
		case <-tick.C:
			el := time.Since(t.start)
			if t.o.text != "" && !textSent && el >= t.o.textAfter {
				textSent = true
				if err := t.sess.SendText(t.o.text); err != nil {
					t.addErr("text: " + err.Error())
				}
			}
			if t.o.push != "" && !pushSent && el >= t.o.pushAfter {
				pushSent = true
				if err := t.sess.Push(t.o.push, t.urgency); err != nil {
					t.addErr("push: " + err.Error())
				}
			}
			if t.o.dropAfter > 0 && !dropDone && el >= t.o.dropAfter {
				dropDone = true
				t.sess.DropConnection()
			}
			scheduled := (t.o.text == "" || textSent) && (t.o.push == "" || pushSent) && (t.o.dropAfter == 0 || dropDone)
			quiet := turnsSeen > 0 && time.Since(lastTurn) >= t.o.idle && !t.sess.TurnOpen()
			if scheduled && inputDone && quiet && t.sess.Status() == voice.StatusConnected {
				t.exitReason = "idle"
				return 0
			}
		}
	}
}

// stream sends the WAV in real time, then silence until ctx ends, so the
// server's VAD sees the end of speech exactly as it would from a mic.
func (t *voiceTest) stream(ctx context.Context, pcm []byte, done chan<- struct{}) {
	silence := make([]byte, frameBytes)
	tick := time.NewTicker(frameDur)
	defer tick.Stop()
	off := 0
	signalled := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		frame := silence
		if off < len(pcm) {
			end := min(off+frameBytes, len(pcm))
			frame = pcm[off:end]
			off = end
		} else if !signalled {
			signalled = true
			close(done)
		}
		if err := t.sess.SendAudio(frame); err != nil {
			if ctx.Err() == nil {
				t.addErr("audio: " + err.Error())
			}
			return
		}
	}
}

// printEvent writes one JSON line per event to stderr.
func (t *voiceTest) printEvent(ev voice.Event) {
	line := map[string]any{"t_ms": time.Since(t.start).Milliseconds()}
	switch e := ev.(type) {
	case voice.EventStatus:
		line["event"], line["status"], line["reason"] = "status", e.Status.String(), e.Reason
	case voice.EventTranscript:
		line["event"], line["dir"], line["text"], line["final"] = "transcript", e.Dir.String(), e.Text, e.Final
		if e.Dir == voice.DirIn {
			t.transcriptIn = append(t.transcriptIn, e.Text)
		} else {
			t.transcriptOut = append(t.transcriptOut, e.Text)
		}
	case voice.EventInterrupted:
		line["event"] = "interrupted"
		t.interrupted++
	case voice.EventToolCall:
		line["event"], line["name"], line["args"], line["id"] = "tool_call", e.Call.Name, e.Call.Args, e.Call.ID
		t.toolCalls = append(t.toolCalls, e.Call.Name)
	case voice.EventToolResult:
		line["event"], line["name"], line["spoken"], line["cancelled"] = "tool_result", e.Call.Name, e.Result.Spoken, e.Cancelled
		if e.Err != nil {
			line["error"] = e.Err.Error()
		}
	case voice.EventPush:
		line["event"], line["text"], line["urgency"] = "push", e.Text, e.Urgency.String()
	case voice.EventTurnComplete:
		line["event"] = "turn_complete"
	case voice.EventResumed:
		line["event"], line["handle"], line["context_lost"] = "resumed", e.Handle, e.ContextLost
		t.resumed++
	case voice.EventError:
		line["event"], line["error"] = "error", e.Err.Error()
		t.addErr(e.Err.Error())
	default:
		line["event"] = fmt.Sprintf("%T", ev)
	}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	_, _ = t.stderr.Write(append(b, '\n'))
}

func (t *voiceTest) summary(w io.Writer) {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	sum := map[string]any{
		"exit_reason":    t.exitReason,
		"elapsed_s":      time.Since(t.start).Seconds(),
		"audio_out_s":    float64(t.audioBytes) / float64(outRate*2),
		"transcript_in":  strings.Join(t.transcriptIn, ""),
		"transcript_out": strings.Join(t.transcriptOut, ""),
		"tool_calls":     t.toolCalls,
		"interrupted":    t.interrupted,
		"resumed":        t.resumed,
		"errors":         t.errs,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sum)
}
