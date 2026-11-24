package world

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

type procTableKey struct{}

type procChild struct {
	pid   int
	etime time.Duration // < 0 if zombie or unreadable etime
}

type procTable struct {
	once       sync.Once
	startGrace time.Duration
	children   map[int][]procChild
	etimes     map[int]time.Duration
}

// WithProcessTable attaches a per-pass process-table cache to ctx so all
// ObserveRun calls in a pass share at most one bounded `ps` read (O24).
func WithProcessTable(ctx context.Context, startGrace time.Duration) context.Context {
	return context.WithValue(ctx, procTableKey{}, &procTable{startGrace: startGrace})
}

func agentPID(t hc.Terminal) int {
	if hc.IsRemote(t.Address) {
		return 0
	}
	if t.Foreground != nil && t.Foreground.PID > 0 {
		return t.Foreground.PID
	}
	return max(deref(t.PID), 0)
}

func hasLiveDescendant(ctx context.Context, client *hc.Client, pid int) bool {
	if client == nil || pid <= 0 {
		return false
	}
	pt, _ := ctx.Value(procTableKey{}).(*procTable)
	pt = cmp.Or(pt, &procTable{})
	pt.once.Do(func() {
		r, err := (&Git{client: client}).run(ctx, "", "ps", []string{"-ax", "-o", "pid=,ppid=,stat=,etime="}, nil)
		if err == nil {
			pt.children, pt.etimes = parseProcessTable(string(r.Stdout))
		}
	})
	grace := pt.startGrace
	if grace <= 0 {
		grace = time.Duration(flow.DefaultSettings().StartMinutes) * time.Minute
	}
	return pt.hasLive(pid, grace)
}

// parseEtime parses ps elapsed time in [[dd-]hh:]mm:ss format.
func parseEtime(s string) (time.Duration, bool) {
	var days, hh uint64
	d, rest, hasDays := strings.Cut(s, "-")
	if hasDays {
		var err error
		if days, err = strconv.ParseUint(d, 10, 32); err != nil || days > 100000 {
			return 0, false
		}
		s = rest
	}
	parts := strings.Split(s, ":")
	if hasDays && len(parts) != 3 || len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	if len(parts) == 3 {
		var err error
		if hh, err = strconv.ParseUint(parts[0], 10, 32); err != nil || hasDays && hh >= 24 || hh > 1000000 {
			return 0, false
		}
		parts = parts[1:]
	}
	mm, err1 := strconv.ParseUint(parts[0], 10, 32)
	ss, err2 := strconv.ParseUint(parts[1], 10, 32)
	if err1 != nil || err2 != nil || mm >= 60 || ss >= 60 {
		return 0, false
	}
	return time.Duration(days)*24*time.Hour + time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second, true
}

func parseProcessTable(out string) (map[int][]procChild, map[int]time.Duration) {
	children := map[int][]procChild{}
	etimes := map[int]time.Duration{}
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
		etime := time.Duration(-1)
		if len(f) >= 4 && f[2][0] != 'Z' && f[2][0] != 'z' {
			if d, ok := parseEtime(f[3]); ok {
				etime, etimes[pid] = d, d
			}
		}
		children[ppid] = append(children[ppid], procChild{pid: pid, etime: etime})
	}
	return children, etimes
}

func (pt *procTable) hasLive(root int, grace time.Duration) bool {
	agentEtime, ok := pt.etimes[root]
	if !ok || len(pt.children) == 0 {
		return false
	}
	seen := map[int]bool{root: true}
	for queue := []int{root}; len(queue) > 0; queue = queue[1:] {
		for _, ch := range pt.children[queue[0]] {
			if seen[ch.pid] {
				continue
			}
			seen[ch.pid] = true
			if ch.etime >= 0 && agentEtime-ch.etime >= grace {
				return true
			}
			queue = append(queue, ch.pid)
		}
	}
	return false
}
