// Command livecanary is a standalone platform canary for Gemini Live
// context retention across session resumptions (work item "Gemini Live
// context retention across resumes", docs/handover-2026-10-04.md).
//
// It speaks eight facts to a Live session set up like the daemon's (same
// voice, compression trigger 16k / target 4k, resumption handle, tools),
// optionally does something to the session (an "arm"), resumes it with
// the latest handle, then asks one probe question per fact and grades the
// answers by keywords. It measures and reports; nothing reacts to the
// numbers and it never touches a running sheepdog daemon.
//
//	set -a; . ./.env; set +a
//	go run ./cmd/livecanary -arm resume -out docs/research/live-resume-context-results
//
// See README.md for the arms and their baselines.
package main

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

//go:embed audio/*.wav
var audioFS embed.FS

var (
	armFlag     = flag.String("arm", "resume", "control | resume | silent | briefing | goaway | compress | fill | long | chain | tool | interrupt | longmic")
	noResume    = flag.Bool("no-resume", false, "run the arm but do not resume (the within-connection control)")
	location    = flag.String("location", "us-central1", "Vertex location")
	model       = flag.String("model", "gemini-3.8-live", "Live model")
	transparent = flag.Bool("transparent", false, "SessionResumption.Transparent (the daemon leaves it off)")
	outDir      = flag.String("out", "", "directory for the raw JSONL (default: stdout only)")
	micMinutes  = flag.Float64("mic-minutes", 5, "longmic: minutes of continuous microphone audio")
	fillTo      = flag.Int("fill-to", 0, "goaway: grow the context to about this many prompt tokens with long spoken answers while waiting (0 = small talk only)")
	chainN      = flag.Int("chain-n", 4, "chain: how many resumes in a row")
	chainTalk   = flag.Bool("chain-talk", false, "chain: a small-talk turn after every resume (default: idle, like a daemon between GoAways)")
	chainFill   = flag.Int("chain-fill", 11000, "chain: grow the context to about this many prompt tokens first (0 = just the facts)")
	chainProbe  = flag.Bool("chain-probe", false, "chain: with -chain-talk, the turn after each resume asks a fact question (graded) instead of small talk")
	chainWait   = flag.Duration("chain-wait", 0, "chain: idle time after the turn before the next resume")
	chainNew    = flag.Bool("chain-newfact", false, "chain: tell one new fact on every resumed connection and ask about all of them at the end (which connection's content does a resume restore?)")
	goawayMax   = flag.Duration("goaway-max", 14*time.Minute, "goaway: longest wait for the server's GoAway")
)

var (
	t0      = time.Now()
	outFile *os.File
	logMu   sync.Mutex
)

func logj(kind string, kv map[string]any) {
	if kv == nil {
		kv = map[string]any{}
	}
	kv["t"] = time.Since(t0).Seconds()
	kv["kind"] = kind
	kv["arm"] = *armFlag
	b, _ := json.Marshal(kv)
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Println(string(b))
	if outFile != nil {
		_, _ = outFile.Write(append(b, '\n'))
	}
}

// fact is one thing the user asks the model to remember: the audio clip
// that says it, the probe question, and the keyword check on the answer
// (every group must match; a group matches if any of its alternatives is
// a substring of the lower-cased answer with punctuation removed).
type fact struct {
	name  string
	clip  string // "" when the fact is told another way (text, silent context, tool)
	q     string
	check [][]string
}

var spokenFacts = []fact{
	{"blue_key", "f1", "Where is the blue key?", [][]string{{"drawer"}, {"seven", "7"}}},
	{"cat", "f2", "What is my sister's cat called?", [][]string{{"pickle"}}},
	{"dana", "f3", "When is the meeting with Dana?", [][]string{{"thursday"}, {"3", "three"}}},
	{"server_code", "f4", "What is the server room code?", [][]string{{"4492", "4 4 9 2", "four four nine two", "forty four ninety two", "forty four hundred ninety two", "four thousand four hundred"}}},
	{"bike", "f5", "What is wrong with the red bike?", [][]string{{"flat"}, {"rear", "back"}}},
	{"wifi", "f6", "What is the Wi-Fi password hint?", [][]string{{"mango"}, {"river"}}},
	{"library", "f7", "When is the library book due?", [][]string{{"12", "12th", "twelfth", "twelve"}}},
	{"charger", "f8", "Where is the spare charger?", [][]string{{"green"}, {"backpack"}}},
}

