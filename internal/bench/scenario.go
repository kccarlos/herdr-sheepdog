package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// File is a bench scenarios file (testdata/bench/scenarios/*.json).
type File struct {
	Scenarios []Scenario `json:"scenarios"`
}

// Scenario is one voice-bench conversation: an eval sim scenario (the
// CEO's goal, persona and opening; the scripted workers; the rubric and
// the action checks), either inline or taken from the sim's files by id
// (FromSim) with any field here overriding it, plus how it sounds and how
// the simulated CEO behaves on the phone.
type Scenario struct {
	eval.SimScenario
	// Category groups the scenarios in the report (status, approve, …).
	Category string `json:"category"`
	// FromSim names a scenario of testdata/eval/sim to start from.
	FromSim string `json:"from_sim,omitempty"`
	// Audio is how the CEO sounds.
	Audio Audio `json:"audio,omitempty"`
	// Behavior is how the CEO behaves on the call.
	Behavior Behavior `json:"behavior,omitempty"`
	// Faults are injected into the scratch daemon's brain provider.
	Faults Faults `json:"faults,omitempty"`
	// Voice are voice-layer checks on the daemon's events.
	Voice VoiceExpect `json:"voice_expect,omitempty"`
	// Seed makes the run's random choices (voice, think times, barge-in,
	// disfluencies) repeatable; each repeat adds its index.
	Seed int64 `json:"seed,omitempty"`
}

// Audio is the acoustic side of a scenario.
type Audio struct {
	// Voice is a Gemini prebuilt voice (default: picked by the seed).
	Voice string `json:"voice,omitempty"`
	// Style is a speech style for every line, or "mixed" for one picked
	// per line by the seed (default neutral).
	Style string `json:"style,omitempty"`
	// Disfluency is the chance of a filler and of a restart per line.
	Disfluency float64 `json:"disfluency,omitempty"`
	// Noise is a room bed under everything.
	Noise *NoiseCond `json:"noise,omitempty"`
	// FloorDBFS is the microphone's self-noise (default -75 dBFS; 0 is
	// digital silence).
	FloorDBFS float64 `json:"floor_dbfs,omitempty"`
	// Echo leaks the bot's played audio back into the microphone.
	Echo *voiceclient.Echo `json:"echo,omitempty"`
	// Gate is the capture gate: ios (default) or web.
	Gate string `json:"gate,omitempty"`
}

// Behavior are the simulated CEO's phone habits.
type Behavior struct {
	// ThinkMS is the pause before each reply, [min, max] (default
	// [500, 2000]).
	ThinkMS []int `json:"think_ms,omitempty"`
	// BargeIn talks over a long answer.
	BargeIn *BargeIn `json:"barge_in,omitempty"`
	// Collision talks just as a scripted event's push arrives.
	Collision *Collision `json:"collision,omitempty"`
	// Reconnect drops the phone's connection for a while.
	Reconnect *Reconnect `json:"reconnect,omitempty"`
	// Resume forces one Gemini session resume mid-call.
	Resume *Resume `json:"resume,omitempty"`
}

// BargeIn: with probability Prob per answer, once a bot utterance has
// played AfterMS ([min, max]), the CEO starts talking over it — Line, or
// the simulated user's next line. Local makes the phone cut its own
// playback at once (the iOS hold-to-talk press; open-mic leaves it to
// the voice model).
type BargeIn struct {
	Prob    float64 `json:"prob"`
	AfterMS []int   `json:"after_ms,omitempty"`
	Line    string  `json:"line,omitempty"`
	Local   bool    `json:"local,omitempty"`
	// Max is how many times per call (default 1).
	Max int `json:"max,omitempty"`
}

// Collision: OffsetMS after scripted event Event lands (its push is on
// the way), the CEO says Line.
type Collision struct {
	Event    int    `json:"event"`
	OffsetMS int    `json:"offset_ms"`
	Line     string `json:"line"`
}

// Reconnect: after the answer to the CEO's AfterTurn-th utterance, the
// phone detaches for AwayMS (pushes are held meanwhile), then comes back.
type Reconnect struct {
	AfterTurn int `json:"after_turn"`
	AwayMS    int `json:"away_ms"`
}

