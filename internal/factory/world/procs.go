package world

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

type procTableKey struct{}
type nowKey struct{}

type procChild struct {
	pid      int
	zombie   bool
	etime    time.Duration
	hasEtime bool
}

type procTable struct {
	once     sync.Once
	children map[int][]procChild
}

// WithProcessTable attaches a per-pass process-table cache to ctx so all
// ObserveRun calls in a pass share at most one bounded `ps` read (O24).
func WithProcessTable(ctx context.Context) context.Context {
	return context.WithValue(ctx, procTableKey{}, &procTable{})
}

// WithNow attaches the pass clock reading to ctx for child-process age checks.
func WithNow(ctx context.Context, now time.Time) context.Context {
	return context.WithValue(ctx, nowKey{}, now)
}

func nowFromContext(ctx context.Context) time.Time {
	if now, ok := ctx.Value(nowKey{}).(time.Time); ok && !now.IsZero() {
		return now
	}
	return time.Now()
}

func turnAge(ctx context.Context, rec flow.RunRecord) time.Duration {
	start := rec.WorkingSince
	if start.IsZero() {
		start = rec.StartedAt
	}
	if start.IsZero() {
		return 0
	}
	return max(0, nowFromContext(ctx).Sub(start))
}

func agentPID(t hc.Terminal) int {
	if t.Foreground != nil && t.Foreground.PID > 0 {
		return t.Foreground.PID
	}
	if t.PID != nil && *t.PID > 0 {
		return *t.PID
	}
	return 0
}

func hasLiveDescendant(ctx context.Context, client *hc.Client, pid int, age time.Duration) bool {
	if client == nil || pid <= 0 || age <= 0 {
		return false
	}
	pt, _ := ctx.Value(procTableKey{}).(*procTable)
	if pt == nil {
		pt = &procTable{}
	}
	pt.once.Do(func() {
		r, err := (&Git{client: client}).run(ctx, "", "ps", []string{"-ax", "-o", "pid=,ppid=,stat=,etime="}, nil)
		if err == nil {
			pt.children = parseProcessTable(string(r.Stdout))
		}
	})
	return pt.hasLive(pid, age)
}

func parseDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}

// parseEtime parses ps elapsed time in [[dd-]hh:]mm:ss format.
func parseEtime(s string) (time.Duration, bool) {
	days := 0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		var valid bool
		if days, valid = parseDigits(d); !valid || days > 100000 {
			return 0, false
		}
		s = rest
	}
	parts := strings.Split(s, ":")
	if (days > 0 && len(parts) != 3) || (len(parts) != 2 && len(parts) != 3) {
		return 0, false
	}
	var hh, mm, ss int
	var ok bool
	if len(parts) == 3 {
		if hh, ok = parseDigits(parts[0]); !ok || (days > 0 && hh >= 24) || hh > 1000000 {
			return 0, false
		}
		parts = parts[1:]
	}
	if mm, ok = parseDigits(parts[0]); !ok || mm >= 60 {
		return 0, false
	}
	if ss, ok = parseDigits(parts[1]); !ok || ss >= 60 {
		return 0, false
	}
	return time.Duration(days)*24*time.Hour + time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second, true
}

func parseProcessTable(out string) map[int][]procChild {
	children := map[int][]procChild{}
	for ln := range strings.Lines(out) {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || pid <= 0 || ppid < 0 || pid == ppid {
			continue
		}
		var etime time.Duration
		hasEtime := false
		if len(f) >= 4 {
			var ok bool
			if etime, ok = parseEtime(f[3]); !ok {
				continue
			}
			hasEtime = true
		}
		children[ppid] = append(children[ppid], procChild{
			pid:      pid,
			zombie:   f[2][0] == 'Z' || f[2][0] == 'z',
			etime:    etime,
			hasEtime: hasEtime,
		})
	}
	return children
}

func (pt *procTable) hasLive(root int, age time.Duration) bool {
	if root <= 0 || age <= 0 || len(pt.children) == 0 {
		return false
	}
	seen := map[int]bool{root: true}
	for queue := []int{root}; len(queue) > 0; queue = queue[1:] {
		for _, ch := range pt.children[queue[0]] {
			if seen[ch.pid] {
				continue
			}
			seen[ch.pid] = true
			if !ch.zombie && (!ch.hasEtime || ch.etime < age) {
				return true
			}
			queue = append(queue, ch.pid)
		}
	}
	return false
}