type usage struct {
	Prompt, Response int32
	PromptBy, RespBy map[string]int32
	T                float64
	Conn             int
}

type state struct {
	mu          sync.Mutex
	conn        int
	last        usage
	afterFirst  *usage // first usage after the last (re)connect
	wantAfter   bool
	handle      string
	handles     int
	goaway      bool
	goawayAt    float64
	calls       []*genai.FunctionCall
	nCalls      int
	outText     strings.Builder
	lastSaid    string
	turnDone    chan struct{}
	interrupted int
	audioChunks int
	turns       int
}

func modal(d []*genai.ModalityTokenCount) map[string]int32 {
	m := map[string]int32{}
	for _, x := range d {
		if x != nil {
			m[string(x.Modality)] += x.TokenCount
		}
	}
	return m
}

func (st *state) readLoop(sess *genai.Session, conn int) {
	for {
		msg, err := sess.Receive()
		if err != nil {
			logj("recv_end", map[string]any{"conn": conn, "err": err.Error()})
			return
		}
		st.handle1(msg, conn)
	}
}

func (st *state) handle1(msg *genai.LiveServerMessage, conn int) {
	if u := msg.UsageMetadata; u != nil {
		cur := usage{u.PromptTokenCount, u.ResponseTokenCount, modal(u.PromptTokensDetails), modal(u.ResponseTokensDetails), time.Since(t0).Seconds(), conn}
		st.mu.Lock()
		st.last = cur
		if st.wantAfter && conn == st.conn {
			c := cur
			st.afterFirst = &c
			st.wantAfter = false
		}
		st.mu.Unlock()
		extra := map[string]any{"conn": conn, "prompt": cur.Prompt, "response": cur.Response, "total": u.TotalTokenCount, "prompt_by_modality": cur.PromptBy, "response_by_modality": cur.RespBy}
		if u.ThoughtsTokenCount != 0 {
			extra["thoughts"] = u.ThoughtsTokenCount
		}
		if u.CachedContentTokenCount != 0 {
			extra["cached"] = u.CachedContentTokenCount
		}
		if u.ToolUsePromptTokenCount != 0 {
			extra["tool_use_prompt"] = u.ToolUsePromptTokenCount
		}
		logj("usage", extra)
	}
	if r := msg.SessionResumptionUpdate; r != nil {
		st.mu.Lock()
		if r.Resumable && r.NewHandle != "" {
			st.handle = r.NewHandle
			st.handles++
		}
		lp := st.last.Prompt
		st.mu.Unlock()
		h := r.NewHandle
		if len(h) > 8 {
			h = h[:8]
		}
		logj("handle", map[string]any{"conn": conn, "handle": h, "resumable": r.Resumable, "prompt_at_issue": lp})
	}
	if tc := msg.ToolCall; tc != nil {
		for _, fc := range tc.FunctionCalls {
			logj("tool_call", map[string]any{"conn": conn, "name": fc.Name, "args": fc.Args})
			st.mu.Lock()
			st.calls = append(st.calls, fc)
			st.nCalls++
			st.mu.Unlock()
		}
	}
	if g := msg.GoAway; g != nil {
		logj("goaway", map[string]any{"conn": conn, "time_left": g.TimeLeft.String()})
		st.mu.Lock()
		st.goaway, st.goawayAt = true, time.Since(t0).Seconds()
		st.mu.Unlock()
	}
	if sc := msg.ServerContent; sc != nil {
		if sc.ModelTurn != nil {
			for _, p := range sc.ModelTurn.Parts {
				if p != nil && p.InlineData != nil && len(p.InlineData.Data) > 0 {
					st.mu.Lock()
					st.audioChunks++
					st.mu.Unlock()
				}
			}
		}
		if sc.OutputTranscription != nil {
			st.mu.Lock()
			st.outText.WriteString(sc.OutputTranscription.Text)
			st.mu.Unlock()
		}
		if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
			logj("heard", map[string]any{"conn": conn, "text": sc.InputTranscription.Text})
		}
		if sc.Interrupted {
			st.mu.Lock()
			st.interrupted++
			st.mu.Unlock()
			logj("interrupted", map[string]any{"conn": conn})
		}
		if sc.TurnComplete {
			st.mu.Lock()
			out := st.outText.String()
			st.outText.Reset()
			st.lastSaid = out
			st.turns++
			ch := st.turnDone
			st.mu.Unlock()
			logj("turn_complete", map[string]any{"conn": conn, "said": out})
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

func pcm(name string) ([]byte, error) {
	b, err := audioFS.ReadFile("audio/" + name + ".wav")
	if err != nil {
		return nil, err
	}
	i := 12
	for i+8 <= len(b) {
		id := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		if id == "data" {
			if i+8+n > len(b) {
				n = len(b) - i - 8
			}
			return b[i+8 : i+8+n], nil
		}
		i += 8 + n + n%2
	}
	return nil, errors.New("no data chunk in " + name)
}

const frameBytes = 640 // 20 ms at 16 kHz PCM16

func sendFrame(sess *genai.Session, b []byte) error {
	return sess.SendRealtimeInput(genai.LiveRealtimeInput{Audio: &genai.Blob{Data: b, MIMEType: "audio/pcm;rate=16000"}})
}

// speak streams a clip in real time, then a little silence so the server
// VAD closes the turn; end of stream follows when streamEnd is set (the
// daemon gates silence the same way).
func speak(sess *genai.Session, clip string, streamEnd bool) error {
	data, err := pcm(clip)
	if err != nil {
		return err
	}
	for off := 0; off < len(data); off += frameBytes {
		end := min(off+frameBytes, len(data))
		if err := sendFrame(sess, data[off:end]); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	sil := make([]byte, frameBytes)
	for k := 0; k < 25; k++ {
		if err := sendFrame(sess, sil); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if streamEnd {
		return sess.SendRealtimeInput(genai.LiveRealtimeInput{AudioStreamEnd: true})
	}
	return nil
}

// drain forgets a turn completion nobody waited for, so the next wait
// sees the next turn.
func (st *state) drain() {
	select {
	case <-st.turnDone:
	default:
	}
}

// waitQuiet waits until no turn completes for quiet (or max passes), so a
// probe is not sent into an answer still being spoken.
func (st *state) waitQuiet(quiet, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		select {
		case <-st.turnDone:
		case <-time.After(quiet):
			return
		}
	}
}

func (st *state) waitTurn(d time.Duration) bool {
	select {
	case <-st.turnDone:
		return true
	case <-time.After(d):
		logj("turn_timeout", nil)
		return false
	}
}

const baseInstruction = "You are a voice assistant in a memory test. When the user tells you something to remember, reply only 'Got it.' When asked a question, answer in one short sentence from what the user told you; if you do not know, say 'I don't know.'"

const toolInstruction = baseInstruction + " You have a tool called lookup. Whenever the user asks about opening hours, schedules or anything external that they did not tell you, you MUST call lookup first, never answer it yourself and never say you do not know; say 'checking' as you call it. When the lookup result arrives, tell the user the answer in one short sentence."

func cfg(handle string) *genai.LiveConnectConfig {
	instr := baseInstruction
	switch *armFlag {
	case "tool":
		instr = toolInstruction
	case "long", "interrupt", "goaway", "chain":
		instr = baseInstruction + " If the user asks for a story, tell a long one of at least fifteen sentences."
	}
	return &genai.LiveConnectConfig{
		ResponseModalities:       []genai.Modality{genai.ModalityAudio},
		SpeechConfig:             &genai.SpeechConfig{VoiceConfig: &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: "Puck"}}},
		SystemInstruction:        &genai.Content{Parts: []*genai.Part{{Text: instr}}},
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		ContextWindowCompression: &genai.ContextWindowCompressionConfig{TriggerTokens: genai.Ptr[int64](16000), SlidingWindow: &genai.SlidingWindow{TargetTokens: genai.Ptr[int64](4000)}},
		SessionResumption:        &genai.SessionResumptionConfig{Handle: handle, Transparent: *transparent},
		Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", Behavior: genai.BehaviorNonBlocking,
			Description: "Look something up for the user. Call it whenever the user asks you to look something up or asks about opening hours; say 'checking' as you call it.",
			Parameters:  &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"query": {Type: genai.TypeString}}, Required: []string{"query"}}}}}},
	}
}

