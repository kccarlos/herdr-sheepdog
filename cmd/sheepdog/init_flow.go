package main

// `sheepdog init` and `sheepdog setup --terminal`: the setup page's
// Models step as terminal questions. The rules (what each choice means
// and what a save writes) are internal/onboard's, the same code the page
// calls; this file only asks the questions.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/service"
)

// initPresets are the orchestrator presets init offers (tests point the
// local ones at a fake server).
var initPresets = onboard.Presets

func printInitUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog init [flags]")
	fprintln(w, "")
	fprintln(w, "Sets sheepdog up in the terminal, with the same choices as the setup page:")
	fprintln(w, "a starting point (Recommended, Claude Opus 5.5, Free quick start or Custom),")
	fprintln(w, "the voice (Gemini Live on AI Studio or Vertex AI, or GPT-Live) with its model")
	fprintln(w, "and voice, and the orchestrator (a preset: Anthropic, Gemini, DeepSeek, Vertex")
	fprintln(w, "AI, OpenRouter, OpenAI, Ollama, LM Studio or any OpenAI-compatible URL) with")
	fprintln(w, "its model. Each key is checked with a live, free call before anything is")
	fprintln(w, "written; keys go to the env file and the choices to the env file and")
	fprintln(w, "config.toml (both 0600). A new config also gets a long random web token; an")
	fprintln(w, "existing token is never changed. Keys are typed without echo and never printed.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH          config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH        secret file (default .env next to the config file)")
	fprintln(w, "  --non-interactive      ask nothing: keys come from the environment (GEMINI_API_KEY,")
	fprintln(w, "                         ANTHROPIC_API_KEY, DEEPSEEK_API_KEY, OPENAI_API_KEY, VERTEX_API_KEY,")
	fprintln(w, "                         OPENROUTER_API_KEY, OPENAI_ORCH_API_KEY, --compat-key-env) or are kept")
	fprintln(w, "                         from the env file; without choices below: the Free quick start")
	fprintln(w, "  --card ID              starting point: recommended | opus | free | custom")
	fprintln(w, "  --voice ID             gemini (AI Studio) | vertex | openai (GPT-Live)")
	fprintln(w, "  --voice-model ID       the voice's Live model (default: the configured one)")
	fprintln(w, "  --voice-name NAME      the voice (default: the configured one)")
	fprintln(w, "  --orchestrator ID      anthropic | gemini | deepseek | vertex | openrouter | openai |")
	fprintln(w, "                         ollama | lmstudio | other (compat is the same as other)")
	fprintln(w, "  --model ID             the orchestrator's model (default: the preset's)")
	fprintln(w, "  --vertex-project ID    Vertex AI: the Google Cloud project (or VERTEX_PROJECT)")
	fprintln(w, "  --vertex-location LOC  Vertex AI: the voice's location (default us-central1)")
	fprintln(w, "  --reuse-openai-key     OpenAI orchestrator: use the voice's OpenAI key")
	fprintln(w, "  --test-tools           also run the tool-calling test (a few tokens; may be billed)")
	fprintln(w, "  --compat-name NAME     other: provider name (default openrouter)")
	fprintln(w, "  --compat-base-url URL  other: API root (default https://openrouter.ai/api/v1)")
	fprintln(w, "  --compat-model ID      other: the model (same as --model)")
	fprintln(w, "  --compat-key-env VAR   other: the key's variable (default <NAME>_API_KEY)")
	fprintln(w, "  --skip-validate        do not call the APIs (offline setup)")
}

// initOptions are init's flags.
type initOptions struct {
	configPath, envPath string
	nonInteractive      bool
	card                string
	voice               string
	voiceModel          string
	voiceName           string
	orchestrator        string
	model               string
	compat              config.OpenAICompat
	vertexProject       string
	vertexLocation      string
	reuseOpenAIKey      bool
	testTools           bool
	skipValidate        bool
}

