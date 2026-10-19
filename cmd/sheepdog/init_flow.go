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
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/service"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
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

	fprintln(out, "sheepdog init: "+onboard.CheckHerdr(cur.Herdr.Socket).Line())

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

	// The web token: only for a new config, and never over one that
	// exists anywhere (the phone is paired with it).
	set := map[string]string{}
	for k, v := range f.keys {
		if v != existing[k] {
			set[k] = v
		}
	}
	tokenNote := ""
	if newConfig && !onboard.HasToken(existing, cur) {
		tok, err := onboard.NewToken()
		if err != nil {
			return err
		}
		set["SHEEPDOG_WEB_TOKEN"] = tok
		tokenNote = "generated a web token (SHEEPDOG_WEB_TOKEN in " + o.envPath + "; the phone app asks for it)"
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
	if tokenNote != "" {
		fprintln(out, "sheepdog init: "+tokenNote)
	}
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

func voiceLabel(v string) string {
	switch v {
	case onboard.VoiceVertex:
		return "Gemini Live on Vertex AI"
	case onboard.VoiceOpenAI:
		return "GPT-Live"
	}
	return "Gemini Live on AI Studio"
}

// voiceStep is step 2: the voice, its key (or Vertex credentials) and the
// optional model and voice. It returns the voice id.
func (f *initFlow) voiceStep(card onboard.StartCard) (string, error) {
	voice := card.Voice
	if voice == "" {
		ids := []string{onboard.VoiceGemini, onboard.VoiceVertex, onboard.VoiceOpenAI}
		labels := []string{
			"Gemini Live on AI Studio: Google AI Studio, one free key",
			"Gemini Live on Vertex AI: Google Cloud, billed to your project",
			"GPT-Live: OpenAI, " + onboard.GPTLivePrice,
		}
		var err error
		voice, err = f.in.chooseDef("Step 2 of 3. Voice", ids, labels, f.savedVoice(), f.o.voice)
		if err != nil {
			return "", err
		}
	}
	gp, op := onboard.VoicePicks(f.cur)
	pick := gp
	var key string
	var err error
	var models []string
	switch voice {
	case onboard.VoiceGemini:
		if key, err = f.key(f.geminiKey()); err != nil {
			return "", err
		}
		if !f.o.skipValidate && !f.in.nonInteractive {
			ids, r := onboard.ListLiveModels(f.ctx, f.ep, key)
			models = f.listNote(ids, r, "Live models")
		}
	case onboard.VoiceOpenAI:
		pick = op
		if key, err = f.key(initKey{env: "OPENAI_API_KEY", label: "OpenAI API key for GPT-Live (https://platform.openai.com/api-keys)",
			check: func(ctx context.Context, k string) onboard.Result {
				return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "openai", Key: k}))
			}}); err != nil {
			return "", err
		}
		if !f.o.skipValidate && !f.in.nonInteractive {
			ids, r := onboard.ListGPTLiveModels(f.ctx, f.ep, key)
			models = f.listNote(ids, r, "GPT-Live models")
		}
	case onboard.VoiceVertex:
		if err := f.vertex(true); err != nil {
			return "", err
		}
		models = gemini.VertexModels
		if f.cur.Gemini.Backend == onboard.VoiceVertex {
			models = onboard.WithFirst(gemini.VertexModels, f.cur.Gemini.Model)
		}
	default:
		return "", fmt.Errorf("--voice %q: want gemini, vertex or openai", voice)
	}
	f.input.Voice = voice
	model, name := f.o.voiceModel, f.o.voiceName
	if !f.in.nonInteractive {
		if model == "" {
			if model, err = f.in.pickName("Live model", onboard.WithFirst(models, pick.Model), pick.Model, nil); err != nil {
				return "", err
			}
		}
		if name == "" {
			fprintln(f.out, "Voices: "+strings.Join(onboard.VoiceNames(voice, pick.Voice), ", "))
			if name, err = f.in.ask("Voice (Enter keeps the default)", "", pick.Voice); err != nil {
				return "", err
			}
		}
	}
	// Only a choice that differs from the configured one is saved, so the
	// defaults are not pinned in the env file.
	if model != pick.Model {
		f.input.VoiceModel = model
	}
	if name != "" && !strings.EqualFold(name, pick.Voice) {
		f.input.VoiceName = name
	}
	if voice == onboard.VoiceVertex && !f.o.skipValidate && f.input.VoiceModel != "" {
		// A new Live model must be served at the location.
		c := onboard.ValidateKey(f.ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "vertex", Key: f.have["VERTEX_API_KEY"], Project: f.input.Vertex.Project,
			Location: onboard.OrDefault(f.input.Vertex.Location, onboard.DefaultVertexLocation), Voice: true, LiveModel: f.input.VoiceModel})
		if c.Status == "fail" {
			return "", fmt.Errorf("the voice model %s: %s — fix: %s", f.input.VoiceModel, c.Detail, c.Fix)
		}
		fprintln(f.out, "  "+resultOf(c).Line())
	}
	return voice, nil
}

