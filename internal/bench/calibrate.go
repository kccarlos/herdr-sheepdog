package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
)

// CalibrationPhrases are user lines the calibration renders, per language.
var CalibrationPhrases = map[string][]string{
	"en": {
		"What's the codex one doing right now?",
		"Tell bench-claude to run the tests and let me know when they pass.",
		"Yes, approve it, just this once.",
		"No wait, stop, don't do that.",
		"Which one is asking for permission?",
		"Call the README one docs from now on.",
	},
	"zh": {
		"那个 codex 现在在干嘛？",
		"让 bench-claude 跑一下测试，跑完告诉我。",
		"可以，批准吧，就这一次。",
		"先不要，等一下再说。",
		"哪个在等我批准？",
		"把写 README 的那个改叫文档。",
	},
}

// NoiseCond is one acoustic condition.
type NoiseCond struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	SNRDB float64 `json:"snr_db"`
}

// CalibrationNoise are the conditions: clean, a café at 10 dB SNR, pink
// noise at 5 dB.
var CalibrationNoise = []NoiseCond{{Name: "clean"}, {Name: "cafe-10dB", Kind: "cafe", SNRDB: 10}, {Name: "pink-5dB", Kind: "pink", SNRDB: 5}}

// CalibrationItem is one clip to send.
type CalibrationItem struct {
	Lang   string    `json:"lang"`
	Voice  string    `json:"voice"`
	Style  string    `json:"style"`
	Noise  NoiseCond `json:"noise"`
	Text   string    `json:"text"`
	Source string    `json:"source"` // gemini or say
}

// CalibrationPlan is the clip matrix: every voice and language over the
// phrases (styles rotating, the hesitant ones disfluent) clean; two
// voices per language over half the phrases in each noise; and the say
// fallback over half the phrases, clean.
func CalibrationPlan(seed int64) []CalibrationItem {
	rng := rand.New(rand.NewSource(seed))
	styles := []string{speech.StyleNeutral, speech.StyleTired, speech.StyleHurried, speech.StyleHesitant, speech.StyleCasual}
	var out []CalibrationItem
	for _, lang := range []string{"en", "zh"} {
		phrases := CalibrationPhrases[lang]
		for vi, v := range speech.Voices {
			for pi, p := range phrases {
				st := styles[(vi+pi)%len(styles)]
				text := p
				if st == speech.StyleHesitant {
					text = speech.Disfluent(p, lang, 0.6, rng)
				}
				out = append(out, CalibrationItem{Lang: lang, Voice: v, Style: st, Noise: CalibrationNoise[0], Text: text, Source: "gemini"})
			}
		}
		for _, n := range CalibrationNoise[1:] {
			for vi, v := range speech.Voices[:2] {
				for pi := vi; pi < len(phrases); pi += 2 {
					out = append(out, CalibrationItem{Lang: lang, Voice: v, Style: speech.StyleNeutral, Noise: n, Text: phrases[pi], Source: "gemini"})
				}
			}
		}
		for pi := 0; pi < len(phrases); pi += 2 {
			out = append(out, CalibrationItem{Lang: lang, Voice: "say", Noise: CalibrationNoise[0], Text: phrases[pi], Source: "say"})
		}
	}
	return out
}

// CalibrationResult is one clip's outcome.
type CalibrationResult struct {
	CalibrationItem
	Heard   string  `json:"heard"`
	WER     float64 `json:"wer"`
	DurMS   int     `json:"dur_ms"`
	Key     string  `json:"key"`
	Model   string  `json:"model,omitempty"`
	Err     string  `json:"error,omitempty"`
	clipped []int16 // the clip as streamed (noise mixed), for the samples
}

// CalibrationGroup aggregates WER over some clips.
type CalibrationGroup struct {
	Group string  `json:"group"`
	Clips int     `json:"clips"`
	Mean  float64 `json:"mean_wer"`
	// Missed counts clips with no input transcript at all.
	Missed int `json:"missed"`
}

// CalibrationReport is the calibration's outcome.
type CalibrationReport struct {
	Started    time.Time           `json:"started"`
	DurationS  int                 `json:"duration_s"`
	TTSModel   string              `json:"tts_model"`
	VoiceModel string              `json:"voice_model"`
	Results    []CalibrationResult `json:"results"`
	Groups     []CalibrationGroup  `json:"groups"`
	SamplesDir string              `json:"samples_dir,omitempty"`
	Spend      Spend               `json:"spend"`
}

// CalibrationOptions configure a calibration.
type CalibrationOptions struct {
	Daemon DaemonOptions
	// Synth renders the gemini clips, Say the fallback ones.
	Synth, Say speech.Synth
	Plan       []CalibrationItem
	// SamplesDir gets 10 sample clips and their transcripts for the user.
	SamplesDir string
	Log        io.Writer
	// Gate and GateStartsOpen are the emulator's capture gate.
	Gate           string
	GateStartsOpen bool
}