// initKey is one key init asks for: its variable, what it is for, and
// how it is validated.
type initKey struct {
	env, label string
	optional   bool // a loopback compat server
	check      func(ctx context.Context, key string) onboard.Result
}

// runInit implements `sheepdog init`.
func runInit(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runInitWith(ctx, args, newPrompter(stdin, stdout, false), stdout, stderr)
}

// runInitWith is init with a prompter the caller shares (one reader per
// stdin: a second one would lose what the first buffered).
func runInitWith(ctx context.Context, args []string, in *prompter, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printInitUsage(stderr) }
	var o initOptions
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.envPath, "env-file", "", "secret file")
	fs.BoolVar(&o.nonInteractive, "non-interactive", false, "ask nothing")
	fs.StringVar(&o.card, "card", "", "starting point")
	fs.StringVar(&o.voice, "voice", "", "gemini | vertex | openai")
	fs.StringVar(&o.voiceModel, "voice-model", "", "voice Live model")
	fs.StringVar(&o.voiceName, "voice-name", "", "voice name")
	fs.StringVar(&o.orchestrator, "orchestrator", "", "orchestrator preset")
	fs.StringVar(&o.model, "model", "", "orchestrator model")
	fs.StringVar(&o.vertexProject, "vertex-project", "", "Vertex AI project")
	fs.StringVar(&o.vertexLocation, "vertex-location", "", "Vertex AI location")
	fs.BoolVar(&o.reuseOpenAIKey, "reuse-openai-key", false, "reuse the voice's OpenAI key")
	fs.BoolVar(&o.testTools, "test-tools", false, "run the tool-calling test")
	fs.StringVar(&o.compat.Name, "compat-name", "", "compat provider name")
	fs.StringVar(&o.compat.BaseURL, "compat-base-url", "", "compat API root")
	fs.StringVar(&o.compat.Model, "compat-model", "", "compat model")
	fs.StringVar(&o.compat.APIKeyEnv, "compat-key-env", "", "compat key variable")
	fs.BoolVar(&o.skipValidate, "skip-validate", false, "do not call the APIs")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if o.configPath == "" {
		o.configPath = config.DefaultPath()
	}
	if o.envPath == "" {
		o.envPath = filepath.Join(filepath.Dir(o.configPath), ".env")
	}
	if o.model == "" {
		o.model = o.compat.Model
	}
	in.nonInteractive = o.nonInteractive
	if err := initRun(ctx, o, in, stdout); err != nil {
		fprintf(stderr, "sheepdog init: %v\n", err)
		return 1
	}
	if !o.nonInteractive {
		offerService(ctx, o, in, stdout, stderr)
	}
	return 0
}

// offerService is init's last step: it offers `sheepdog service install`
// unless the service is installed already. Declining or failing is not
// a failure of init; the keys and the config are written by then.
func offerService(ctx context.Context, o initOptions, in *prompter, stdout, stderr io.Writer) {
	var extra []string
	if o.configPath != config.DefaultPath() {
		extra = append(extra, "--config", o.configPath)
	}
	if o.envPath != config.DefaultEnvPath() {
		extra = append(extra, "--env-file", o.envPath)
	}
	spec, err := serviceSpec(o.configPath, o.envPath, "")
	if err != nil {
		fprintf(stderr, "sheepdog init: not offering the login service: %v\n", err)
		return
	}
	m := service.Manager{Spec: spec, Run: serviceRunner, Out: stdout}
	if st, _ := m.Status(ctx); !strings.HasPrefix(st, "not installed") {
		fprintln(stdout, "sheepdog init: the login service is already installed ("+st+")")
		return
	}
	fprintln(stdout, "")
	fprintln(stdout, "Start at login (optional): this installs a background service (a launchd agent on macOS,")
	fprintln(stdout, "a systemd user unit on Linux) that runs sheepdog whenever you log in and restarts it if it")
	fprintln(stdout, "crashes. It does not need admin rights. Undo it any time with: sheepdog service uninstall")
	yes, err := in.yesNo("Install it now?", false)
	if err != nil || !yes {
		fprintln(stdout, "sheepdog init: skipped. sheepdog starts only when you run it; to start at login later: sheepdog service install")
		return
	}
	if code := runService(ctx, append([]string{"install"}, extra...), stdout, stderr); code != 0 {
		fprintln(stderr, "sheepdog init: the service did not install; run sheepdog service install after fixing the above")
	}
}

