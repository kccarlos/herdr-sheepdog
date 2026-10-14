package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"


	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// onboardEndpoints are the API roots init and doctor validate keys
// against (tests point them at an httptest server).
var onboardEndpoints = onboard.DefaultEndpoints

func printDoctorUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog doctor [flags]")
	fprintln(w, "")
	fprintln(w, "Checks the setup and prints one line per check with the fix: herdr,")
	fprintln(w, "every configured key (live and cheap; the balance where the API shows it),")
	fprintln(w, "a tool-calling probe on OpenAI-compatible orchestrators (a few tokens),")
	fprintln(w, "the orchestrator and voice providers, the web port, the bind/token")
	fprintln(w, "pairing, push and the secret files' permissions. Exit 1 on any FAIL.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH   env file (default ./.env, else ~/.config/sheepdog/.env, as serve)")
	fprintln(w, "  --no-probe        skip the tool-calling probe (no tokens spent)")
}

// runDoctor implements `sheepdog doctor`.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printDoctorUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	envFile := fs.String("env-file", "", "env file")
	noProbe := fs.Bool("no-probe", false, "skip the tool-calling probe")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	envPath := *envFile
	if envPath == "" {
		envPath = implicitEnvFile()
	}
	if code, ok := loadServeEnvFile(*envFile, stderr); !ok {
		return code
	}
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fprintf(stdout, "%s\n", onboard.Result{Name: "config", Status: onboard.Fail, Detail: err.Error(), Fix: "fix the file, or move it away and run sheepdog init"}.Line())
		return 1
	}
	src := cfgPath
	if _, err := os.Stat(cfgPath); err != nil {
		src = "defaults (no " + cfgPath + ")"
	}
	fprintln(stdout, onboard.Result{Name: "config", Status: onboard.OK, Detail: "loaded " + src}.Line())
	results := onboard.Doctor(ctx, cfg, onboard.DoctorOptions{Endpoints: onboardEndpoints(), Probe: !*noProbe, ConfigPath: cfgPath, EnvPath: envPath})
	for _, r := range results {
		fprintln(stdout, r.Line())
	}
	if onboard.Failed(results) {
		return 1
	}
	return 0
}

func printInitUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog init [flags]")
	fprintln(w, "")
	fprintln(w, "Sets sheepdog up: checks herdr, asks for one key (Gemini AI Studio by")
	fprintln(w, "default; or DeepSeek / any OpenAI-compatible orchestrator plus a Gemini key")
	fprintln(w, "for the voice), validates each key with a live, free call, and writes the")
	fprintln(w, "keys to the env file and, if there is none yet, config.toml (both 0600).")
	fprintln(w, "A new config also gets a long random web token; an existing token is")
	fprintln(w, "never changed. Keys are never printed.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH          config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH        secret file (default .env next to the config file)")
	fprintln(w, "  --non-interactive      ask nothing: keys come from the environment")
	fprintln(w, "                         (GEMINI_API_KEY, DEEPSEEK_API_KEY, the --compat-key-env")
	fprintln(w, "                         variable) or are kept from the env file")
	fprintln(w, "  --orchestrator KIND    gemini (default) | deepseek | compat")
	fprintln(w, "  --compat-name NAME     compat: provider name (default openrouter)")
	fprintln(w, "  --compat-base-url URL  compat: API root (default https://openrouter.ai/api/v1)")
	fprintln(w, "  --compat-model ID      compat: the model (must support tool calling)")
	fprintln(w, "  --compat-key-env VAR   compat: the key's variable (default <NAME>_API_KEY)")
	fprintln(w, "  --skip-validate        do not call the APIs (offline setup)")
}

