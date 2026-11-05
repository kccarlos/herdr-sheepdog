package supervisor

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

var picturePath = regexp.MustCompile(`(?im)^[/~]\S*\.(png|jpe?g|gif|webp|bmp|heic)\s*$`)
var imageMarker = regexp.MustCompile(`\[Image #\d+\]`)

// prompt sends once. Claude can consume Enter while loading a pasted image;
// retry it only while the send is in flight and the image row has settled.
func (s *Supervisor) prompt(ctx context.Context, row state.Agent, text string) error {
	if row.Kind != "claude" || !picturePath.MatchString(text) {
		_, err := s.herdr.Prompt(ctx, row.PaneID, text)
		return err
	}
	type result struct {
		info herdr.AgentInfo
		err  error
	}
	done := make(chan result, 1)
	go func() { info, err := s.herdr.Prompt(ctx, row.PaneID, text); done <- result{info, err} }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	last := ""
	for {
		select {
		case r := <-done:
			return r.err
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			r := <-done
			return r.err
		case <-ticker.C:
			read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceVisible, 0)
			if err != nil {
				last = ""
				continue
			}
			line := imageInputLine(read.Text)
			if line != "" && line == last {
				select {
				case r := <-done:
					return r.err
				default:
				}
				if err := s.herdr.SendKeys(ctx, row.PaneID, "enter"); err != nil {
					s.log.Log(logSource, "supervisor.image_enter_error", row.PaneID, map[string]any{"error": err.Error()})
				}
				r := <-done
				return r.err
			}
			last = line
		}
	}
}

func imageInputLine(screen string) string {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(reANSI.ReplaceAllString(lines[i], ""))
		if strings.HasPrefix(line, "❯") {
			if imageMarker.MatchString(line) {
				return line
			}
			return ""
		}
	}
	return ""
}
