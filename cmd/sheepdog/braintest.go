package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func printBrainTestUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog brain-test --question \"Q\" [flags]")
	fprintln(w, "")
	fprintln(w, "Runs one supervisor-brain turn (ask by default) against a mock herdr with one")
	fprintln(w, "agent whose screen is a testdata/herdr fixture, and prints the reply as JSON.")
	fprintln(w, "Every event (brain.turn, brain.step, brain.tool_call/tool_result, herdr.read,")
	fprintln(w, "…) goes to stderr as JSON lines. Needs the provider's key unless --fake.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH       config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH     load KEY=VALUE lines into the environment first")
	fprintln(w, "  --question TEXT     the assistant's question (ask) or instruction (propose)")
	fprintln(w, "  --kind KIND       utterance | ask | propose (default utterance: the shepherd's")
	fprintln(w, "                    words verbatim, which the brain classifies itself)")
	fprintln(w, "  --agent ALIAS       the agent hint, only if the CEO said a name")
	fprintln(w, "  --alias NAME        the mock agent's alias (default auth)")
	fprintln(w, "  --agent-kind KIND   the mock agent's kind (default codex)")
	fprintln(w, "  --state STATE       working | idle | done | blocked (default idle; blocked runs")
	fprintln(w, "                      the supervisor's block chain first)")
	fprintln(w, "  --fixture NAME      screen fixture under testdata/herdr, or a path (default")
	fprintln(w, "                      codex-after-approval.screen.txt)")
	fprintln(w, "  --transcript PATH   a brain transcript (JSONL) to seed the run with")
	fprintln(w, "  --say TEXT          a CEO utterance heard before the request (repeatable)")
	fprintln(w, "  --provider ID       brain provider: gemini | deepseek (default config brain.provider)")
	fprintln(w, "  --model NAME        brain model (default config brain.model)")
	fprintln(w, "  --thinking LEVEL    Gemini thinking level: minimal | low | medium | high (default low;")
	fprintln(w, "                      DeepSeek's thinking comes from [deepseek])")
	fprintln(w, "  --prompts-dir PATH  prompt override directory (default config prompts.dir)")
	fprintln(w, "  --timeout D         turn budget (default from config)")
	fprintln(w, "  --fake              use a scripted model (read_screen, then an answer): no network")
}

// brainTestPane is the mock agent's pane id.
const brainTestPane = "w1:p1"

type brainTestOpts struct {
	configPath, envFile, question, kind, agent, alias, agentKind, st, fixture, transcript, provider, model, thinking, promptsDir string
	say                                                                                                                          multiFlag
	timeout                                                                                                                      time.Duration
	fake                                                                                                                         bool
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, "; ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func parseBrainTestFlags(args []string, stderr io.Writer) (brainTestOpts, int, bool) {
	fs := flag.NewFlagSet("brain-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printBrainTestUsage(stderr) }
	var o brainTestOpts
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.envFile, "env-file", "", "env file")
	fs.StringVar(&o.question, "question", "", "question or instruction")
	fs.StringVar(&o.kind, "kind", string(brain.KindUtterance), "utterance | ask | propose")
	fs.StringVar(&o.agent, "agent", "", "agent hint")
	fs.StringVar(&o.alias, "alias", "auth", "mock agent alias")
	fs.StringVar(&o.agentKind, "agent-kind", "codex", "mock agent kind")
	fs.StringVar(&o.st, "state", "idle", "mock agent state")
	fs.StringVar(&o.fixture, "fixture", "codex-after-approval.screen.txt", "screen fixture")
	fs.StringVar(&o.transcript, "transcript", "", "transcript to seed")
	fs.Var(&o.say, "say", "CEO utterance (repeatable)")
	fs.StringVar(&o.provider, "provider", "", "brain provider")
	fs.StringVar(&o.model, "model", "", "brain model")
	fs.StringVar(&o.thinking, "thinking", "", "thinking level")
	fs.StringVar(&o.promptsDir, "prompts-dir", "", "prompt override directory (default config prompts.dir)")
	fs.DurationVar(&o.timeout, "timeout", 0, "turn budget")
	fs.BoolVar(&o.fake, "fake", false, "scripted model")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.question == "" {
		fprintln(stderr, "sheepdog brain-test: needs --question")
		return o, 2, false
	}
	switch o.kind {
	case string(brain.KindUtterance), string(brain.KindAsk), string(brain.KindPropose):
	default:
		fprintln(stderr, "sheepdog brain-test: --kind must be utterance, ask or propose")
		return o, 2, false
	}
	switch herdr.State(o.st) {
	case herdr.StateWorking, herdr.StateIdle, herdr.StateDone, herdr.StateBlocked:
	default:
		fprintln(stderr, "sheepdog brain-test: --state must be working, idle, done or blocked")
		return o, 2, false
	}
	return o, 0, true
}

