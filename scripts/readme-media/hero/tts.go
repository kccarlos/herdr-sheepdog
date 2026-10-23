//go:build ignore

// tts.go renders the hero and launch videos' voice lines to 24 kHz mono
// WAV files: Gemini TTS on Vertex AI with VERTEX_API_KEY and
// VERTEX_PROJECT from the environment (never printed; for example
// `op run --env-file=.env -- go run …`), or macOS `say` when there is no
// key or -say is set. A line already rendered (its WAV exists) is kept, so
// each line costs one request.
//
//	go run scripts/readme-media/hero/tts.go -lines lines.tsv -out DIR [-say] [-strict]
//
// lines.tsv: id<TAB>role<TAB>text, role "sheepdog" or "you".
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

const rate = 24000

var (
	geminiVoice = map[string]string{"sheepdog": "Kore", "you": "Puck"}
	sayVoice    = map[string]string{"sheepdog": "Samantha", "you": "Daniel"}
	style       = map[string]string{
		"sheepdog": "Say briskly and clearly, in a friendly voice, like an assistant giving a quick update in someone's earbuds, with no pauses between phrases",
		"you":      "Say casually, like someone asking a colleague a quick question while walking",
	}
)

func main() {
	lines := flag.String("lines", "", "lines.tsv")
	out := flag.String("out", "", "output directory")
	useSay := flag.Bool("say", false, "render with macOS say only")
	strict := flag.Bool("strict", false, "fail instead of falling back to say, so a failed line is retried next run")
	model := flag.String("model", "gemini-2.5-flash-tts", "Gemini TTS model")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	var m brain.Model
	if !*useSay {
		var err error
		if key := os.Getenv("VERTEX_API_KEY"); key != "" {
			m, err = brain.NewVertexModel(key, os.Getenv("VERTEX_PROJECT"), nil)
		}
		if m == nil {
			if *strict {
				fail(fmt.Errorf("no Vertex key (VERTEX_API_KEY) %v", err))
			}
			fmt.Fprintln(os.Stderr, "tts: no Vertex key, using say", err)
		}
	}
	f, err := os.Open(*lines)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 || strings.HasPrefix(parts[0], "#") {
			continue
		}
		id, role, text := parts[0], parts[1], parts[2]
		path := filepath.Join(*out, id+".wav")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		var pcm []byte
		if m != nil {
			pcm, err = gemini(m, *model, role, text)
			if err != nil {
				if *strict {
					fail(fmt.Errorf("%s: gemini failed: %v", id, err))
				}
				fmt.Fprintf(os.Stderr, "tts: %s: gemini failed (%v), using say\n", id, err)
			}
		}
		if pcm == nil {
			pcm, err = say(role, text)
			if err != nil {
				fail(err)
			}
		}
		if err := writeWAV(path, pcm); err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "tts: %s %.1f s\n", id, float64(len(pcm))/2/rate)
	}
	if err := sc.Err(); err != nil {
		fail(err)
	}
}

func gemini(m brain.Model, model, role, text string) ([]byte, error) {
	cfg := &genai.GenerateContentConfig{
		ResponseModalities: []string{"AUDIO"},
		SpeechConfig: &genai.SpeechConfig{VoiceConfig: &genai.VoiceConfig{
			PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: geminiVoice[role]},
		}},
	}
	var last error
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := m.Generate(ctx, model, genai.Text(style[role]+": "+text), cfg)
		cancel()
		if err != nil {
			last = err
			time.Sleep(time.Duration(3*(i+1)) * time.Second)
			continue
		}
		var raw []byte
		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			for _, p := range resp.Candidates[0].Content.Parts {
				if p.InlineData != nil {
					if !strings.Contains(p.InlineData.MIMEType, "rate=24000") && strings.Contains(p.InlineData.MIMEType, "rate=") {
						return nil, fmt.Errorf("unexpected audio format %s", p.InlineData.MIMEType)
					}
					raw = append(raw, p.InlineData.Data...)
				}
			}
		}
		if len(raw) == 0 {
			last = fmt.Errorf("no audio in the response")
			continue
		}
		return raw, nil
	}
	return nil, last
}

func say(role, text string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "hero-say-*.wav")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.Command("say", "-v", sayVoice[role], "--file-format=WAVE", "--data-format=LEI16@24000", "-o", tmp.Name(), text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("say: %v: %s", err, out)
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	// The PCM after the "data" chunk header.
	i := strings.Index(string(b), "data")
	if i < 0 {
		return nil, fmt.Errorf("say: no data chunk")
	}
	return b[i+8:], nil
}

func writeWAV(path string, pcm []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], rate)
	binary.LittleEndian.PutUint32(h[28:], rate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	if _, err := f.Write(h); err != nil {
		return err
	}
	_, err = f.Write(pcm)
	return err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tts:", err)
	os.Exit(1)
}