// listNote prints a failed list as a note (the default is still
// offered) and returns the ids.
func (f *initFlow) listNote(ids []string, r onboard.Result, what string) []string {
	if r.Status != onboard.OK {
		fprintf(f.out, "  could not list the %s (%s), so only the default is offered\n", what, r.Detail)
		return nil
	}
	return ids
}

func (f *initFlow) geminiKey() initKey {
	return initKey{env: "GEMINI_API_KEY", label: "Gemini API key (" + onboard.GeminiKeyURL + ")",
		check: func(ctx context.Context, k string) onboard.Result {
			return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "gemini", Key: k}))
		}}
}

// key asks for k once per run (a key two choices share is asked once).
func (f *initFlow) key(k initKey) (string, error) {
	if v, ok := f.have[k.env]; ok {
		return v, nil
	}
	v, err := initAskKey(f.ctx, k, f.existing, f.in, f.o.skipValidate, f.out)
	if err != nil {
		return "", err
	}
	f.have[k.env] = v
	if v != "" {
		f.keys[k.env] = v
	}
	return v, nil
}

// vertex asks for the Google Cloud project, the location (for the
// voice) and the key, and checks them live.
func (f *initFlow) vertex(forVoice bool) error {
	if f.input.Vertex.Project != "" {
		return nil
	}
	saved := f.cur.Gemini.Vertex
	defProject := onboard.OrDefault(os.Getenv("VERTEX_PROJECT"), saved.Project)
	project, err := f.in.ask("Google Cloud project id for Vertex AI", f.o.vertexProject, defProject)
	if err != nil {
		return err
	}
	f.input.Vertex.Project = project
	if forVoice {
		loc, err := f.in.ask("Location for the voice", f.o.vertexLocation, onboard.OrDefault(saved.Location, onboard.DefaultVertexLocation))
		if err != nil {
			return err
		}
		f.input.Vertex.Location = loc
	}
	// A saved key is kept only for the project it was saved for.
	existing := f.existing
	if saved.Project != project {
		existing = map[string]string{}
	}
	k := initKey{env: "VERTEX_API_KEY", label: "Vertex AI API key (" + onboard.VertexKeyDocURL + ")",
		check: func(ctx context.Context, key string) onboard.Result {
			req := onboard.ValidateRequest{Kind: "vertex", Key: key, Project: project}
			if forVoice {
				req.Location, req.Voice = f.input.Vertex.Location, true
			}
			return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, req))
		}}
	v, err := initAskKey(f.ctx, k, existing, f.in, f.o.skipValidate, f.out)
	if err != nil {
		return err
	}
	f.have[k.env] = v
	f.keys[k.env] = v
	return nil
}