type runner struct {
	client *genai.Client
	st     *state
	sess   *genai.Session
}

func (r *runner) connect(handle string) {
	r.st.mu.Lock()
	r.st.conn++
	conn := r.st.conn
	r.st.mu.Unlock()
	s, err := r.client.Live.Connect(context.Background(), *model, cfg(handle))
	if err != nil {
		fatal("connect: " + err.Error())
	}
	logj("connected", map[string]any{"conn": conn, "resumed_with": len(handle) > 0})
	go r.st.readLoop(s, conn)
	r.sess = s
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	logj("fatal", map[string]any{"err": msg})
	os.Exit(1)
}

func (r *runner) text(s string) {
	if err := r.sess.SendRealtimeInput(genai.LiveRealtimeInput{Text: s}); err != nil {
		logj("send_error", map[string]any{"err": err.Error()})
	}
}

func (r *runner) silent(text string) {
	tc := false
	err := r.sess.SendClientContent(genai.LiveClientContentInput{Turns: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: text}}}}, TurnComplete: &tc})
	if err != nil {
		logj("send_error", map[string]any{"err": err.Error()})
	}
}

func (r *runner) brief(id, facts string, sched genai.FunctionResponseScheduling) {
	err := r.sess.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: []*genai.FunctionResponse{{ID: id, Name: "briefing",
		Response: map[string]any{"from": "supervisor", "kind": "fact", "facts": facts}, Scheduling: sched}}})
	logj("briefing_sent", map[string]any{"id": id, "facts": facts, "sched": string(sched), "err": fmt.Sprint(err)})
}

