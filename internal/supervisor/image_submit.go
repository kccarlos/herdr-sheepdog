package supervisor

import (
	"context"
	"regexp"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

var picturePath = regexp.MustCompile(`(?im)^[/~]\S*\.(png|jpe?g|gif|webp|bmp|heic)[ \t]*\z`)

// prompt waits for the backend to finish before releasing the pane send lock.
func (s *Supervisor) prompt(ctx context.Context, row state.Agent, text string) error {
	_, err := s.herdr.Prompt(ctx, row.PaneID, text)
	return err
}
