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
	"strconv"
	"strings"
	"time"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// onboardEndpoints are the API roots init and doctor validate keys
// against (tests point them at an httptest server).
var onboardEndpoints = onboard.DefaultEndpoints

func printDoctorUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog doctor [flags]")
	fprintln(w, "")
	fprintln(w, "Checks the setup and prints one line per check with the fix: herdr and")
	fprintln(w, "its backend, the herdr-app panel, Sheepdog.app on macOS,")
	fprintln(w, "every configured key (live and cheap; the balance where the API shows it),")
	fprintln(w, "a tool-calling probe on OpenAI-compatible orchestrators (a few tokens),")
	fprintln(w, "the orchestrator and voice providers, the web port, the bind/token")
	fprintln(w, "pairing, push and the secret files' permissions. Exit 1 on any FAIL.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH   env file (default ./.env, else ~/.config/sheepdog/.env, as serve)")
	fprintln(w, "  --no-probe        skip the tool-calling probe (no tokens spent)")
	fprintln(w, "  --report          print a diagnostic bundle for a bug report instead: versions,")
	fprintln(w, "                    OS, config, doctor, status and the last log lines, with every")
	fprintln(w, "                    secret, key and token redacted")
	fprintln(w, "  --out FILE        with --report: write it to FILE (mode 0600) instead of stdout")
	fprintln(w, "  --lines N         with --report: log lines to include (default 200)")
}

// runDoctor implements `sheepdog doctor`.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printDoctorUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	envFile := fs.String("env-file", "", "env file")
	noProbe := fs.Bool("no-probe", false, "skip the tool-calling probe")
	report := fs.Bool("report", false, "diagnostic bundle")
	out := fs.String("out", "", "report file")
	lines := fs.Int("lines", 200, "log lines in the report")
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
	if *report {
		if err != nil {
			cfg = config.Default()
		}
		text := buildReport(ctx, reportOptions{configPath: cfgPath, envPath: envPath, cfg: cfg, loadErr: err, lines: *lines, now: time.Now()})
		if *out == "" {
			fprintln(stdout, text)
			return 0
		}
		if werr := os.WriteFile(*out, []byte(text+"\n"), 0o600); werr != nil {
			fprintf(stderr, "sheepdog doctor: %v\n", werr)
			return 1
		}
		fprintf(stdout, "wrote %s: review it, then paste it into a GitHub issue (https://github.com/%s/issues/new)\n", *out, releaseRepo)
		return 0
	}
	if err != nil {
		fprintf(stdout, "%s\n", onboard.Result{Name: "config", Status: onboard.Fail, Detail: err.Error(), Fix: "fix the file, or move it away and run sheepdog init"}.Line())
		return 1
	}
	src := cfgPath
	if _, err := os.Stat(cfgPath); err != nil {
		src = "defaults (no " + cfgPath + ")"
	}
	fprintln(stdout, onboard.Result{Name: "config", Status: onboard.OK, Detail: "loaded " + src}.Line())
	environment := onboard.Detect(cfg, onboard.SystemProbe(), herdrsheepdog.Panel, herdrsheepdog.Layout)
	results := onboard.Doctor(ctx, cfg, onboard.DoctorOptions{
		Endpoints: onboardEndpoints(), Probe: !*noProbe, ConfigPath: cfgPath, EnvPath: envPath,
		Environment: &environment,
	})
	if r, ok := envLocationCheck(cfg.DataDir, envPath); ok {
		results = append(results, r)
	}
	for _, r := range results {
		fprintln(stdout, r.Line())
	}
	if onboard.Failed(results) {
		return 1
	}
	return 0
}

