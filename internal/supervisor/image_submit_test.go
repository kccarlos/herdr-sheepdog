package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

func TestImagePromptRetriesEnterAfterSettled(t *testing.T) {
	h := twoAgents(t)
	base := h.sup.herdr
	sent := make(chan struct{})
	h.sup.herdr = &interceptHerdr{Herdr: base,
		onPrompt: func(ctx context.Context, pane, text string) (herdr.AgentInfo, error) {
			select {
			case <-sent:
				return herdr.AgentInfo{}, nil
			case <-ctx.Done():
				return herdr.AgentInfo{}, ctx.Err()
			}
		},
		onRead: func(context.Context, string, herdr.ReadSource, int) (herdr.ReadResult, error) {
			return herdr.ReadResult{Text: "❯ [Image #1] look\n────────"}, nil
		},
		onSendKeys: func(ctx context.Context, pane string, keys ...string) error {
			if len(keys) != 1 || keys[0] != "enter" {
				t.Errorf("keys: %v", keys)
			}
			close(sent)
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.sup.prompt(ctx, state.Agent{PaneID: "w5:p1", Kind: "claude"}, "look\n/tmp/photo.png"); err != nil {
		t.Fatal(err)
	}
}