// initFlow is one run of the questions.
type initFlow struct {
	ctx      context.Context
	o        initOptions
	in       *prompter
	out      io.Writer
	ep       onboard.Endpoints
	presets  []onboard.Preset
	existing map[string]string // the env file as read
	cur      config.Config     // what the config says today
	keys     map[string]string // env variable -> a key to write
	have     map[string]string // env variable -> every key known this run (typed or saved)
	// ask is the model input being built.
	input onboard.ModelsInput
	// orchModel is the orchestrator model for the summary.
	preset onboard.Preset
}

// initRun is init's flow; it returns before writing anything when a key
// does not validate.
func initRun(ctx context.Context, o initOptions, in *prompter, out io.Writer) error {
	_, statErr := os.Stat(o.configPath)
	newConfig := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !newConfig {
		return statErr
	}
	existing, err := onboard.ReadEnvFile(o.envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", o.envPath, err)
	}
	// What the config says today (the herdr socket, an existing token).
	cur := config.Default()
	if !newConfig {
		if c, err := config.Load(o.configPath); err == nil {
			cur = c
		}
	}
	f := &initFlow{ctx: ctx, o: o, in: in, out: out, ep: onboardEndpoints(), presets: initPresets(), existing: existing, cur: cur,
		keys: map[string]string{}, have: map[string]string{}}
	f.ep = onboard.AnthropicEndpoints(f.ep, cur)

	fprintln(out, "sheepdog init: "+onboard.CheckHerdr(cur.Herdr).Line())

	card, err := f.startingPoint()
	if err != nil {
		return err
	}
	voice, err := f.voiceStep(card)
	if err != nil {
		return err
	}
	if _, err := f.orchestratorStep(card, voice); err != nil {
		return err
	}

	f.input.Voice = voice
	ch, msg := onboard.ResolveModels(cur, f.presets, f.input)
	if msg != "" {
		return errors.New(msg)
	}
	if ch.Compat != nil && !ch.IsPreset && o.compat.APIKeyEnv != "" {
		ch.Compat.APIKeyEnv = o.compat.APIKeyEnv
	}
	fprintln(out, "")
	fprintln(out, "Summary: "+f.summary(ch))

	set := map[string]string{}
	for k, v := range f.keys {
		if v != existing[k] {
			set[k] = v
		}
	}

	// The same save the setup page does.
	names, err := onboard.SaveModels(o.configPath, o.envPath, existing, ch, set)
	if err != nil {
		return err
	}
	if _, err := os.Stat(o.configPath); errors.Is(err, os.ErrNotExist) {
		if err := onboard.WriteConfig(o.configPath, brain.ProviderGemini, nil); err != nil {
			return err
		}
	}
	if _, err := config.Load(o.configPath); err != nil {
		return fmt.Errorf("the config %s does not load: %w", o.configPath, err)
	}
	switch {
	case newConfig:
		fprintf(out, "sheepdog init: wrote %s (0600)\n", o.configPath)
	case ch.Compat != nil || ch.VertexProject != "":
		fprintf(out, "sheepdog init: updated %s\n", o.configPath)
	default:
		fprintf(out, "sheepdog init: kept %s as it is\n", o.configPath)
	}
	fprintf(out, "sheepdog init: wrote %s (0600): %s\n", o.envPath, strings.Join(names, ", "))
	fprintf(out, "sheepdog init: next: sheepdog doctor, then sheepdog start, and open http://127.0.0.1:%d/voice (sheepdog open)\n", cur.Web.Port)
	return nil
}

