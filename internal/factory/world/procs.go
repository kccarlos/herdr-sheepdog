package world

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

type procTableKey struct{}

type procChild struct {
	pid    int
	zombie bool
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

func agentPID(t hc.Terminal) int {
	if t.Foreground != nil && t.Foreground.PID > 0 {
		return t.Foreground.PID
	}
	if t.PID != nil && *t.PID > 0 {
		return *t.PID
	}
	return 0
}

func hasLiveDescendant(ctx context.Context, client *hc.Client, pid int) bool {
	if client == nil || pid <= 0 {
		return false
	}
	pt, _ := ctx.Value(procTableKey{}).(*procTable)
	if pt == nil {
		pt = &procTable{}
	}
	pt.once.Do(func() {
		r, err := NewGit(client, nil, nil).run(ctx, "", "ps", []string{"-ax", "-o", "pid=,ppid=,stat="}, nil)
		if err == nil {
			pt.children = parseProcessTable(string(r.Stdout))
		}
	})
	return pt.hasLive(pid)
}

func parseProcessTable(out string) map[int][]procChild {
	var children map[int][]procChild
	for ln := range strings.Lines(out) {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || pid <= 0 || ppid < 0 || pid == ppid || f[2] == "" {
			continue
		}
		if children == nil {
			children = map[int][]procChild{}
		}
		children[ppid] = append(children[ppid], procChild{pid: pid, zombie: f[2][0] == 'Z' || f[2][0] == 'z'})
	}
	return children
}

func (pt *procTable) hasLive(root int) bool {
	if root <= 0 || len(pt.children) == 0 {
		return false
	}
	seen := map[int]bool{root: true}
	for queue := []int{root}; len(queue) > 0; queue = queue[1:] {
		for _, ch := range pt.children[queue[0]] {
			if !ch.zombie {
				return true
			}
			if !seen[ch.pid] {
				seen[ch.pid] = true
				queue = append(queue, ch.pid)
			}
		}
	}
	return false
}