// initOptions are init's flags.
type initOptions struct {
	configPath, envPath string
	nonInteractive      bool
	orchestrator        string
	compat              config.OpenAICompat
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
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printInitUsage(stderr) }
	var o initOptions
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.envPath, "env-file", "", "secret file")
	fs.BoolVar(&o.nonInteractive, "non-interactive", false, "ask nothing")
	fs.StringVar(&o.orchestrator, "orchestrator", "", "gemini | deepseek | compat")
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
	in := newPrompter(stdin, stdout, o.nonInteractive)
	if err := initRun(ctx, o, in, stdout); err != nil {
		fprintf(stderr, "sheepdog init: %v\n", err)
		return 1
	}
	return 0
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

	fprintln(out, "sheepdog init: "+onboard.CheckHerdr(cur.Herdr.Socket).Line())

	orch, err := in.choose(o.orchestrator, "Orchestrator (the model that reads and acts on your agents)",
		[]string{"gemini", "deepseek", "compat"},
		[]string{"Gemini via AI Studio — one free key for the voice and the orchestrator (default)", "DeepSeek — paid per token, plus a Gemini key for the voice", "another OpenAI-compatible endpoint (OpenRouter, Ollama, Groq, …), plus a Gemini key for the voice"})
	if err != nil {
		return err
	}
	ep := onboardEndpoints()
	keys := []initKey{{env: "GEMINI_API_KEY", label: "Gemini API key (https://aistudio.google.com/apikey)", check: func(ctx context.Context, k string) onboard.Result { return onboard.CheckGeminiKey(ctx, ep, k) }}}
	var compat *config.OpenAICompat
	switch orch {
	case "deepseek":
		off := false
		keys = append(keys, initKey{env: "DEEPSEEK_API_KEY", label: "DeepSeek API key (https://platform.deepseek.com)", check: func(ctx context.Context, k string) onboard.Result {
			return onboard.CheckCompat(ctx, ep, onboard.Compat{Name: brain.ProviderDeepSeek, BaseURL: config.DefaultDeepSeekBaseURL, APIKey: k, KeyEnv: "DEEPSEEK_API_KEY", Thinking: &off}, false)[0]
		}})
	case "compat":
		c := o.compat
		if c.Name, err = in.ask("Provider name", c.Name, "openrouter"); err != nil {
			return err
		}
		if c.BaseURL, err = in.ask("Base URL (the API root; /chat/completions is appended)", c.BaseURL, "https://openrouter.ai/api/v1"); err != nil {
			return err
		}
		if c.Model, err = in.ask("Model (must support tool calling)", c.Model, ""); err != nil {
			return err
		}
		if c.APIKeyEnv == "" {
			c.APIKeyEnv = config.DefaultCompatKeyEnv(strings.ToLower(c.Name))
		}
		c.Name = strings.ToLower(strings.TrimSpace(c.Name))
		probe := c
		keys = append(keys, initKey{env: c.APIKeyEnv, label: c.Name + " API key", optional: c.KeyOptional(), check: func(ctx context.Context, k string) onboard.Result {
			return onboard.CheckCompat(ctx, ep, onboard.Compat{Name: probe.Name, BaseURL: probe.BaseURL, APIKey: k, KeyEnv: probe.APIKeyEnv, NoKey: probe.KeyOptional()}, false)[0]
		}})
		compat = &c
	}

	set := map[string]string{}
	for _, k := range keys {
		v, err := initAskKey(ctx, k, existing, in, o.skipValidate, out)
		if err != nil {
			return err
		}
		if v != "" && v != existing[k.env] {
			set[k.env] = v
		}
	}

	// The web token: only for a new config, and never over one that
	// exists anywhere (the phone is paired with it).
	tokenNote := ""
	envHasToken := strings.TrimSpace(os.Getenv("SHEEPDOG_WEB_TOKEN")) != ""
	if newConfig && !envHasToken && existing["SHEEPDOG_WEB_TOKEN"] == "" && cur.Web.Token == "" {
		tok, err := onboard.NewToken()
		if err != nil {
			return err
		}
		set["SHEEPDOG_WEB_TOKEN"] = tok
		tokenNote = "generated a web token (SHEEPDOG_WEB_TOKEN in " + o.envPath + "; the phone app asks for it)"
	}

	if newConfig {
		if err := writeInitConfig(o.configPath, orch, compat); err != nil {
			return err
		}
		if _, err := config.Load(o.configPath); err != nil {
			_ = os.Remove(o.configPath)
			return fmt.Errorf("the generated config does not load (nothing kept): %w", err)
		}
		fprintf(out, "sheepdog init: wrote %s (0600)\n", o.configPath)
	} else {
		fprintf(out, "sheepdog init: kept %s as it is\n", o.configPath)
		if hint := configHint(cur, orch, compat); hint != "" {
			fprintf(out, "sheepdog init: to use this orchestrator, add to %s:\n%s", o.configPath, hint)
		}
	}
	if len(set) > 0 {
		if err := onboard.UpsertEnvFile(o.envPath, set); err != nil {
			return err
		}
		names := make([]string, 0, len(set))
		for k := range set {
			names = append(names, k)
		}
		sort.Strings(names)
		fprintf(out, "sheepdog init: wrote %s (0600): %s\n", o.envPath, strings.Join(names, ", "))
	} else {
		fprintf(out, "sheepdog init: %s unchanged\n", o.envPath)
	}
	if tokenNote != "" {
		fprintln(out, "sheepdog init: "+tokenNote)
	}
	fprintln(out, "sheepdog init: next: sheepdog doctor, then sheepdog serve, and open http://127.0.0.1:7777/voice")
	return nil
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