func (r *runner) tellFacts() {
	for _, f := range spokenFacts {
		if err := speak(r.sess, f.clip, true); err != nil {
			fatal("speak: " + err.Error())
		}
		logj("fact_sent", map[string]any{"fact": f.name})
		r.st.waitTurn(25 * time.Second)
		time.Sleep(1500 * time.Millisecond)
	}
	time.Sleep(3 * time.Second) // let a resumption update arrive
}

// ask sends text and waits for the turn it starts to complete; a turn
// completion left over from before (an interrupted answer) is dropped
// first, so a long answer is really waited for.
func (r *runner) ask(s string, wait time.Duration) {
	r.st.waitQuiet(1500*time.Millisecond, 10*time.Second)
	r.st.drain()
	r.text(s)
	r.st.waitTurn(wait)
}

func (r *runner) smallTalk() {
	r.text("Small talk: nothing to remember here, just say ok.")
	r.st.waitTurn(30 * time.Second)
}

func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ",", "")
	s = regexp.MustCompile(`[^a-z0-9 ]+`).ReplaceAllString(s, " ")
	return " " + strings.Join(strings.Fields(s), " ") + " "
}

func grade(said string, check [][]string) bool {
	n := normalize(said)
	for _, group := range check {
		ok := false
		for _, alt := range group {
			a := normalize(alt)
			if strings.Contains(n, a) || (len(alt) > 3 && strings.Contains(strings.ReplaceAll(n, " ", ""), strings.ReplaceAll(a, " ", ""))) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

type probeResult struct {
	Name string `json:"name"`
	Q    string `json:"q"`
	Said string `json:"said"`
	Hit  bool   `json:"hit"`
}

func (r *runner) probe(fs []fact) (res []probeResult) {
	for _, f := range fs {
		r.st.waitQuiet(1500*time.Millisecond, 10*time.Second)
		r.st.drain()
		r.text(f.q)
		logj("probe_sent", map[string]any{"fact": f.name, "q": f.q})
		r.st.waitTurn(30 * time.Second)
		time.Sleep(1200 * time.Millisecond)
		r.st.mu.Lock()
		said := r.st.lastSaid
		r.st.mu.Unlock()
		hit := grade(said, f.check)
		logj("probe_result", map[string]any{"fact": f.name, "said": said, "hit": hit})
		res = append(res, probeResult{f.name, f.q, said, hit})
	}
	return res
}

func main() {
	flag.Parse()
	key, project := os.Getenv("VERTEX_API_KEY"), os.Getenv("VERTEX_PROJECT")
	if key == "" || project == "" {
		fmt.Fprintln(os.Stderr, "VERTEX_API_KEY / VERTEX_PROJECT unset")
		os.Exit(2)
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			fatal(err.Error())
		}
		name := fmt.Sprintf("%s-%s", time.Now().UTC().Format("2006-01-02T150405Z"), *armFlag)
		if *noResume {
			name += "-noresume"
		}
		f, err := os.Create(filepath.Join(*outDir, name+".jsonl"))
		if err != nil {
			fatal(err.Error())
		}
		outFile = f
		defer f.Close()
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, APIKey: key, Project: project, Location: *location,
		HTTPOptions: genai.HTTPOptions{BaseURL: fmt.Sprintf("https://%s-aiplatform.googleapis.com/", *location), Headers: http.Header{"X-Goog-Api-Key": []string{key}}},
	})
	if err != nil {
		fatal("client: " + err.Error())
	}
	r := &runner{client: client, st: &state{turnDone: make(chan struct{}, 1)}}
	logj("start", map[string]any{"date": time.Now().UTC().Format(time.RFC3339), "model": *model, "location": *location, "backend": "vertex", "resume": !*noResume, "transparent": *transparent})
	r.connect("")

	extra := r.runArm()

	// The resume.
	st := r.st
	st.mu.Lock()
	before, handle := st.last, st.handle
	hs := st.handles
	st.mu.Unlock()
	logj("before_resume", map[string]any{"prompt": before.Prompt, "prompt_by_modality": before.PromptBy, "handles_seen": hs, "handle_known": handle != ""})
	resumed := false
	if !*noResume {
		if handle == "" {
			fatal("no resumption handle to resume with")
		}
		old := r.sess
		st.mu.Lock()
		st.wantAfter, st.afterFirst = true, nil
		st.mu.Unlock()
		if *armFlag == "goaway" {
			// Like the daemon: the new connection first, then the old one goes.
			r.connect(handle)
			old.Close()
		} else {
			old.Close()
			time.Sleep(2 * time.Second)
			r.connect(handle)
		}
		resumed = true
		time.Sleep(2 * time.Second)
	}

	probes := append([]fact(nil), spokenFacts...)
	probes = append(probes, extra...)
	results := r.probe(probes)
	time.Sleep(2 * time.Second)

	st.mu.Lock()
	after := st.afterFirst
	nCalls, interrupted, turns := st.nCalls, st.interrupted, st.turns
	st.mu.Unlock()
	hit, total := 0, 0
	earlyHit := 0
	for i, p := range results {
		total++
		if p.Hit {
			hit++
			if i < len(spokenFacts) {
				earlyHit++
			}
		}
	}
	sum := map[string]any{
		"date": time.Now().UTC().Format("2006-01-02"), "model": *model, "backend": "vertex", "location": *location,
		"resumed": resumed, "recall": fmt.Sprintf("%d/%d", hit, total), "recall_spoken_facts": fmt.Sprintf("%d/%d", earlyHit, len(spokenFacts)),
		"prompt_before": before.Prompt, "prompt_before_by_modality": before.PromptBy,
		"handles": hs, "tool_calls": nCalls, "interrupted": interrupted, "turns": turns, "results": results,
	}
	if after != nil {
		sum["prompt_after_first"] = after.Prompt
		sum["prompt_after_by_modality"] = after.PromptBy
		if before.Prompt > 0 {
			sum["prompt_ratio"] = float64(after.Prompt) / float64(before.Prompt)
		}
	}
	var missed []string
	for _, p := range results {
		if !p.Hit {
			missed = append(missed, p.Name)
		}
	}
	sum["missed"] = missed
	logj("summary", sum)
	r.sess.Close()
	logj("done", nil)
}

// runArm tells the facts and does what the arm is about. It returns the
// extra facts to probe beyond the eight spoken ones.
func (r *runner) runArm() []fact {
	st := r.st
	r.tellFacts()
	switch *armFlag {
	case "control", "resume":
		return nil

	case "silent":
		// Silent clientContent after the last handle: expected to be lost on a resume.
		r.silent("[system context, not spoken by the user] The green door is locked.")
		time.Sleep(2 * time.Second)
		r.silent("[system context, not spoken by the user] The plant needs water on Friday.")
		time.Sleep(2 * time.Second)
		r.smallTalk()
		time.Sleep(2 * time.Second)
		r.silent("[system context, not spoken by the user] The parcel is with the neighbour at number nine.")
		time.Sleep(3 * time.Second)
		return briefedFacts("silent_after_handle")

	case "briefing":
		r.brief("b-1", "The green door is locked.", genai.FunctionResponseSchedulingSilent)
		time.Sleep(2 * time.Second)
		r.brief("b-2", "The plant needs water on Friday.", genai.FunctionResponseSchedulingSilent)
		time.Sleep(2 * time.Second)
		r.brief("b-3", "The parcel is with the neighbour at number nine. Tell the user this in one sentence.", genai.FunctionResponseSchedulingWhenIdle)
		st.waitTurn(30 * time.Second)
		time.Sleep(2 * time.Second)
		r.smallTalk()
		time.Sleep(3 * time.Second)
		return briefedFacts("briefing")

	case "goaway":
		deadline := time.Now().Add(*goawayMax)
		for k := 1; time.Now().Before(deadline); k++ {
			st.mu.Lock()
			ga := st.goaway
			st.mu.Unlock()
			if ga {
				break
			}
			st.mu.Lock()
			p := st.last.Prompt
			st.mu.Unlock()
			if *fillTo > 0 && int(p) < *fillTo {
				r.ask(fmt.Sprintf("Story %d: tell me a detailed story about a different traveller, at least ten sentences.", k), 90*time.Second)
			} else {
				r.text(fmt.Sprintf("Small talk %d: nothing to remember here, just say ok.", k))
				st.waitTurn(30 * time.Second)
			}
			for w := 0; w < 20; w++ {
				st.mu.Lock()
				ga = st.goaway
				st.mu.Unlock()
				if ga {
					break
				}
				time.Sleep(time.Second)
			}
		}
		time.Sleep(time.Second)
		return nil

	case "compress":
		return r.runCompress(0)
	case "fill":
		// A big context below the trigger (resume at ~12k tokens, the size
		// of the daemon's sessions at a resume), no compression.
		return r.runCompress(12000)

	case "tool":
		return r.runTool()

	case "long":
		return r.runLong()

	case "chain":
		return r.runChain()

	case "interrupt":
		return r.runInterrupt()

	case "longmic":
		r.runLongMic()
		return nil
	}
	fatal("unknown arm " + *armFlag)
	return nil
}

func briefedFacts(how string) []fact {
	_ = how
	return []fact{
		{"green_door", "", "Is the green door locked or open?", [][]string{{"locked"}}},
		{"plant", "", "When does the plant need water?", [][]string{{"friday"}}},
		{"parcel", "", "Where is the parcel?", [][]string{{"neighbour", "neighbor"}, {"nine", "9"}}},
	}
}

var words = strings.Fields("amber river lantern copper meadow quiet engine harbor velvet orbit pencil canyon winter signal marble thistle garden anchor ribbon cobalt violet timber echo summit fabric glacier pepper rocket saddle tunnel willow yonder zephyr basket candle dragon forest island jungle kettle ladder mirror needle oyster palace quartz rabbit season tablet umbrella voyage window")

// filler returns n characters of deterministic, fact-free prose.
func filler(rng *rand.Rand, n int) string {
	var b strings.Builder
	for b.Len() < n {
		l := 6 + rng.Intn(8)
		for i := 0; i < l; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(words[rng.Intn(len(words))])
		}
		b.WriteString(". ")
	}
	return b.String()
}