// savedVoice is the voice card the saved setup uses.
func (f *initFlow) savedVoice() string {
	provider := onboard.OrDefault(f.existing["SHEEPDOG_VOICE_PROVIDER"], onboard.OrDefault(f.cur.Voice.Provider, onboard.VoiceGemini))
	backend := onboard.OrDefault(f.existing["SHEEPDOG_GEMINI_BACKEND"], f.cur.Gemini.Backend)
	switch {
	case provider == onboard.VoiceOpenAI:
		return onboard.VoiceOpenAI
	case backend == onboard.VoiceVertex:
		return onboard.VoiceVertex
	}
	return onboard.VoiceGemini
}

// savedOrch is the orchestrator preset the saved setup uses.
func (f *initFlow) savedOrch() string {
	id, _ := onboard.PresetOf(f.presets, f.cur, onboard.OrDefault(f.existing["SHEEPDOG_BRAIN_PROVIDER"], f.cur.Brain.Provider))
	return id
}

func (f *initFlow) hasPreset(id string) bool {
	_, ok := onboard.PresetByID(f.presets, id)
	return ok
}

// startingPoint asks for the card (step 1). With --card it is that card;
// with --voice or --orchestrator it is Custom; non-interactive with no
// choice at all it is the Free quick start (what init always did);
// otherwise the default is the setup page's: Recommended on a fresh
// install, else the card that matches the saved setup.
func (f *initFlow) startingPoint() (onboard.StartCard, error) {
	cards := onboard.StartCards()
	if f.o.card != "" {
		c, ok := onboard.CardByID(f.o.card)
		if !ok {
			return c, fmt.Errorf("--card %q: want recommended, opus, free or custom", f.o.card)
		}
		if c.Orch != "" && !f.hasPreset(c.Orch) {
			return c, fmt.Errorf("--card %s needs the %s preset", c.ID, c.Orch)
		}
		return f.announce(c), nil
	}
	if f.o.voice != "" || f.o.orchestrator != "" {
		c, _ := onboard.CardByID(onboard.CardCustom)
		return c, nil
	}
	if f.in.nonInteractive {
		c, _ := onboard.CardByID(onboard.CardFree)
		return f.announce(c), nil
	}
	init := onboard.InitialChoice(onboard.VoiceSetupNeeded(f.cur, f.existing), f.hasPreset(brain.ProviderDeepSeek), f.savedVoice(), f.savedOrch())
	ids, labels := make([]string, len(cards)), make([]string, len(cards))
	for i, c := range cards {
		ids[i], labels[i] = c.ID, c.Label+": "+c.Note
		if c.Need != "" {
			labels[i] += " " + c.Need
		}
	}
	id, err := f.in.chooseDef("Step 1 of 3. Starting point", ids, labels, init.Card)
	if err != nil {
		return onboard.StartCard{}, err
	}
	c, _ := onboard.CardByID(id)
	return f.announce(c), nil
}

// announce says what a card picks (Custom picks nothing).
func (f *initFlow) announce(c onboard.StartCard) onboard.StartCard {
	if c.Voice != "" {
		p, _ := onboard.PresetByID(f.presets, c.Orch)
		fprintf(f.out, "Starting point: %s (voice: %s, orchestrator: %s)\n", c.Label, voiceLabel(c.Voice), p.Label)
	}
	return c
}

// summary is the page's summary line for the resolved choice.
func (f *initFlow) summary(ch onboard.ModelsChoice) string {
	gp, op := onboard.VoicePicks(f.cur)
	vm := gp.Model
	if ch.Voice == onboard.VoiceOpenAI {
		vm = op.Model
	}
	if ch.VoiceSel.Model != "" {
		vm = ch.VoiceSel.Model
	}
	om := ch.Model
	if om == "" {
		om = f.preset.DefaultModel
	}
	p := f.preset
	return onboard.Summary(onboard.SummaryInput{Voice: ch.Voice, VoiceModel: vm, Preset: &p, OrchModel: om, OtherName: f.input.Compat.Name})
}
