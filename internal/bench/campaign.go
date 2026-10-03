package bench

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// CampaignOptions configure a campaign.
type CampaignOptions struct {
	Run RunOptions
	// Ports are the scratch daemons' ports: one worker each, so at most
	// len(Ports) conversations at once (default DefaultPorts).
	Ports  []int
	Repeat int
	// BudgetUSD stops starting runs once the estimated spend reaches it
	// (0: no limit); runs not started are not_run.
	BudgetUSD float64
	// Done is told about each finished run with the campaign's spend so
	// far (for continuous spend logging).
	Done func(r Run, spentUSD float64)
}

// RunCampaign runs every scenario Repeat times over the ports' workers.
func RunCampaign(ctx context.Context, scs []Scenario, o CampaignOptions) ([]Run, *SpendMeter) {
	ports := o.Ports
	if len(ports) == 0 {
		ports = DefaultPorts
	}
	repeat := max(o.Repeat, 1)
	type job struct {
		sc     Scenario
		repeat int
	}
	jobs := make(chan job)
	total := NewSpendMeter(o.Run.Config.Gemini.Model)
	var mu sync.Mutex
	var runs []Run
	var wg sync.WaitGroup
	for _, port := range ports {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			for j := range jobs {
				mu.Lock()
				spent := total.Total().TotalUSD
				mu.Unlock()
				var r Run
				switch {
				case ctx.Err() != nil:
					r = notRun(j.sc, j.repeat, "the campaign was cancelled")
				case o.BudgetUSD > 0 && spent >= o.BudgetUSD:
					r = notRun(j.sc, j.repeat, fmt.Sprintf("budget $%.2f reached ($%.2f spent)", o.BudgetUSD, spent))
				default:
					r = RunScenario(ctx, j.sc, j.repeat, port, o.Run)
				}
				mu.Lock()
				runs = append(runs, r)
				total.Meter.Merge(meterOf(r))
				total.Audio(int64(r.Spend.LiveInMin*60000), int64(r.Spend.LiveOutMin*60000))
				spent = total.Total().TotalUSD
				mu.Unlock()
				if o.Done != nil {
					o.Done(r, spent)
				}
				// Let the port's listener go before the next daemon binds it.
				time.Sleep(500 * time.Millisecond)
			}
		}(port)
	}
	// Repeats interleave (every scenario once, then again), so a budget
	// stop leaves whole passes rather than whole scenarios unrun.
	for rep := 1; rep <= repeat; rep++ {
		for _, sc := range scs {
			jobs <- job{sc: sc, repeat: rep}
		}
	}
	close(jobs)
	wg.Wait()
	sortRuns(runs)
	return runs, total
}

func notRun(sc Scenario, repeat int, why string) Run {
	return Run{Scenario: sc.ID, Category: sc.Category, Repeat: repeat, Status: eval.RunNotRun, Failures: []eval.Failure{{Type: eval.FailNotRun, Detail: why}}, EndReason: "not_run"}
}

// meterOf rebuilds a run's meter from its spend lines.
func meterOf(r Run) *eval.Meter {
	m := eval.NewMeter()
	for _, l := range r.Spend.Lines {
		m.Add(l.Role, l.Model, l.Usage)
	}
	return m
}