// runCompress pushes the session with text filler (cheap), keeping a usage
// trail, then tells one late fact: past the 16k compression trigger when
// stopAt is 0, else only up to stopAt prompt tokens (below the trigger).
func (r *runner) runCompress(stopAt int32) []fact {
	st := r.st
	rng := rand.New(rand.NewSource(7))
	peak, dropped, after := int32(0), false, 0
	for round := 1; round <= 12; round++ {
		for k := 0; k < 4; k++ {
			r.silent("[system context, not spoken by the user] Background notes, no action needed: " + filler(rng, 3000))
			time.Sleep(300 * time.Millisecond)
		}
		r.smallTalk()
		time.Sleep(1500 * time.Millisecond)
		st.mu.Lock()
		p := st.last.Prompt
		st.mu.Unlock()
		logj("compress_round", map[string]any{"round": round, "prompt": p, "peak": peak})
		if p < peak-1000 {
			dropped = true
		}
		peak = max(peak, p)
		if stopAt > 0 {
			if p >= stopAt {
				break
			}
			continue
		}
		if dropped {
			after++
			if after >= 2 {
				break
			}
		} else if peak > 17500 && round >= 8 {
			// Past the trigger without a visible drop: keep going a little.
			after++
			if after >= 4 {
				break
			}
		}
	}
	logj("compress_done", map[string]any{"peak": peak, "drop_seen": dropped})
	r.text("Remember this: the parking spot is B14.")
	st.waitTurn(25 * time.Second)
	time.Sleep(3 * time.Second)
	return []fact{{"parking", "", "Where is the parking spot?", [][]string{{"b14", "b 14", "b one four", "b fourteen"}}}}
}