// envLocationCheck warns when the keys come from an env file other than
// the default (a ./.env in some working directory): a restart from another
// directory would not find it. It looks at the file this doctor loaded
// and at the one the last daemon recorded.
func envLocationCheck(dataDir, loaded string) (onboard.Result, bool) {
	def := configEnvPath()
	path := ""
	if _, err := os.Stat(loaded); err == nil {
		path, _ = filepath.Abs(loaded)
	} else if rec := daemon.ReadEnvRecord(dataDir); rec.HadKeys {
		path = rec.Path
	}
	if path == "" || path == def {
		return onboard.Result{}, false
	}
	return onboard.Result{Name: "env file location", Status: onboard.Warn,
		Detail: "keys come from " + path + ", not the default env file",
		Fix:    "copy it to " + def + " (chmod 600)"}, true
}

// secretPromptHook, when set (tests), is told the label of every key
// prompt: the keys all go through secret.
var secretPromptHook func(label string)

// prompter asks the user questions on stdin; in non-interactive mode it
// takes each flag value or default instead.
type prompter struct {
	r              *bufio.Reader
	tty            *os.File // stdin when it is a terminal, else nil
	out            io.Writer
	nonInteractive bool
}

func newPrompter(stdin io.Reader, out io.Writer) *prompter {
	p := &prompter{r: bufio.NewReader(stdin), out: out}
	if f, ok := stdin.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			p.tty = f
		}
	}
	return p
}

func (p *prompter) line() (string, error) {
	s, err := p.r.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || s == "") {
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

// chooseDef picks one of ids: the flag if given, def when nothing is
// asked (non-interactive), else a numbered menu where Enter takes def.
func (p *prompter) chooseDef(q string, ids, labels []string, def string, flag ...string) (string, error) {
	f := ""
	if len(flag) > 0 {
		f = flag[0]
	}
	if f != "" || p.nonInteractive {
		if f == "" {
			f = def
		}
		for _, id := range ids {
			if f == id {
				return id, nil
			}
		}
		return "", fmt.Errorf("%q: want one of %s", f, strings.Join(ids, ", "))
	}
	defN := 1
	for i, id := range ids {
		if id == def {
			defN = i + 1
		}
	}
	fprintln(p.out, q+":")
	for i, l := range labels {
		fprintf(p.out, "  %d) %s\n", i+1, l)
	}
	for {
		fprintf(p.out, "choice [%d]: ", defN)
		v, err := p.line()
		if err != nil {
			return "", err
		}
		if v == "" {
			return ids[defN-1], nil
		}
		for i, id := range ids {
			if v == fmt.Sprint(i+1) || v == id {
				return id, nil
			}
		}
	}
}

// pickName picks a model from a numbered list: Enter takes def (when it
// has one), a number takes that entry, anything else is a typed name. A
// name that cannot be saved asks again.
func (p *prompter) pickName(q string, items []string, def string, notes map[string]string) (string, error) {
	const shown = 15
	if len(items) > 0 {
		fprintln(p.out, q+":")
		for i, m := range items {
			if i >= shown {
				fprintf(p.out, "  … %d more; type a name to use one\n", len(items)-shown)
				break
			}
			tag := []string{}
			if m == def {
				tag = append(tag, "default")
			}
			if n := notes[m]; n != "" {
				tag = append(tag, n)
			}
			if len(tag) > 0 {
				fprintf(p.out, "  %d) %s (%s)\n", i+1, m, strings.Join(tag, ", "))
			} else {
				fprintf(p.out, "  %d) %s\n", i+1, m)
			}
		}
	}
	for {
		if def != "" {
			fprintf(p.out, "%s [%s] (Enter keeps it; a number or a name picks another): ", q, def)
		} else {
			fprintf(p.out, "%s (a number or a name): ", q)
		}
		v, err := p.line()
		if err != nil {
			return "", err
		}
		switch {
		case v == "" && def != "":
			return def, nil
		case v == "":
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(items) {
			return items[n-1], nil
		}
		if onboard.ValidModelName(v) {
			return v, nil
		}
		fprintln(p.out, "  a model name may only hold letters, digits and . _ / -")
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
	if secretPromptHook != nil {
		secretPromptHook(q)
	}
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