// runBrainTest implements `sheepdog brain-test`.
func runBrainTest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseBrainTestFlags(args, stderr)
	if !run {
		return code
	}
	if o.envFile != "" {
		if err := config.LoadEnvFile(o.envFile); err != nil {
			fprintf(stderr, "sheepdog brain-test: %v\n", err)
			return 1
		}
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	fixture := o.fixture
	if !strings.ContainsRune(fixture, filepath.Separator) {
		fixture = filepath.Join("testdata", "herdr", fixture)
	}
	screen, err := os.ReadFile(fixture)
	if err != nil {
		fprintf(stderr, "sheepdog brain-test: fixture: %v\n", err)
		return 1
	}
	log := logging.NewJSONLLogger(stderr)

	dir, err := os.MkdirTemp("", "brain-test")
	if err != nil {
		fprintf(stderr, "sheepdog brain-test: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	model, err := brainTestModel(o, cfg, log)
	if err != nil {
		fprintf(stderr, "sheepdog brain-test: %v\n", err)
		return 1
	}
	bcfg := app.BrainConfig(cfg)
	bcfg.DataDir = dir
	if o.provider != "" {
		bcfg.Provider = o.provider
	}
	if o.model != "" {
		bcfg.Model = o.model
	}
	bcfg.ThinkingLevel = o.thinking
	promptsDir := o.promptsDir
	if promptsDir == "" {
		promptsDir = cfg.Prompts.Dir
	}
	bcfg.Prompts = prompts.NewLoader(promptsDir, log)
	if o.timeout > 0 {
		bcfg.AskTimeout, bcfg.ProposeTimeout = o.timeout, o.timeout
	}
	b, err := brain.New(bcfg, model, log)
	if err != nil {
		fprintf(stderr, "sheepdog brain-test: %v\n", err)
		return 1
	}
	defer func() { _ = b.Close() }()

	sup, mock, err := brainTestSupervisor(dir, o.alias, b, log, stderr)
	if err != nil {
		fprintf(stderr, "sheepdog brain-test: %v\n", err)
		return 1
	}
	mock.SetScreen(brainTestPane, o.agentKind, string(screen), herdr.State(o.st))
	sup.SetVoiceConnected(true)
	sup.HandleTransition(ctx, herdr.Transition{PaneID: brainTestPane, To: herdr.State(o.st), At: time.Now(), Agent: o.agentKind, WorkspaceID: "ws", WorkspaceLabel: o.alias, Source: state.SourceInject})
	if o.transcript != "" {
		msgs, err := brain.ReadTranscript(o.transcript)
		if err != nil {
			fprintf(stderr, "sheepdog brain-test: %v\n", err)
			return 1
		}
		b.Seed(msgs)
	}
	for _, s := range o.say {
		b.Heard(s, true)
	}

	start := time.Now()
	var res voice.ToolResult
	switch o.kind {
	case string(brain.KindPropose):
		res, err = sup.Propose(ctx, o.question, o.agent)
	case string(brain.KindAsk):
		res, err = sup.Ask(ctx, o.question, o.agent)
	default:
		// --agent is only a hint for the older kinds; an utterance
		// reaches the brain exactly as the shepherd said it.
		// Typed, as far as the one rail is concerned: the question is
		// the shepherd's own words.
		sup.ShepherdInput(supervisor.InputTyped, o.question)
		res, err = sup.ToSupervisor(ctx, o.question)
	}
	b.Quiesce()
	out := map[string]any{
		"kind": o.kind, "question": o.question, "agent": o.agent, "dur_ms": time.Since(start).Milliseconds(),
		"result":     map[string]any{"spoken": res.Spoken, "detail": res.Detail, "options": res.Options, "clarify": res.Clarify, "pending": res.Pending, "scheduling": res.Scheduling.String()},
		"transcript": transcriptForReport(b.Transcript().Messages()),
	}
	if err != nil {
		out["error"] = err.Error()
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}

// transcriptForReport drops the replayed model content (base64 thought
// signatures, reasoning blobs) from the transcript so the report stays
// readable.
func transcriptForReport(msgs []brain.Message) []brain.Message {
	for i := range msgs {
		msgs[i].Raw = nil
	}
	return msgs
}

// brainTestModel is the real model of the chosen provider (--provider,
// default brain.provider) over its key, or the scripted one with --fake.
func brainTestModel(o brainTestOpts, cfg config.Config, log logging.Logger) (brain.Model, error) {
	if o.fake {
		m := braintest.NewModel(
			braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolReadScreen, map[string]any{"agent": o.alias})}},
			braintest.Step{Text: braintest.Reply("The fake model read the screen of "+o.alias+".", "This is the scripted brain-test model; nothing was sent.")},
		)
		return m, nil
	}
	provider := o.provider
	if provider == "" {
		provider = cfg.Brain.Provider
	}
	models, err := app.BrainModels(cfg, log)
	if err != nil {
		return nil, err
	}
	m, ok := models[provider]
	if !ok {
		switch provider {
		case brain.ProviderGemini:
			return nil, errors.New("no Gemini key (set GEMINI_API_KEY or use --fake)")
		case brain.ProviderDeepSeek:
			return nil, errors.New("no DeepSeek key (set DEEPSEEK_API_KEY or use --fake)")
		}
		return nil, fmt.Errorf("unknown provider %q (gemini or deepseek)", provider)
	}
	return m, nil
}

// brainTestSupervisor builds a supervisor over a mock herdr and a fresh
// state table under dir, with b as its brain.
func brainTestSupervisor(dir, alias string, b *brain.Brain, log logging.Logger, stderr io.Writer) (*supervisor.Supervisor, *inject.Herdr, error) {
	store, err := state.Open(filepath.Join(dir, "state.json"), state.Options{Aliases: map[string]string{brainTestPane: alias}, Warn: stderr})
	if err != nil {
		return nil, nil, err
	}
	mock := inject.NewHerdr(nil, log)
	// ReplyWait negative: the command prints the one tool result it gets,
	// so the reply must be that result however long the turn takes.
	sup, err := supervisor.New(store, mock, nil, inject.SkippedPusher{Log: log}, log, supervisor.Config{DataDir: dir, Brain: b, ReplyWait: -1})
	if err != nil {
		return nil, nil, err
	}
	return sup, mock, nil
}