// orchestratorStep is step 3: the preset, its key, and its model. It
// returns the preset id as ResolveModels takes it.
func (f *initFlow) orchestratorStep(card onboard.StartCard, voice string) (string, error) {
	id := card.Orch
	if id == "" {
		flag := f.o.orchestrator
		if flag == "compat" {
			flag = onboard.PresetOther
		}
		ids := make([]string, len(f.presets))
		labels := make([]string, len(f.presets))
		for i, p := range f.presets {
			ids[i], labels[i] = p.ID, p.Label+": "+p.Note
		}
		def := f.savedOrch()
		if onboard.VoiceSetupNeeded(f.cur, f.existing) && f.hasPreset(brain.ProviderDeepSeek) {
			def = brain.ProviderDeepSeek
		}
		if f.in.nonInteractive {
			def = brain.ProviderGemini
		}
		var err error
		if id, err = f.in.chooseDef("Step 3 of 3. Orchestrator (the model that reads and acts on your agents)", ids, labels, def, flag); err != nil {
			return "", err
		}
	}
	p, ok := onboard.PresetByID(f.presets, id)
	if !ok {
		return "", fmt.Errorf("unknown orchestrator %q", id)
	}
	f.preset = p
	f.input.Orchestrator = id
	if id == onboard.PresetOther {
		f.input.Orchestrator = "compat"
	}
	var err error
	switch p.ID {
	case brain.ProviderGemini:
		err = f.geminiOrch()
	case brain.ProviderDeepSeek:
		_, err = f.key(initKey{env: "DEEPSEEK_API_KEY", label: "DeepSeek API key (" + p.DocsURL + ")",
			check: func(ctx context.Context, k string) onboard.Result {
				return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "deepseek", Key: k}))
			}})
	case brain.ProviderAnthropic:
		err = f.anthropicOrch(p)
	case brain.ProviderVertex:
		err = f.vertexOrch(voice)
	default:
		err = f.compatOrch(p)
	}
	return id, err
}

func (f *initFlow) geminiOrch() error {
	key, err := f.key(f.geminiKey())
	if err != nil {
		return err
	}
	model := f.o.model
	if !f.in.nonInteractive && model == "" {
		var ids []string
		if !f.o.skipValidate {
			var r onboard.Result
			ids, r = onboard.ListGeminiModels(f.ctx, f.ep, key)
			ids = f.listNote(ids, r, "models")
		}
		if model, err = f.in.pickName("Orchestrator model", onboard.WithFirst(ids, brain.DefaultModel), brain.DefaultModel, nil); err != nil {
			return err
		}
	}
	if model != brain.DefaultModel {
		f.input.GeminiModel = model
	}
	return nil
}

func (f *initFlow) vertexOrch(voice string) error {
	if err := f.vertex(voice == onboard.VoiceVertex); err != nil {
		return err
	}
	var names []string
	for _, m := range brain.VertexModels {
		names = append(names, m.Name)
	}
	model := f.o.model
	if !f.in.nonInteractive && model == "" {
		var err error
		if model, err = f.in.pickName("Orchestrator model", onboard.WithFirst(names, brain.DefaultModel), brain.DefaultModel, nil); err != nil {
			return err
		}
	}
	f.input.Vertex.Model = model
	return nil
}

func (f *initFlow) anthropicOrch(p onboard.Preset) error {
	key, err := f.key(initKey{env: p.KeyEnv, label: "Anthropic API key (" + p.DocsURL + ")",
		check: func(ctx context.Context, k string) onboard.Result {
			return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "anthropic", Key: k}))
		}})
	if err != nil {
		return err
	}
	model := f.o.model
	if !f.in.nonInteractive && model == "" {
		var entries []onboard.ModelEntry
		if !f.o.skipValidate {
			var r onboard.Result
			entries, r = onboard.ListAnthropicModels(f.ctx, f.ep, key)
			if r.Status != onboard.OK {
				fprintf(f.out, "  could not list the models (%s), so only the default is offered\n", r.Detail)
				entries = nil
			}
		}
		if model, err = f.pickOrchModel(entries, brain.DefaultAnthropicModel, false); err != nil {
			return err
		}
	}
	if model != brain.DefaultAnthropicModel {
		f.input.AnthropicModel = model
	}
	shown := onboard.OrDefault(model, brain.DefaultAnthropicModel)
	if f.wantToolTest() && !f.o.skipValidate {
		r := onboard.ProbeAnthropic(f.ctx, f.ep, key, shown)
		fprintln(f.out, "  "+r.Line())
	}
	return nil
}

// wantToolTest: "test tool calling? [y/N]" (a few tokens, which a paid
// endpoint may bill); non-interactive runs it with --test-tools.
func (f *initFlow) wantToolTest() bool {
	if f.in.nonInteractive {
		return f.o.testTools
	}
	if f.o.skipValidate {
		return false
	}
	yes, err := f.in.yesNo("Test tool calling on this model now? (a few tokens; a paid endpoint may bill them)", false)
	return err == nil && yes
}