// Calibrate streams every clip into a scratch daemon (voice on, orchestrator
// off) and scores the voice model's input transcript against the text.
func Calibrate(ctx context.Context, o CalibrationOptions) (CalibrationReport, error) {
	rep := CalibrationReport{Started: time.Now()}
	if o.Synth != nil {
		rep.TTSModel = o.Synth.ID()
	}
	o.Daemon.Brain = app.BrainOff
	d, err := StartDaemon(ctx, o.Daemon)
	if err != nil {
		return rep, err
	}
	defer d.Close()
	rep.VoiceModel = o.Daemon.Config.Gemini.Model
	bed, _ := NewBed("", 0)
	beds := map[string]*Bed{}
	cur := bed
	c, err := voiceclient.Dial(ctx, voiceclient.Options{BaseURL: d.URL, Bed: func(n int) []int16 { return cur.Next(n) }, Gate: o.Gate, GateStartsOpen: o.GateStartsOpen})
	if err != nil {
		return rep, err
	}
	defer c.Close()
	if err := c.WaitConnected(ctx, 30*time.Second); err != nil {
		return rep, err
	}
	c.WaitQuiet(ctx, 2*time.Second, 10*time.Second)
	spend := NewSpendMeter(o.Daemon.Config.Gemini.Model)
	for i, it := range o.Plan {
		if ctx.Err() != nil {
			break
		}
		res := CalibrationResult{CalibrationItem: it}
		syn := o.Synth
		if it.Source == "say" {
			syn = o.Say
		}
		clip, err := syn.Synthesize(ctx, speech.Request{Text: it.Text, Lang: it.Lang, Voice: it.Voice, Style: it.Style})
		if err != nil {
			res.Err = err.Error()
			rep.Results = append(rep.Results, res)
			logf(o.Log, "[%d/%d] %s %s: tts error: %v", i+1, len(o.Plan), it.Lang, it.Voice, err)
			continue
		}
		spend.TTS(clip)
		samples := speech.Trim(clip.Samples, 60, 120)
		res.DurMS, res.Key, res.Model = pcm.DurationMS(len(samples), speech.Rate), clip.Key, clip.Model
		// The room for this clip.
		if it.Noise.Kind == "" {
			cur = bed
		} else {
			b := beds[it.Noise.Kind]
			if b == nil {
				b, _ = NewBed(it.Noise.Kind, int64(len(beds))+11)
				beds[it.Noise.Kind] = b
			}
			b.SetSNR(samples, it.Noise.SNRDB)
			cur = b
		}
		start := c.SinceMS()
		// The clip as the microphone hears it (for the samples).
		res.clipped = pcm.Mix(samples, cur.peekLike(len(samples)))
		if err := c.Say(ctx, it.Text, samples); err != nil {
			return rep, err
		}
		// Room tone after the words, then wait for the bot to finish.
		waitIn(ctx, c, start, 6*time.Second)
		c.WaitQuiet(ctx, 1500*time.Millisecond, 20*time.Second)
		cur = bed
		res.Heard = c.InTranscriptSince(start)
		res.WER = ErrorRate(it.Text, res.Heard)
		rep.Results = append(rep.Results, res)
		logf(o.Log, "[%d/%d] %s %-7s %-8s %-9s WER %.2f  %q -> %q", i+1, len(o.Plan), it.Lang, it.Voice, it.Style, it.Noise.Name, res.WER, it.Text, res.Heard)
	}
	rec := c.Close()
	spend.Live(rec.Events)
	spend.Audio(AudioMS(rec))
	rep.Spend = spend.Total()
	rep.Groups = calibrationGroups(rep.Results)
	rep.DurationS = int(time.Since(rep.Started).Seconds())
	if o.SamplesDir != "" {
		if err := writeSamples(o.SamplesDir, rep.Results); err != nil {
			return rep, err
		}
		rep.SamplesDir = o.SamplesDir
	}
	return rep, nil
}

// peekLike returns n samples of the bed's sound without the exact phase
// the client will use (a representative mix for the saved sample).
func (b *Bed) peekLike(n int) []int16 {
	b.mu.Lock()
	pos, fpos := b.pos, b.fpos
	b.mu.Unlock()
	out := b.Next(n)
	b.mu.Lock()
	b.pos, b.fpos = pos, fpos
	b.mu.Unlock()
	return out
}