// runTool makes the model call a tool twice (a real function call and
// response each time), with a spoken fact told between the call and its
// result.
func (r *runner) runTool() []fact {
	st := r.st
	round := func(ask, result string, between string) bool {
		st.mu.Lock()
		st.calls = nil
		n := st.nCalls
		st.mu.Unlock()
		r.text(ask)
		deadline := time.Now().Add(25 * time.Second)
		var fc *genai.FunctionCall
		for time.Now().Before(deadline) {
			st.mu.Lock()
			if len(st.calls) > 0 {
				fc = st.calls[0]
			}
			st.mu.Unlock()
			if fc != nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if fc == nil {
			logj("no_tool_call", map[string]any{"ask": ask, "calls_before": n})
			return false
		}
		st.waitTurn(15 * time.Second) // the "checking" turn
		if between != "" {
			r.text(between)
			st.waitTurn(25 * time.Second)
			time.Sleep(2 * time.Second)
		}
		err := r.sess.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: []*genai.FunctionResponse{{ID: fc.ID, Name: fc.Name,
			Response: map[string]any{"result": result}, Scheduling: genai.FunctionResponseSchedulingWhenIdle}}})
		logj("tool_response_sent", map[string]any{"result": result, "err": fmt.Sprint(err)})
		st.waitTurn(30 * time.Second)
		time.Sleep(3 * time.Second)
		return true
	}
	ok1 := round("Please look up the opening hours of the city museum.", "The city museum is open 9 to 5, closed Mondays.", "While that runs: remember that the umbrella is in the car.")
	ok2 := round("Now look up when the number 42 bus runs.", "The number 42 bus runs every 15 minutes until midnight.", "")
	logj("tool_rounds", map[string]any{"first_called": ok1, "second_called": ok2})
	r.smallTalk()
	time.Sleep(3 * time.Second)
	return []fact{
		{"umbrella", "", "Where is the umbrella?", [][]string{{"car"}}},
		{"museum_hours", "", "What are the museum's opening hours?", [][]string{{"monday"}, {"9", "nine"}}},
		{"bus42", "", "How often does the number 42 bus run?", [][]string{{"15", "fifteen"}}},
	}
}