// writeInitConfig writes the commented template with the chosen
// orchestrator filled in.
func writeInitConfig(path, orch string, compat *config.OpenAICompat) error {
	t := config.Template()
	var lines string
	switch orch {
	case "deepseek":
		lines = fmt.Sprintf("provider = %q\nmodel = %q\n", brain.ProviderDeepSeek, brain.DefaultDeepSeekModel)
	case "compat":
		lines = fmt.Sprintf("provider = %q\n", compat.Name)
	}
	if lines != "" {
		if !strings.Contains(t, "\n[brain]\n") {
			return errors.New("config template has no [brain] table")
		}
		t = strings.Replace(t, "\n[brain]\n", "\n[brain]\n"+lines, 1)
	}
	if compat != nil {
		t += "\n" + compatTOML(*compat)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(t), 0o600)
}

// compatTOML renders one [[brain.openai_compat]] table.
func compatTOML(c config.OpenAICompat) string {
	return fmt.Sprintf("[[brain.openai_compat]]\nname = %q\nbase_url = %q\napi_key_env = %q\nmodel = %q\n", c.Name, c.BaseURL, c.APIKeyEnv, c.Model)
}

// configHint is what to add to an existing config for the chosen
// orchestrator ("" when it already uses it).
func configHint(cur config.Config, orch string, compat *config.OpenAICompat) string {
	switch orch {
	case "deepseek":
		if cur.Brain.Provider != brain.ProviderDeepSeek {
			return fmt.Sprintf("  [brain]\n  provider = %q\n  model = %q\n", brain.ProviderDeepSeek, brain.DefaultDeepSeekModel)
		}
	case "compat":
		var b strings.Builder
		if _, ok := cur.Brain.CompatProvider(compat.Name); !ok {
			for _, l := range strings.Split(strings.TrimRight(compatTOML(*compat), "\n"), "\n") {
				b.WriteString("  " + l + "\n")
			}
		}
		if cur.Brain.Provider != compat.Name {
			fmt.Fprintf(&b, "  and under [brain]: provider = %q\n", compat.Name)
		}
		return b.String()
	}
	return ""
}

// prompter asks the user questions on stdin; in non-interactive mode it
// takes each flag value or default instead.
type prompter struct {
	r              *bufio.Reader
	tty            *os.File // stdin when it is a terminal, else nil
	out            io.Writer
	nonInteractive bool
}

func newPrompter(stdin io.Reader, out io.Writer, nonInteractive bool) *prompter {
	p := &prompter{r: bufio.NewReader(stdin), out: out, nonInteractive: nonInteractive}
	if f, ok := stdin.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			p.tty = f
		}
	}
	return p
}

func (p *prompter) line() (string, error) {
	s, err := p.r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		if errors.Is(err, io.EOF) {
			return "", errors.New("input ended")
		}
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// ask returns flag when set, else asks (default def on Enter); in
// non-interactive mode a missing value without a default is an error.
func (p *prompter) ask(q, flag, def string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if p.nonInteractive {
		if def == "" {
			return "", fmt.Errorf("%s: required (pass the flag)", q)
		}
		return def, nil
	}
	for {
		if def != "" {
			fprintf(p.out, "%s [%s]: ", q, def)
		} else {
			fprintf(p.out, "%s: ", q)
		}
		v, err := p.line()
		if err != nil {
			return "", err
		}
		if v == "" {
			v = def
		}
		if v != "" {
			return v, nil
		}
	}
}

// choose picks one of ids (flag, else a numbered menu; the first is the
// default).
func (p *prompter) choose(flag, q string, ids, labels []string) (string, error) {
	if flag != "" || p.nonInteractive {
		if flag == "" {
			flag = ids[0]
		}
		for _, id := range ids {
			if flag == id {
				return id, nil
			}
		}
		return "", fmt.Errorf("%q: want one of %s", flag, strings.Join(ids, ", "))
	}
	fprintln(p.out, q+":")
	for i, l := range labels {
		fprintf(p.out, "  %d) %s\n", i+1, l)
	}
	for {
		fprintf(p.out, "choice [1]: ")
		v, err := p.line()
		if err != nil {
			return "", err
		}
		if v == "" {
			return ids[0], nil
		}
		for i, id := range ids {
			if v == fmt.Sprint(i+1) || v == id {
				return id, nil
			}
		}
	}
}

func (p *prompter) yesNo(q string, def bool) (bool, error) {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	fprintf(p.out, "%s %s: ", q, hint)
	v, err := p.line()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(v) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// secret reads a key without echoing it on a terminal (a plain line
// otherwise, e.g. piped input).
func (p *prompter) secret(q string) (string, error) {
	fprintf(p.out, "%s: ", q)
	if p.tty != nil {
		// Echo off for the key (stty works on macOS and Linux); restored
		// whatever happens.
		off := exec.Command("stty", "-echo")
		off.Stdin = p.tty
		if off.Run() == nil {
			defer func() {
				on := exec.Command("stty", "echo")
				on.Stdin = p.tty
				_ = on.Run()
				fprintln(p.out, "")
			}()
		}
	}
	return p.line()
}
