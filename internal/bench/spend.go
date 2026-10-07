package bench

import (
	"fmt"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// Spend is an estimated bill: per role and model (eval.Prices, list
// prices as ESTIMATES), plus the Live session's audio minutes, priced
// separately from its tokens in the report.
type Spend struct {
	TotalUSD float64          `json:"total_usd_estimate"`
	Lines    []eval.MeterLine `json:"lines"`
	// LiveInMin and LiveOutMin are the audio minutes sent to and
	// received from the voice model.
	LiveInMin  float64 `json:"live_audio_in_min"`
	LiveOutMin float64 `json:"live_audio_out_min"`
}

// Summary is a one-line breakdown per role.
func (s Spend) Summary() string {
	by := map[string]float64{}
	var roles []string
	for _, l := range s.Lines {
		if _, ok := by[l.Role]; !ok {
			roles = append(roles, l.Role)
		}
		by[l.Role] += l.CostUSD
	}
	var parts []string
	for _, r := range roles {
		parts = append(parts, fmt.Sprintf("%s $%.3f", r, by[r]))
	}
	parts = append(parts, fmt.Sprintf("live audio %.1f min in / %.1f min out", s.LiveInMin, s.LiveOutMin))
	return strings.Join(parts, ", ")
}

// SpendMeter accumulates a run's or a campaign's spend. Safe for
// concurrent use.
type SpendMeter struct {
	Meter *eval.Meter
	// LiveModel is the voice model voice.usage is billed at.
	LiveModel string

	mu            sync.Mutex
	inMin, outMin float64
}

// NewSpendMeter returns an empty meter billing Live at liveModel.
func NewSpendMeter(liveModel string) *SpendMeter {
	if liveModel == "" {
		liveModel = "gemini-3.8-live"
	}
	return &SpendMeter{Meter: eval.NewMeter(), LiveModel: liveModel}
}

// TTS adds one rendered clip (nothing when it came from the cache or say).
func (m *SpendMeter) TTS(c speech.Clip) {
	if c.Cached || c.Source != "gemini" {
		return
	}
	m.Meter.Add("tts", c.Model, eval.Usage{Calls: 1, In: c.InTokens, Out: c.OutTokens})
}

// Live adds a recording's voice-model usage (voice.usage events) and
// orchestrator steps (brain.step events), and its audio minutes.
func (m *SpendMeter) Live(evs []voiceclient.Event) {
	for _, ev := range evs {
		switch ev.Type {
		case "voice.usage":
			m.Meter.Add("live", m.LiveModel, eval.Usage{Calls: 1, In: toInt(ev.Payload["prompt"]), Out: toInt(ev.Payload["response"])})
		case "brain.step":
			model, _ := ev.Payload["model"].(string)
			if model == "" {
				model = "deepseek-flash"
			}
			m.Meter.Add("brain", model, eval.Usage{Calls: 1, In: toInt(ev.Payload["prompt_tokens"]), Out: toInt(ev.Payload["completion_tokens"]), Reasoning: toInt(ev.Payload["reasoning_tokens"])})
		}
	}
}

// Audio adds audio minutes sent and received.
func (m *SpendMeter) Audio(inMS, outMS int64) {
	m.mu.Lock()
	m.inMin += float64(inMS) / 60000
	m.outMin += float64(outMS) / 60000
	m.mu.Unlock()
}

// Merge adds another meter.
func (m *SpendMeter) Merge(o *SpendMeter) {
	m.Meter.Merge(o.Meter)
	o.mu.Lock()
	in, out := o.inMin, o.outMin
	o.mu.Unlock()
	m.mu.Lock()
	m.inMin += in
	m.outMin += out
	m.mu.Unlock()
}

// Total is the spend so far.
func (m *SpendMeter) Total() Spend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Spend{TotalUSD: m.Meter.Total(), Lines: m.Meter.Lines(), LiveInMin: m.inMin, LiveOutMin: m.outMin}
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// AudioMS is the audio a recording sent to the daemon and received
// from it, in ms.
func AudioMS(rec voiceclient.Recording) (upMS, downMS int64) {
	var up, down int64
	for _, f := range rec.Frames {
		if f.Kind != "audio" {
			continue
		}
		if f.Dir == "up" {
			up += int64(f.Bytes)
		} else {
			down += int64(f.Bytes)
		}
	}
	return up * 1000 / (2 * voiceclient.InRate), down * 1000 / (2 * voiceclient.OutRate)
}
