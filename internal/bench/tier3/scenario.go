// Package tier3 is the tier-3 end-to-end bench (`sheepdog bench run
// --tier 3`): the real daemon driving real coding agents in a real herdr.
// Each run gets its own named herdr session (never the default one), a
// throwaway clone of the bench repo per workspace, real Claude Code or
// Codex workers started in its panes, and a scratch daemon bound to that
// session's socket. The simulated CEO of internal/eval talks to the
// daemon in text (POST /api/inject/utterance, the text router standing in
// for the voice model; `serve --heard-echo` reports what it says back as
// heard), hears its pushes (voice.push events), and the judge of
// internal/eval grades the conversation against the workers' real
// screens and what the daemon sent them. Everything is torn down after
// the run.
package tier3

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// File is a tier-3 scenarios file (testdata/bench/tier3/*.json). Its
// conversation fields are those of eval.SimScenario (a superset of the
// simulated-user format), so the same simulated user and judge run it.
type File struct {
	Scenarios []Scenario `json:"scenarios"`
}

// Scenario is one tier-3 run: real workers, a setup that puts them in
// the state under test, then a simulated call.
type Scenario struct {
	ID string `json:"id"`
	// Tier is 3 (the field lets one directory hold several tiers).
	Tier     int    `json:"tier,omitempty"`
	Note     string `json:"note,omitempty"`
	Goal     string `json:"goal"`
	Persona  string `json:"persona,omitempty"`
	Language string `json:"language,omitempty"`
	Opening  string `json:"opening,omitempty"`
	// Script and Fixed are as in eval.SimScenario: Fixed plays Script
	// verbatim ("<wait>" waits) instead of the LLM simulated user.
	Script []string     `json:"script,omitempty"`
	Fixed  bool         `json:"fixed,omitempty"`
	Caps   eval.SimCaps `json:"caps,omitempty"`
	// Workers are the real agents, in start order.
	Workers []Worker `json:"workers"`
	// Audio and Behavior are how the CEO sounds and acts in --voice mode
	// (tier 2's types: voice, style, disfluency, noise, echo, gate; think
	// time and barge-in). Collision and reconnect are not supported on a
	// real flock. Text mode ignores both.
	Audio    bench.Audio    `json:"audio,omitempty"`
	Behavior bench.Behavior `json:"behavior,omitempty"`
	// Setup runs before the call, in order (prompts typed straight into
	// a worker, as the CEO would have at the terminal earlier).
	Setup  []Step `json:"setup,omitempty"`
	Expect Expect `json:"expect"`
}

// Worker is one real coding agent.
type Worker struct {
	// Alias is the alias the daemon derives for it (workspace label +
	// kind, "-2" for a second one); expectations name workers by it.
	Alias string `json:"alias"`
	// Workspace is the herdr workspace label. Workers with the same
	// label share the workspace (split panes) and its repo clone.
	Workspace string `json:"workspace"`
	// Kind is claude or codex.
	Kind string `json:"kind"`
	// Model is passed as --model (claude) or -m (codex); default haiku
	// for claude, the bench's Codex model for codex.
	Model string `json:"model,omitempty"`
	// Args are more CLI arguments (e.g. a permission mode).
	Args []string `json:"args,omitempty"`
	// Trust is what the runner does with Claude Code's folder trust
	// prompt on a fresh clone: accept (default) before the daemon starts,
	// or leave it for sheepdog to handle.
	Trust string `json:"trust,omitempty"`
}

// Step is one setup action on a worker.
type Step struct {
	Worker string `json:"worker"`
	// Prompt is submitted to the worker (herdr agent prompt).
	Prompt string `json:"prompt,omitempty"`
	// Keys are sent to the worker's pane instead.
	Keys []string `json:"keys,omitempty"`
	// WaitFor waits until the worker's herdr status is one of these
	// (working, blocked, idle, done), up to TimeoutS.
	WaitFor []string `json:"wait_for,omitempty"`
	// WaitScreen waits until the pane's text matches this regexp.
	WaitScreen string `json:"wait_screen,omitempty"`
	// TimeoutS bounds the wait (default 180).
	TimeoutS int `json:"timeout_s,omitempty"`
	// SleepS pauses after the step.
	SleepS int `json:"sleep_s,omitempty"`
}

// Expect is what a run checks besides the judge's rubric.
type Expect struct {
	// The simulated-user expectations: actions, forbid, no_actions,
	// agent, rubric, skip (eval.SimExpect).
	eval.SimExpect
	// Events must (or, with Max 0 and Absent, must not) appear in the
	// daemon's event log.
	Events []EventCheck `json:"events,omitempty"`
	// Screens are checked on the workers' panes after the call.
	Screens []ScreenCheck `json:"screens,omitempty"`
	// Commands run in a worker's repo after the call (e.g. go test).
	Commands []CommandCheck `json:"commands,omitempty"`
}

// EventCheck counts daemon events of a type (prefix) whose JSON payload
// matches a regexp.
type EventCheck struct {
	Type    string `json:"type"`
	Matches string `json:"matches,omitempty"`
	// Absent: no such event may appear.
	Absent bool `json:"absent,omitempty"`
	// Why says what the check is about, for the report.
	Why string `json:"why,omitempty"`
}

// ScreenCheck matches a worker's final screen.
type ScreenCheck struct {
	Worker  string `json:"worker"`
	Matches string `json:"matches"`
	Absent  bool   `json:"absent,omitempty"`
	Why     string `json:"why,omitempty"`
}