// waitIn waits until an input transcript arrived after start and then
// stopped growing for a second, at most max after the call.
func waitIn(ctx context.Context, c *voiceclient.Client, start int64, max time.Duration) {
	end := time.Now().Add(max)
	last, since := "", time.Now()
	for time.Now().Before(end) && ctx.Err() == nil {
		got := c.InTranscriptSince(start)
		if got != last {
			last, since = got, time.Now()
		}
		if got != "" && time.Since(since) > time.Second {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func calibrationGroups(rs []CalibrationResult) []CalibrationGroup {
	type acc struct {
		n, missed int
		sum       float64
	}
	m := map[string]*acc{}
	add := func(k string, r CalibrationResult) {
		if m[k] == nil {
			m[k] = &acc{}
		}
		m[k].n++
		m[k].sum += r.WER
		if r.Heard == "" {
			m[k].missed++
		}
	}
	for _, r := range rs {
		if r.Err != "" {
			continue
		}
		add("all", r)
		add("lang="+r.Lang, r)
		add("voice="+r.Voice+" lang="+r.Lang, r)
		add("noise="+r.Noise.Name+" lang="+r.Lang, r)
		add("style="+orStr(r.Style, "neutral")+" lang="+r.Lang, r)
	}
	var out []CalibrationGroup
	for k, a := range m {
		out = append(out, CalibrationGroup{Group: k, Clips: a.n, Mean: a.sum / float64(a.n), Missed: a.missed})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group == "all" || out[j].Group == "all" {
			return out[i].Group == "all"
		}
		return out[i].Group < out[j].Group
	})
	return out
}

// writeSamples saves 10 clips spread over the conditions (as the
// microphone heard them) with their transcripts.
func writeSamples(dir string, rs []CalibrationResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	taken := map[int]bool{}
	var pick []CalibrationResult
	for pass := 0; pass < 2 && len(pick) < 10; pass++ {
		for i, r := range rs {
			if len(pick) >= 10 || taken[i] || r.Err != "" || r.clipped == nil {
				continue
			}
			k := r.Lang + r.Noise.Name + r.Style + r.Source
			if pass == 0 && seen[k] {
				continue
			}
			seen[k], taken[i] = true, true
			pick = append(pick, r)
		}
	}
	var b strings.Builder
	b.WriteString("# Voice bench calibration samples\n\nEach clip is what the scratch daemon's microphone input carried (the TTS line with the room noise mixed in), and what Gemini Live's input transcription heard.\n\n| # | File | Lang | Voice | Style | Noise | Said | Heard | WER |\n|---|---|---|---|---|---|---|---|---|\n")
	for i, r := range pick {
		name := fmt.Sprintf("%02d-%s-%s-%s-%s.wav", i+1, r.Lang, strings.ToLower(r.Voice), orStr(r.Style, "neutral"), r.Noise.Name)
		if err := speech.WriteWAV(filepath.Join(dir, name), r.clipped, speech.Rate); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s | %s | %.2f |\n", i+1, name, r.Lang, r.Voice, orStr(r.Style, "neutral"), r.Noise.Name, mdCell(r.Text), mdCell(orStr(r.Heard, "(nothing)")), r.WER)
	}
	if err := os.WriteFile(filepath.Join(dir, "transcripts.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	j, _ := json.MarshalIndent(pick, "", "  ")
	return os.WriteFile(filepath.Join(dir, "transcripts.json"), j, 0o644)
}

func mdCell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format+"\n", args...)
	}
}

// WriteCalibrationMarkdown renders the report.
func WriteCalibrationMarkdown(w io.Writer, rep CalibrationReport) {
	_, _ = fmt.Fprintf(w, "# Voice bench calibration, %s\n\n", rep.Started.UTC().Format("2006-01-02 15:04Z"))
	_, _ = fmt.Fprintf(w, "TTS `%s` -> phone emulator -> scratch daemon -> Gemini Live `%s` (input transcription). %d clips in %d s. Word error rate (character error rate for Chinese) after normalising case, punctuation and fillers.\n\n", rep.TTSModel, rep.VoiceModel, len(rep.Results), rep.DurationS)
	if rep.SamplesDir != "" {
		_, _ = fmt.Fprintf(w, "Sample clips with transcripts: `%s` (transcripts.md).\n\n", rep.SamplesDir)
	}
	_, _ = fmt.Fprintf(w, "| Group | Clips | Mean WER | No transcript |\n|---|---|---|---|\n")
	for _, g := range rep.Groups {
		_, _ = fmt.Fprintf(w, "| %s | %d | %.3f | %d |\n", g.Group, g.Clips, g.Mean, g.Missed)
	}
	_, _ = fmt.Fprintf(w, "\n## Worst clips\n\n| Lang | Voice | Style | Noise | Said | Heard | WER |\n|---|---|---|---|---|---|---|\n")
	rs := append([]CalibrationResult(nil), rep.Results...)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].WER > rs[j].WER })
	for i, r := range rs {
		if i >= 12 || r.WER == 0 {
			break
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %.2f |\n", r.Lang, r.Voice, orStr(r.Style, "neutral"), r.Noise.Name, mdCell(r.Text), mdCell(orStr(r.Heard, "(nothing)")), r.WER)
	}
	_, _ = fmt.Fprintf(w, "\nEstimated spend: $%.3f (%s).\n", rep.Spend.TotalUSD, rep.Spend.Summary())
}
