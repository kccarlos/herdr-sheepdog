package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

func TestImagePromptNeverRetriesEnterAndWaitsOnCancel(t *testing.T) {
	h := twoAgents(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	sent := make(chan struct{}, 1)
	h.sup.herdr = &interceptHerdr{Herdr: h.sup.herdr,
		onPrompt: func(ctx context.Context, pane, text string) (herdr.AgentInfo, error) {
			close(entered)
			<-ctx.Done()
			<-release
			return herdr.AgentInfo{}, ctx.Err()
		},
		onRead: func(context.Context, string, herdr.ReadSource, int) (herdr.ReadResult, error) {
			return herdr.ReadResult{Text: "❯ [Image #1] old\n────────"}, nil
		},
		onSendKeys: func(context.Context, string, ...string) error { sent <- struct{}{}; return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- h.sup.prompt(ctx, state.Agent{PaneID: "w5:p1", Kind: "claude"}, "look\n/tmp/photo.png")
	}()
	<-entered
	select {
	case <-sent:
		t.Error("spurious Enter")
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
		t.Error("returned before Prompt finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
}
