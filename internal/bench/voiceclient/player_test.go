package voiceclient

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func samples(d time.Duration) []int16 { return make([]int16, samplesFor(d)) }

// run ticks the player every 10 ms from t0 for d and collects reports
// and the samples played.
func run(p *Player, t0 time.Time, from, d time.Duration) (reports []Report, played int) {
	for t := from; t <= from+d; t += ms(10) {
		pl, r := p.Tick(t0.Add(t))
		played += len(pl)
		reports = append(reports, r...)
	}
	return reports, played
}

func TestPlayerStartsAtTargetAndDrains(t *testing.T) {
	p := NewPlayer(PlayerConfig{})
	t0 := time.Now()
	p.Tick(t0)
	p.Push(t0, samples(ms(100))) // under the 150 ms target
	if r, _ := run(p, t0, ms(10), ms(100)); len(r) != 0 {
		t.Fatalf("started under the target: %v", r)
	}
	p.Push(t0.Add(ms(110)), samples(ms(900))) // now 1 s buffered
	r, played := run(p, t0, ms(120), ms(1400))
	if len(r) != 2 || r[0].State != wsproto.PlaybackStarted || r[1].State != wsproto.PlaybackDrained {
		t.Fatalf("reports = %+v", r)
	}
	if r[1].PlayedMS < 990 || r[1].PlayedMS > 1000 || played != samplesFor(ms(1000)) {
		t.Fatalf("drained played_ms = %d, played %d samples", r[1].PlayedMS, played)
	}
	if u, d, b := p.Stats(); u != 0 || d != 1 || b != 0 || p.Active() {
		t.Fatalf("stats = %d %d %d active %v", u, d, b, p.Active())
	}
}

// A short reply that never reaches the target still plays after
// target + 250 ms.
func TestPlayerShortUtteranceStarts(t *testing.T) {
	p := NewPlayer(PlayerConfig{})
	t0 := time.Now()
	p.Tick(t0)
	p.Push(t0, samples(ms(80)))
	r, played := run(p, t0, ms(10), ms(900))
	if len(r) != 2 || r[0].State != wsproto.PlaybackStarted || r[1].PlayedMS != 80 || played != samplesFor(ms(80)) {
		t.Fatalf("reports = %+v played %d", r, played)
	}
}

// A mid-run underrun that resumes within 300 ms is the same run: no
// drained, no second started, played_ms keeps counting, and the target
// rises by 100 ms.
func TestPlayerUnderrunIsOneRun(t *testing.T) {
	p := NewPlayer(PlayerConfig{})
	t0 := time.Now()
	p.Tick(t0)
	p.Push(t0, samples(ms(200)))
	r1, _ := run(p, t0, ms(10), ms(300)) // plays 200 ms, runs dry
	p.Push(t0.Add(ms(400)), samples(ms(300)))
	r2, _ := run(p, t0, ms(400), ms(1000))
	all := append(r1, r2...)
	if len(all) != 2 || all[0].State != wsproto.PlaybackStarted || all[1].State != wsproto.PlaybackDrained || all[1].PlayedMS != 500 {
		t.Fatalf("reports = %+v", all)
	}
	if u, _, _ := p.Stats(); u != 1 || p.target != ms(250) {
		t.Fatalf("underruns %d target %v", u, p.target)
	}
}

func TestPlayerFlush(t *testing.T) {
	p := NewPlayer(PlayerConfig{})
	if r := p.Flush(); r.State != wsproto.PlaybackFlushed || r.PlayedMS != 0 {
		t.Fatalf("idle flush = %+v", r)
	}
	t0 := time.Now()
	p.Tick(t0)
	p.Push(t0, samples(ms(2000)))
	run(p, t0, ms(10), ms(700))
	r := p.Flush()
	if r.PlayedMS < 650 || r.PlayedMS > 720 || p.Active() || p.BufferedMS() != 0 {
		t.Fatalf("flush = %+v active %v", r, p.Active())
	}
}