// runLong gets four long spoken answers (about a dozen sentences each, in
// full) from the model, so its own audio output is a large part of the
// context when the session resumes.
func (r *runner) runLong() []fact {
	for i, topic := range []string{"a lighthouse keeper", "a railway station at night", "a market in a mountain village", "a ship crossing a storm"} {
		r.ask(fmt.Sprintf("Story %d: tell me a long, detailed story about %s, at least twelve sentences.", i+1, topic), 90*time.Second)
		time.Sleep(2 * time.Second)
	}
	r.smallTalk()
	time.Sleep(3 * time.Second)
	return nil
}

// runChain grows the context to about 11k prompt tokens with long spoken
// answers, then resumes -chain-n times in a row with the latest handle,
// idle in between (or with a small-talk turn after each resume), the way
// the daemon is resumed every ~9 minutes. The runner's own final resume
// is the last of the chain. Real logs show resumes that shed context
// again and again with no turns between them.
func (r *runner) runChain() []fact {
	st := r.st
	for k := 1; k <= 12; k++ {
		st.mu.Lock()
		p := st.last.Prompt
		st.mu.Unlock()
		if p >= int32(*chainFill) {
			break
		}
		r.ask(fmt.Sprintf("Story %d: tell me a detailed story about a different traveller, at least ten sentences.", k), 90*time.Second)
		time.Sleep(2 * time.Second)
	}
	r.smallTalk()
	time.Sleep(3 * time.Second)
	var extra []fact
	codes := []string{"amber", "copper", "harbor", "velvet", "marble", "lantern"}
	for i := 1; i < *chainN; i++ {
		st.mu.Lock()
		before, handle := st.last, st.handle
		st.wantAfter, st.afterFirst = true, nil
		st.mu.Unlock()
		old := r.sess
		old.Close()
		time.Sleep(2 * time.Second)
		r.connect(handle)
		time.Sleep(12 * time.Second)
		if *chainNew {
			code := codes[(i-1)%len(codes)]
			r.ask(fmt.Sprintf("Remember this: the password for step %d is %s.", i, code), 30*time.Second)
			time.Sleep(3 * time.Second)
			extra = append(extra, fact{fmt.Sprintf("step%d_in_conn%d", i, i+1), "", fmt.Sprintf("What is the password for step %d?", i), [][]string{{code}}})
		} else if *chainTalk {
			if *chainProbe {
				f := spokenFacts[(i-1)%len(spokenFacts)]
				res := r.probe([]fact{f})
				kvp := map[string]any{"step": i, "fact": f.name, "hit": res[0].Hit, "said": res[0].Said}
				logj("chain_probe", kvp)
			} else {
				r.smallTalk()
			}
			time.Sleep(3 * time.Second)
		}
		time.Sleep(*chainWait)
		st.mu.Lock()
		after := st.afterFirst
		h2 := st.handle
		st.mu.Unlock()
		kv := map[string]any{"step": i, "before_prompt": before.Prompt, "same_handle_next": h2 == handle}
		if after != nil {
			kv["after_prompt"], kv["after_by_modality"] = after.Prompt, after.PromptBy
		}
		logj("chain_step", kv)
	}
	return extra
}

