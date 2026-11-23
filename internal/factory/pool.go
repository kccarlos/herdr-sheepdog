package factory

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
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
	Family  string
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

func antigravityFamily(s string) string {
	if low := strings.ToLower(s); strings.Contains(low, "gemini") {
		return "gemini"
	} else if strings.Contains(low, "3p") || strings.Contains(low, "claude") || strings.Contains(low, "gpt") {
		return "3p"
	}
	return ""
}

// parsePool decodes `codexbar usage --provider <p> --json` output into quota
// windows; antigravity windows are scoped by model family (gemini vs 3p).
func parsePool(provider string, out CommandOutput) poolReading {
	if out.Err != nil || out.TimedOut || out.Exit != 0 {
		return poolReading{}
	}
	var entries []struct {
		Provider         string            `json:"provider"`
		RateWindowLabels map[string]string `json:"rateWindowLabels"`
		Usage            *struct {
			Primary, Secondary *codexBarWindow
			ExtraRateWindows   []struct {
				ID, Title string
				Window    *codexBarWindow
			}
		}
	}
	if json.Unmarshal([]byte(out.Stdout), &entries) != nil {
		return poolReading{}
	}
	for _, ent := range entries {
		if !strings.EqualFold(ent.Provider, provider) || ent.Usage == nil {
			continue
		}
		var wins []poolWindow
		add := func(raw *codexBarWindow, fam string) bool {
			if raw == nil || raw.UsedPercent == nil || math.IsNaN(*raw.UsedPercent) || math.IsInf(*raw.UsedPercent, 0) {
				return false
			}
			reset, _ := time.Parse(time.RFC3339Nano, raw.ResetsAt)
			wins = append(wins, poolWindow{Minutes: raw.WindowMinutes, Left: max(0, min(100, int(math.Round(100-*raw.UsedPercent)))), Reset: reset, Family: fam})
			return true
		}
		if strings.EqualFold(provider, "antigravity") {
			seen := map[string]bool{}
			for _, ew := range ent.Usage.ExtraRateWindows {
				if fam := cmp.Or(antigravityFamily(ew.ID), antigravityFamily(ew.Title)); fam != "" && add(ew.Window, fam) {
					seen[fam] = true
				}
			}
			for k, raw := range map[string]*codexBarWindow{"primary": ent.Usage.Primary, "secondary": ent.Usage.Secondary} {
				if fam := antigravityFamily(ent.RateWindowLabels[k]); fam != "" && !seen[fam] {
					add(raw, fam)
				}
			}
		} else {
			add(ent.Usage.Primary, "")
			add(ent.Usage.Secondary, "")
		}
		if len(wins) > 0 {
			return poolReading{Known: true, Windows: wins}
		}
	}
	return poolReading{}
}

func poolFamily(c flow.Candidate) string {
	if m := strings.ToLower(strings.TrimSpace(c.Model)); c.CLI != world.AgyCLI {
		return ""
	} else if strings.HasPrefix(m, "gemini-") {
		return "gemini"
	} else if strings.HasPrefix(m, "claude-") || strings.HasPrefix(m, "gpt-") {
		return "3p"
	}
	return ""
}

func poolProvider(c flow.Candidate) string {
	if c.CLI == "claude" || c.CLI == world.CodexCLI {
		return c.CLI
	}
	if poolFamily(c) != "" {
		return "antigravity"
	}
	return ""
}

func poolCooldownKey(c flow.Candidate) string {
	if fam := poolFamily(c); fam != "" {
		return c.CLI + "/antigravity/" + fam
	}
	return c.CLI + "/" + cmp.Or(poolProvider(c), c.CLI)
}

func projectCandidates(s flow.State) []flow.Candidate {
	var out []flow.Candidate
	for _, role := range []string{flow.RoleCoordinator, flow.RoleWorker, flow.RoleReviewer} {
		out = append(out, flow.Candidates(role, flow.Task{}, s.Settings)...)
	}
	if s.Project.Run != nil && s.Project.Run.State != flow.PhaseEnded {
		out = append(out, flow.Candidate{CLI: s.Project.Run.CLI, Model: s.Project.Run.Model})
	}
	for _, t := range s.Tasks {
		if !flow.Ended(t) {
			for _, cs := range t.Request {
				out = append(out, cs...)
			}
			if a := flow.OpenAttempt(t); a != nil && a.Run != nil && a.Run.State != flow.PhaseEnded {
				out = append(out, flow.Candidate{CLI: a.Run.CLI, Model: a.Run.Model})
			}
		}
	}
	return out
}

func projectPoolProviders(s flow.State) []string {
	var out []string
	for _, c := range projectCandidates(s) {
		if p := poolProvider(c); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
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

func formatReset(reset, now time.Time) string {
	return reset.Local().Format(map[bool]string{true: "Mon 3:04 PM", false: "3:04 PM"}[reset.Sub(now) > 24*time.Hour])
}

func loadCooldowns(path string) map[string]time.Time {
	m := map[string]time.Time{}
	_ = readJSON(path, &m)
	return m
}

func (e *Engine) recordQuotaCooldownLocked(s flow.State, c flow.Candidate, limitLine string, now time.Time) {
	path := e.Paths.poolCooldowns()
	until := cmp.Or(world.ParseLimitReset(limitLine, now), now.Add(time.Duration(s.Settings.PoolMinutes)*time.Minute))
	m := loadCooldowns(path)
	maps.DeleteFunc(m, func(_ string, t time.Time) bool { return !now.Before(t) })
	if k := poolCooldownKey(c); until.After(m[k]) {
		m[k] = until
	}
	_ = writeJSON(path, m)
}

func quotaReason(cause string, reset, now time.Time) flow.ResolveReason {
	if !reset.IsZero() {
		return flow.ResolveReason{Kind: flow.ReasonPool, Why: fmt.Sprintf("waiting for quota until %s: %s", formatReset(reset, now), cause), Reset: reset}
	}
	return flow.ResolveReason{Kind: flow.ReasonPool, Why: "waiting for quota: " + cause}
}

func poolWaitReason(provider, family string, r poolReading, floor int, now time.Time) flow.ResolveReason {
	if !r.Known {
		return flow.ResolveReason{}
	}
	blocked := false
	var earliest time.Time
	for _, w := range r.Windows {
		if (w.Family != "" && family != "" && w.Family != family) || (!w.Reset.IsZero() && !now.Before(w.Reset)) {
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
	return quotaReason(fmt.Sprintf("the %s pool is at its floor", provider), earliest, now)
}

func poolCheck(pools map[string]poolReading, cooldownPath string, floor int, now time.Time) func(flow.Candidate) flow.ResolveReason {
	return func(c flow.Candidate) flow.ResolveReason {
		p := poolProvider(c)
		var cd, pr flow.ResolveReason
		if until := loadCooldowns(cooldownPath)[poolCooldownKey(c)]; now.Before(until) {
			cd = quotaReason(cmp.Or(p, c.CLI)+" hit its usage limit", until, now)
		}
		if p != "" {
			pr = poolWaitReason(p, poolFamily(c), pools[p], floor, now)
		}
		if cd.Why != "" && (pr.Why == "" || pr.Reset.IsZero() || !pr.Reset.After(cd.Reset)) {
			return cd
		}
		return pr
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
			if prev, ok := e.pools[p]; ok && !poolWaitReason(p, "", prev.reading, floor, at).Reset.IsZero() {
				r = prev.reading
			}
		}
		e.pools[p] = cachedPool{at: at, reading: r}
		snap[p] = r
	}
	e.mu.Unlock()
	return snap
}