// CommandCheck runs a command in a worker's repo after the call.
type CommandCheck struct {
	Worker string   `json:"worker"`
	Argv   []string `json:"argv"`
	// Exit is the wanted exit code.
	Exit int    `json:"exit"`
	Why  string `json:"why,omitempty"`
}

// Kinds.
const (
	KindClaude = "claude"
	KindCodex  = "codex"
)

// Trust handling.
const (
	TrustAccept = "accept"
	TrustLeave  = "leave"
)

// Load reads and validates a scenarios file.
func Load(path string) ([]Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, s := range f.Scenarios {
		if seen[s.ID] {
			return nil, fmt.Errorf("%s: duplicate scenario id %q", path, s.ID)
		}
		seen[s.ID] = true
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("%s: scenario %q: %w", path, s.ID, err)
		}
	}
	return f.Scenarios, nil
}

// LoadGlob loads every file of a glob, keeping the scenarios whose id
// contains filter.
func LoadGlob(glob, filter string) ([]Scenario, error) {
	files, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	var out []Scenario
	for _, f := range files {
		scs, err := Load(f)
		if err != nil {
			return nil, err
		}
		for _, s := range scs {
			if filter == "" || strings.Contains(s.ID, filter) {
				out = append(out, s)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tier-3 scenarios in %s (filter %q)", glob, filter)
	}
	return out, nil
}

// Validate checks a scenario.
func (s Scenario) Validate() error {
	if s.Tier != 0 && s.Tier != 3 {
		return fmt.Errorf("tier %d: want 3", s.Tier)
	}
	if len(s.Workers) == 0 {
		return fmt.Errorf("no workers")
	}
	workers := map[string]Worker{}
	for _, w := range s.Workers {
		if w.Alias == "" || workers[w.Alias].Alias != "" {
			return fmt.Errorf("worker alias %q empty or repeated", w.Alias)
		}
		if w.Workspace == "" {
			return fmt.Errorf("worker %s: no workspace", w.Alias)
		}
		switch w.Kind {
		case KindClaude, KindCodex:
		default:
			return fmt.Errorf("worker %s: kind %q: want claude or codex", w.Alias, w.Kind)
		}
		switch w.Trust {
		case "", TrustAccept, TrustLeave:
		default:
			return fmt.Errorf("worker %s: trust %q: want accept or leave", w.Alias, w.Trust)
		}
		workers[w.Alias] = w
	}
	for i, st := range s.Setup {
		if _, ok := workers[st.Worker]; !ok {
			return fmt.Errorf("setup %d: unknown worker %q", i, st.Worker)
		}
		if st.Prompt != "" && len(st.Keys) > 0 {
			return fmt.Errorf("setup %d: prompt and keys both set", i)
		}
		for _, w := range st.WaitFor {
			switch w {
			case "working", "blocked", "idle", "done":
			default:
				return fmt.Errorf("setup %d: wait_for %q", i, w)
			}
		}
		if err := checkRe(st.WaitScreen); err != nil {
			return fmt.Errorf("setup %d: wait_screen: %w", i, err)
		}
	}
	for i, e := range s.Expect.Events {
		if e.Type == "" {
			return fmt.Errorf("expect.events %d: no type", i)
		}
		if err := checkRe(e.Matches); err != nil {
			return fmt.Errorf("expect.events %d: %w", i, err)
		}
	}
	for i, c := range s.Expect.Screens {
		if _, ok := workers[c.Worker]; !ok || c.Matches == "" {
			return fmt.Errorf("expect.screens %d: unknown worker or no pattern", i)
		}
		if err := checkRe(c.Matches); err != nil {
			return fmt.Errorf("expect.screens %d: %w", i, err)
		}
	}
	for i, c := range s.Expect.Commands {
		if _, ok := workers[c.Worker]; !ok || len(c.Argv) == 0 {
			return fmt.Errorf("expect.commands %d: unknown worker or no argv", i)
		}
	}
	if s.Behavior.Collision != nil || s.Behavior.Reconnect != nil {
		return fmt.Errorf("behavior: collision and reconnect are tier-2 only")
	}
	// The conversation half is checked as a simulated-user scenario.
	return s.Sim().Validate()
}

func checkRe(p string) error {
	if p == "" {
		return nil
	}
	_, err := regexp.Compile("(?i)" + p)
	return err
}

// Sim is the scenario as the simulated user and the judge see it: the
// workers are its agents (their real screens replace the scripted ones).
func (s Scenario) Sim() eval.SimScenario {
	sc := eval.SimScenario{
		ID: s.ID, Note: s.Note, Goal: s.Goal, Persona: s.Persona, Language: s.Language,
		Opening: s.Opening, Script: s.Script, Fixed: s.Fixed, Caps: s.Caps, Expect: s.Expect.SimExpect,
	}
	for _, w := range s.Workers {
		sc.Agents = append(sc.Agents, eval.SimAgent{Alias: w.Alias, Kind: w.Kind, Screen: "(real worker)"})
	}
	return sc
}

// caps are the tier-3 defaults: real workers take tens of seconds a
// turn, so the call and its silences are longer than in eval sim.
func (s Scenario) caps() eval.SimCaps {
	c := s.Caps
	if c.MaxTurns <= 0 {
		c.MaxTurns = 8
	}
	if c.MaxSeconds <= 0 {
		c.MaxSeconds = 600
	}
	if c.SilenceSeconds <= 0 {
		c.SilenceSeconds = 150
	}
	return c
}