// runInterrupt cuts the model off three times mid-answer (a long story)
// with a spoken interruption that carries a new fact.
func (r *runner) runInterrupt() []fact {
	st := r.st
	for i, clip := range []string{"int1", "int2", "int3"} {
		st.mu.Lock()
		before, intr := st.audioChunks, st.interrupted
		st.mu.Unlock()
		r.text("Tell me a long, detailed story about a lighthouse keeper, at least fifteen sentences.")
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			st.mu.Lock()
			c := st.audioChunks
			st.mu.Unlock()
			if c > before+20 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		time.Sleep(1500 * time.Millisecond)
		if err := speak(r.sess, clip, true); err != nil {
			fatal("speak: " + err.Error())
		}
		st.waitTurn(25 * time.Second)
		time.Sleep(2 * time.Second)
		st.mu.Lock()
		logj("interrupt_round", map[string]any{"round": i + 1, "interrupted_events": st.interrupted - intr})
		st.mu.Unlock()
	}
	r.smallTalk()
	time.Sleep(3 * time.Second)
	return []fact{
		{"parking", "", "Where is the parking spot?", [][]string{{"b14", "b 14", "b one four", "b fourteen"}}},
		{"dentist", "", "When is the dentist?", [][]string{{"tuesday"}, {"9", "nine"}}},
		{"gift", "", "Where is the gift?", [][]string{{"attic"}}},
	}
}

// runLongMic streams the microphone continuously for -mic-minutes: low
// ambient noise the whole time and an unrelated spoken remark about every
// 45 seconds, like an open mic in a room.
func (r *runner) runLongMic() {
	st := r.st
	dur := time.Duration(*micMinutes * float64(time.Minute))
	end := time.Now().Add(dur)
	rng := rand.New(rand.NewSource(3))
	narr := []string{"narr1", "narr2", "narr3", "narr4", "narr5", "narr6"}
	next := time.Now().Add(20 * time.Second)
	ni := 0
	frame := make([]byte, frameBytes)
	var clip []byte
	pos := 0
	for time.Now().Before(end) {
		if clip == nil && time.Now().After(next) && ni < len(narr) {
			c, err := pcm(narr[ni])
			if err != nil {
				fatal(err.Error())
			}
			clip, pos = c, 0
			logj("mic_remark", map[string]any{"clip": narr[ni]})
			ni++
			next = time.Now().Add(45 * time.Second)
		}
		for i := 0; i < frameBytes; i += 2 {
			v := int16(rng.Intn(61) - 30) // ambient noise, far below speech
			if clip != nil && pos+i+1 < len(clip) {
				v = int16(binary.LittleEndian.Uint16(clip[pos+i:])) + v
			}
			binary.LittleEndian.PutUint16(frame[i:], uint16(v))
		}
		if clip != nil {
			pos += frameBytes
			if pos >= len(clip) {
				clip = nil
			}
		}
		if err := sendFrame(r.sess, frame); err != nil {
			logj("send_error", map[string]any{"err": err.Error()})
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	logj("mic_done", map[string]any{"minutes": *micMinutes, "remarks": ni})
	time.Sleep(6 * time.Second) // the last remark's turn and handle
	_ = st
}