// Resume: after the answer to the CEO's AfterTurn-th utterance the
// daemon's voice connection is recycled (the GoAway path: a new
// connection resumed from the latest handle, the old one closed), so
// the following turns show what the model kept. Measurement scenario for
// docs/research/live-resume-context-2026-10-04.md.
type Resume struct {
	AfterTurn int `json:"after_turn"`
}

// Faults are injected between the scratch daemon and DeepSeek.
type Faults struct {
	// BrainFailFirst answers the first N DeepSeek requests with
	// BrainFailStatus (default 503): the failover path.
	BrainFailFirst  int `json:"brain_fail_first,omitempty"`
	BrainFailStatus int `json:"brain_fail_status,omitempty"`
	// BrainDelayMS delays every DeepSeek request (slowness).
	BrainDelayMS int `json:"brain_delay_ms,omitempty"`
}

// Active reports whether any fault is configured.
func (f Faults) Active() bool { return f.BrainFailFirst > 0 || f.BrainDelayMS > 0 }

// VoiceExpect are voice-layer checks (failures are labelled check,
// "voice: …").
type VoiceExpect struct {
	// Events must each appear (type, and these payload fields equal).
	Events []EventMatch `json:"events,omitempty"`
	// Absent must not appear.
	Absent []EventMatch `json:"absent,omitempty"`
	// OpeningHeard: the voice model's input transcript of the opening
	// must fuzzily contain one of these.
	OpeningHeard []string `json:"opening_heard,omitempty"`
}

// EventMatch matches a daemon event.
type EventMatch struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
	Note    string         `json:"note,omitempty"`
}

// Categories are the known scenario categories, in report order.
var Categories = []string{"status", "deep_ask", "instruct", "approve", "deferral", "stop", "ambiguous", "names", "language", "collision", "barge_in", "noise", "echo", "reconnect", "failover", "unheard", "slowness"}