// compatOrch handles OpenRouter, OpenAI, Ollama, LM Studio and Other URL.
func (f *initFlow) compatOrch(p onboard.Preset) error {
	name, base := p.ID, p.BaseURL
	if p.Kind == onboard.PresetOther {
		var err error
		if name, err = f.in.ask("Provider name", f.o.compat.Name, "openrouter"); err != nil {
			return err
		}
		if base, err = f.in.ask("Base URL (the API root; /chat/completions is appended)", f.o.compat.BaseURL, "https://openrouter.ai/api/v1"); err != nil {
			return err
		}
		name = onboard.CompatName(name)
	}
	f.input.Compat.Name, f.input.Compat.BaseURL = name, base

	// The key: a keyed preset always needs one; a typed loopback URL may
	// run without.
	keyed := p.KeyNeeded || (p.Kind == onboard.PresetOther && !onboard.KeyOptionalURL(base))
	reuse := false
	var key string
	if keyed || p.Kind == onboard.PresetOther {
		reuse = p.ReuseEnv != "" && (f.o.reuseOpenAIKey || f.askReuse(p))
		f.input.Compat.ReuseKey = reuse
		env := onboard.CompatKeyEnv(f.cur, name, reuse)
		if p.Kind == onboard.PresetOther && f.o.compat.APIKeyEnv != "" {
			env = f.o.compat.APIKeyEnv
		}
		if reuse {
			key = f.have[p.ReuseEnv]
			if key == "" {
				key = f.existing[p.ReuseEnv]
			}
			if key == "" {
				return errors.New("no OpenAI voice key is saved to reuse: use an OpenAI voice, or give this orchestrator a key of its own")
			}
		} else {
			var err error
			label := name + " API key"
			if p.DocsURL != "" {
				label += " (" + p.DocsURL + ")"
			}
			probe := config.OpenAICompat{Name: name, BaseURL: base}
			if key, err = f.key(initKey{env: env, label: label, optional: !keyed, check: func(ctx context.Context, k string) onboard.Result {
				return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "compat", Key: k, Name: probe.Name, BaseURL: probe.BaseURL, Preset: pp(p)}))
			}}); err != nil {
				return err
			}
		}
	}

	model := f.o.model
	if model == "" || (!f.in.nonInteractive && p.Local) {
		list := f.findModels(p, name, base, key)
		if model == "" {
			var err error
			if f.in.nonInteractive {
				if list.Suggested == "" {
					return fmt.Errorf("--model: required for %s (it must support tool calling)", p.Label)
				}
				model = list.Suggested
			} else if model, err = f.pickCompatModel(list); err != nil {
				return err
			}
		}
	}
	f.input.Compat.Model = model
	if f.wantToolTest() && !f.o.skipValidate {
		r := onboard.ProbeTools(f.ctx, f.ep, onboard.Compat{Name: name, BaseURL: base, APIKey: key, KeyEnv: onboard.CompatKeyEnv(f.cur, name, reuse), NoKey: onboard.KeyOptionalURL(base), Model: model})
		fprintln(f.out, "  "+r.Line())
	}
	return nil
}

// pp is a preset id for a compat key check ("" for Other URL).
func pp(p onboard.Preset) string {
	if p.Kind == onboard.PresetCompat {
		return p.ID
	}
	return ""
}

// askReuse offers the voice's OpenAI key to the OpenAI orchestrator, only
// on an explicit yes.
func (f *initFlow) askReuse(p onboard.Preset) bool {
	if f.in.nonInteractive || (f.have[p.ReuseEnv] == "" && f.existing[p.ReuseEnv] == "") {
		return false
	}
	yes, err := f.in.yesNo("Use the same OpenAI key as the voice for the orchestrator?", false)
	return err == nil && yes
}

// findModels is the page's "Find models": the models the endpoint lists,
// tool-capable first. A local server that is not running is waited for
// (interactive) or reported.
func (f *initFlow) findModels(p onboard.Preset, name, base, key string) onboard.ModelsResponse {
	if f.o.skipValidate {
		return onboard.ModelsResponse{}
	}
	req := onboard.ModelsRequest{Name: name, BaseURL: base, Preset: pp(p)}
	for attempt := 0; ; attempt++ {
		resp := onboard.FindModels(f.ctx, f.ep.HTTP, f.presets, req, key)
		if resp.Error == "" {
			if p.Local {
				fprintf(f.out, "  %s is running (%d models)\n", p.Label, len(resp.Models))
			}
			sort.SliceStable(resp.Models, func(i, j int) bool { return isTool(resp.Models[i]) && !isTool(resp.Models[j]) })
			return resp
		}
		fprintf(f.out, "  %s %s\n", resp.Error, resp.Fix)
		if !p.Local || f.in.nonInteractive || attempt >= 2 {
			return onboard.ModelsResponse{}
		}
		// The page's "press Check again".
		if v, err := f.in.ask("Press Enter to check again, or type s to type the model name yourself", "", "again"); err != nil || strings.EqualFold(v, "s") {
			return onboard.ModelsResponse{}
		}
	}
}

