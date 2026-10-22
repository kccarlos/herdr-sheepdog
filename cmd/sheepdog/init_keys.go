package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// `sheepdog init`'s key questions (init_flow.go).

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
