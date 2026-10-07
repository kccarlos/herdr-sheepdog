package brain_test

import (
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// Quiesce called while turns start must not race the after-turn
// bookkeeping (a WaitGroup Add concurrent with Wait at a zero counter).
func TestQuiesceConcurrentWithTurn(t *testing.T) {
	h := newHarness(t, braintest.NewModel(), nil)
	h.model.Default = &braintest.Step{Text: braintest.Reply("Fine.", "")}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				h.brain.Quiesce()
			}
		}
	}()
	for i := 0; i < 50; i++ {
		if _, err := h.ask("what is auth doing?", "auth"); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	h.brain.Quiesce()
}