func isTool(m onboard.ModelInfo) bool { return m.Tools != nil && *m.Tools }

// pickCompatModel offers the listed models (tool-capable first) and the
// suggested one on Enter; with no list the name is typed.
func (f *initFlow) pickCompatModel(list onboard.ModelsResponse) (string, error) {
	entries := make([]onboard.ModelEntry, len(list.Models))
	for i, m := range list.Models {
		entries[i] = onboard.ModelEntry(m)
	}
	return f.pickOrchModel(entries, list.Suggested, true)
}

// pickOrchModel is the numbered model list: tool-capable first, marked
// when the endpoint says. def is what Enter takes ("" : the name must be
// typed, as for OpenRouter's long list).
func (f *initFlow) pickOrchModel(entries []onboard.ModelEntry, def string, required bool) (string, error) {
	names := make([]string, len(entries))
	notes := map[string]string{}
	tools := false
	for i, e := range entries {
		names[i] = e.ID
		if e.Tools != nil {
			tools = true
			if *e.Tools {
				notes[e.ID] = "tool calling"
			} else {
				notes[e.ID] = "no tool calling"
			}
		}
	}
	if tools {
		sort.SliceStable(names, func(i, j int) bool { return notes[names[i]] == "tool calling" && notes[names[j]] != "tool calling" })
	}
	if def == "" && required && tools {
		for _, n := range names {
			if notes[n] == "tool calling" {
				def = n
				break
			}
		}
	}
	if !required || len(names) > 0 {
		names = onboard.WithFirst(names, def)
	}
	return f.in.pickName("Orchestrator model", names, def, notes)
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

// initAskKey gets one key: the existing one (kept on request or in
// non-interactive mode), the environment's (non-interactive), or typed
// in without echo; validated live unless skip.
func initAskKey(ctx context.Context, k initKey, existing map[string]string, in *prompter, skip bool, out io.Writer) (string, error) {
	have := existing[k.env]
	if in.nonInteractive {
		v := strings.TrimSpace(os.Getenv(k.env))
		if v == "" {
			v = have
		}
		if v == "" {
			if k.optional {
				return "", nil
			}
			return "", fmt.Errorf("%s: not set in the environment or %s", k.env, "the env file")
		}
		if !skip {
			if r := k.check(ctx, v); r.Status == onboard.Fail {
				return "", fmt.Errorf("%s: %s — fix: %s", k.env, r.Detail, r.Fix)
			}
		}
		return v, nil
	}
	if have != "" {
		keep, err := in.yesNo(fmt.Sprintf("%s is already in the env file. Keep it?", k.env), true)
		if err != nil {
			return "", err
		}
		if keep {
			return have, nil
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		label := k.label
		if k.optional {
			label += " (optional for a local server; Enter to skip)"
		}
		v, err := in.secret(label)
		if err != nil {
			return "", err
		}
		if v == "" {
			if k.optional {
				return "", nil
			}
			fprintln(out, "  a key is needed")
			continue
		}
		if skip {
			return v, nil
		}
		r := k.check(ctx, v)
		if r.Status != onboard.Fail {
			fprintln(out, "  "+r.Line())
			return v, nil
		}
		fprintln(out, "  "+r.Line())
	}
	return "", fmt.Errorf("%s: no valid key after 3 tries; nothing written", k.env)
}

// resultOf is a check as a doctor-style result.
func resultOf(c onboard.Check) onboard.Result {
	r := onboard.Result{Name: c.Name, Detail: c.Detail, Fix: c.Fix}
	switch c.Status {
	case "ok":
		r.Status = onboard.OK
	case "warn":
		r.Status = onboard.Warn
	case "fail":
		r.Status = onboard.Fail
	default:
		r.Status = onboard.Skip
	}
	return r
}
