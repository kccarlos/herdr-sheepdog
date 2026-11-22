package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

const poolReadTimeout = 15 * time.Second

// poolWindow is one provider-supported quota window from CodexBar JSON.
type poolWindow struct {
	Minutes int
	Left    int
	Reset   time.Time
}

// poolReading is one provider's parsed quota snapshot; Known is false whenever
// the reading could not be taken or parsed (unknown is never zero).
type poolReading struct {
	Known   bool
	Windows []poolWindow
}

type cachedPool struct {
	at      time.Time
	reading poolReading
}

type codexBarWindow struct {
	UsedPercent   *float64 `json:"usedPercent"`
	WindowMinutes int      `json:"windowMinutes"`
	ResetsAt      string   `json:"resetsAt"`
}

// parsePool decodes `codexbar usage --provider <p> --json` output into
// primary/secondary windows. Scoped extra windows and dashboard duplicates are
// ignored; any error, non-zero exit or missing percentage yields unknown.
func parsePool(provider string, out CommandOutput) poolReading {
	if out.Err != nil || out.TimedOut || out.Exit != 0 {
		return poolReading{}
	}
	var entries []struct {
		Provider string `json:"provider"`
		Usage    *struct {
			Primary   *codexBarWindow `json:"primary"`
			Secondary *codexBarWindow `json:"secondary"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(out.Stdout), &entries) != nil {
		return poolReading{}
	}
	for _, ent := range entries {
		if !strings.EqualFold(ent.Provider, provider) || ent.Usage == nil {
			continue
		}
		var wins []poolWindow
		for _, raw := range []*codexBarWindow{ent.Usage.Primary, ent.Usage.Secondary} {
			if raw == nil || raw.UsedPercent == nil || math.IsNaN(*raw.UsedPercent) || math.IsInf(*raw.UsedPercent, 0) {
				continue
			}
			var reset time.Time
			if raw.ResetsAt != "" {
				if ts, err := time.Parse(time.RFC3339Nano, raw.ResetsAt); err == nil {
					reset = ts
				}
			}
			left := max(0, min(100, int(math.Round(100-*raw.UsedPercent))))
			wins = append(wins, poolWindow{Minutes: raw.WindowMinutes, Left: left, Reset: reset})
		}
		if len(wins) > 0 {
			return poolReading{Known: true, Windows: wins}
		}
	}
	return poolReading{}
}

func poolProvider(c flow.Candidate) string {
	switch c.CLI {
	case "claude", world.CodexCLI:
		return c.CLI
	default:
		return ""
	}
}

func projectPoolProviders(s flow.State) []string {
	var out []string
	add := func(cs []flow.Candidate) {
		for _, c := range cs {
			if p := poolProvider(c); p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	add(flow.Candidates(flow.RoleCoordinator, flow.Task{}, s.Settings))
	add(flow.Candidates(flow.RoleWorker, flow.Task{}, s.Settings))
	add(flow.Candidates(flow.RoleReviewer, flow.Task{}, s.Settings))
	for _, t := range s.Tasks {
		if !flow.Ended(t) {
			add(flow.Candidates(flow.RoleWorker, t, s.Settings))
			add(flow.Candidates(flow.RoleReviewer, t, s.Settings))
		}
	}
	return out
}

func poolStale(c cachedPool, now time.Time, ttl time.Duration) bool {
	if now.Before(c.at) || now.Sub(c.at) >= ttl {
		return true
	}
	if c.reading.Known {
		for _, w := range c.reading.Windows {
			if !w.Reset.IsZero() && w.Reset.After(c.at) && !now.Before(w.Reset) {
				return true
			}
		}
	}
	return false
}

func poolWaitReason(provider string, r poolReading, floor int, now time.Time) flow.ResolveReason {
	if !r.Known {
		return flow.ResolveReason{}
	}
	blocked := false
	var earliest time.Time
	for _, w := range r.Windows {
		if !w.Reset.IsZero() && !now.Before(w.Reset) {
			continue
		}
		if w.Left <= floor {
			blocked = true
			if !w.Reset.IsZero() && (earliest.IsZero() || w.Reset.Before(earliest)) {
				earliest = w.Reset
			}
		}
	}
	if !blocked {
		return flow.ResolveReason{}
	}
	if !earliest.IsZero() {
		layout := "3:04 PM"
		if earliest.Sub(now) > 24*time.Hour {
			layout = "Mon 3:04 PM"
		}
		return flow.ResolveReason{Kind: flow.ReasonPool, Why: fmt.Sprintf("waiting for quota until %s: the %s pool is at its floor", earliest.Local().Format(layout), provider), Reset: earliest}
	}
	return flow.ResolveReason{Kind: flow.ReasonPool, Why: fmt.Sprintf("waiting for quota: the %s pool is at its floor", provider)}
}

func poolCheck(pools map[string]poolReading, floor int, now time.Time) func(flow.Candidate) flow.ResolveReason {
	return func(c flow.Candidate) flow.ResolveReason {
		if p := poolProvider(c); p != "" {
			return poolWaitReason(p, pools[p], floor, now)
		}
		return flow.ResolveReason{}
	}
}

func (e *Engine) poolRunnerLocked() CommandFunc {
	if e.poolRun != nil {
		return e.poolRun
	}
	if e.HC != nil && e.HC.Socket != "" {
		return hcExecRunner(e.HC.Socket)
	}
	return nil
}

// refreshPools updates stale per-provider readings without holding e.mu across
// the bounded CodexBar call through hc machine.exec (design.md 2, 7.3).
func (e *Engine) refreshPools(ctx context.Context, id string) map[string]poolReading {
	e.mu.Lock()
	s, err := LoadState(e.Paths.State(id))
	if err != nil || !s.Project.Started {
		e.mu.Unlock()
		return nil
	}
	now, runner := e.now(), e.poolRunnerLocked()
	providers := projectPoolProviders(s)
	ttl, floor := time.Duration(s.Settings.PoolMinutes)*time.Minute, s.Settings.PoolFloorPercent
	snap := make(map[string]poolReading, len(providers))
	var stale []string
	for _, p := range providers {
		if c, ok := e.pools[p]; ok && !poolStale(c, now, ttl) {
			snap[p] = c.reading
		} else if runner != nil {
			stale = append(stale, p)
		}
	}
	e.mu.Unlock()
	if len(stale) == 0 {
		return snap
	}
	pctx, cancel := context.WithTimeout(ctx, poolReadTimeout)
	defer cancel()
	fresh := make(map[string]poolReading, len(stale))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range stale {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := parsePool(p, runner(pctx, os.TempDir(), nil, "codexbar", "usage", "--provider", p, "--json"))
			mu.Lock()
			fresh[p] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	e.mu.Lock()
	at := e.now()
	if e.pools == nil {
		e.pools = make(map[string]cachedPool, len(fresh))
	}
	for p, r := range fresh {
		if !r.Known {
			if prev, ok := e.pools[p]; ok && !poolWaitReason(p, prev.reading, floor, at).Reset.IsZero() {
				r = prev.reading
			}
		}
		e.pools[p] = cachedPool{at: at, reading: r}
		snap[p] = r
	}
	e.mu.Unlock()
	return snap
}