// LoadScenarios reads every file of glob, resolving from_sim against
// the sim scenarios in simGlob, and validates them.
func LoadScenarios(glob, simGlob string) ([]Scenario, error) {
	files, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("bench: no scenario files match %s", glob)
	}
	sims := map[string]eval.SimScenario{}
	simFiles, _ := filepath.Glob(simGlob)
	for _, f := range simFiles {
		scs, err := eval.LoadSimFile(f)
		if err != nil {
			return nil, err
		}
		for _, s := range scs {
			sims[s.ID] = s
		}
	}
	simScreens := ""
	if len(simFiles) > 0 {
		simScreens = filepath.Join(filepath.Dir(simFiles[0]), "screens")
	}
	var out []Scenario
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var file File
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&file); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, s := range file.Scenarios {
			if s.FromSim != "" {
				base, ok := sims[s.FromSim]
				if !ok {
					return nil, fmt.Errorf("%s: scenario %q: from_sim %q is not a sim scenario", f, s.ID, s.FromSim)
				}
				s.SimScenario = merge(base, s.SimScenario)
			}
			// Bare screen names: the sim's screens/ directory, then the
			// herdr fixtures (as for the sim's own scenarios).
			s.SetScreenDir(simScreens)
			if seen[s.ID] {
				return nil, fmt.Errorf("%s: duplicate scenario id %q", f, s.ID)
			}
			seen[s.ID] = true
			if err := s.validate(); err != nil {
				return nil, fmt.Errorf("%s: scenario %q: %w", f, s.ID, err)
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// merge lays the fields set in over onto base.
func merge(base, over eval.SimScenario) eval.SimScenario {
	out := base
	out.ID = over.ID
	if over.Note != "" {
		out.Note = over.Note
	}
	if over.Goal != "" {
		out.Goal = over.Goal
	}
	if over.Persona != "" {
		out.Persona = over.Persona
	}
	if over.Language != "" {
		out.Language = over.Language
	}
	if over.Opening != "" {
		out.Opening = over.Opening
	}
	if len(over.Script) > 0 {
		out.Script, out.Fixed = over.Script, over.Fixed
	}
	if len(over.Agents) > 0 {
		out.Agents = over.Agents
	}
	if len(over.Events) > 0 {
		out.Events = over.Events
	}
	if len(over.Notes) > 0 {
		out.Notes = over.Notes
	}
	if len(over.Transcript) > 0 {
		out.Transcript = over.Transcript
	}
	if over.Caps != (eval.SimCaps{}) {
		out.Caps = over.Caps
	}
	if len(over.Expect.Actions)+len(over.Expect.Forbid)+len(over.Expect.Rubric)+len(over.Expect.Skip) > 0 || over.Expect.NoActions || over.Expect.Agent != "" {
		out.Expect = over.Expect
	}
	return out
}

func (s Scenario) validate() error {
	if err := s.Validate(); err != nil {
		return err
	}
	if len(s.Notes) > 0 || len(s.Transcript) > 0 {
		return fmt.Errorf("notes and transcript seed the in-process brain; a scratch daemon has no way to take them")
	}
	if !knownCategory(s.Category) {
		return fmt.Errorf("category %q: want one of %s", s.Category, strings.Join(Categories, ", "))
	}
	a := s.Audio
	if a.Voice != "" && !contains(speech.Voices, a.Voice) {
		return fmt.Errorf("audio.voice %q: want one of %s", a.Voice, strings.Join(speech.Voices, ", "))
	}
	if a.Style != "" && a.Style != "mixed" && !speech.ValidStyle(a.Style) {
		return fmt.Errorf("audio.style %q", a.Style)
	}
	if a.Gate != "" && a.Gate != voiceclient.GateIOS && a.Gate != voiceclient.GateWeb {
		return fmt.Errorf("audio.gate %q: want ios or web", a.Gate)
	}
	if a.Noise != nil {
		if _, err := NewBed(a.Noise.Kind, 0); err != nil {
			return fmt.Errorf("audio.noise: %w", err)
		}
	}
	if a.Disfluency < 0 || a.Disfluency > 1 {
		return fmt.Errorf("audio.disfluency %v: want 0..1", a.Disfluency)
	}
	b := s.Behavior
	if err := checkRange("behavior.think_ms", b.ThinkMS); err != nil {
		return err
	}
	if b.BargeIn != nil {
		if b.BargeIn.Prob <= 0 || b.BargeIn.Prob > 1 {
			return fmt.Errorf("behavior.barge_in.prob %v: want (0, 1]", b.BargeIn.Prob)
		}
		if err := checkRange("behavior.barge_in.after_ms", b.BargeIn.AfterMS); err != nil {
			return err
		}
	}
	if c := b.Collision; c != nil {
		if c.Event < 0 || c.Event >= len(s.Events) {
			return fmt.Errorf("behavior.collision.event %d: the scenario has %d events", c.Event, len(s.Events))
		}
		if strings.TrimSpace(c.Line) == "" {
			return fmt.Errorf("behavior.collision.line is empty")
		}
	}
	if r := b.Reconnect; r != nil && (r.AfterTurn < 1 || r.AwayMS <= 0) {
		return fmt.Errorf("behavior.reconnect: after_turn >= 1 and away_ms > 0")
	}
	if r := b.Resume; r != nil && r.AfterTurn < 1 {
		return fmt.Errorf("behavior.resume: after_turn >= 1")
	}
	for _, m := range append(append([]EventMatch(nil), s.Voice.Events...), s.Voice.Absent...) {
		if m.Type == "" {
			return fmt.Errorf("voice_expect: an event match without a type")
		}
	}
	return nil
}

func checkRange(name string, r []int) error {
	if len(r) == 0 {
		return nil
	}
	if len(r) != 2 || r[0] < 0 || r[1] < r[0] {
		return fmt.Errorf("%s %v: want [min, max]", name, r)
	}
	return nil
}

func knownCategory(c string) bool { return contains(Categories, c) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// CategoryCounts counts scenarios per category (for the spec's table).
func CategoryCounts(scs []Scenario) map[string]int {
	m := map[string]int{}
	for _, s := range scs {
		m[s.Category]++
	}
	return m
}

// SortedCategories lists the categories present, in Categories order.
func SortedCategories(m map[string]int) []string {
	var out []string
	for _, c := range Categories {
		if m[c] > 0 {
			out = append(out, c)
		}
	}
	return out
}
