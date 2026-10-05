package scorecard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

func TestScorecardNorthStar(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	ev := func(sec int, typ, pane string, payload any) eventlog.Event {
		b, _ := json.Marshal(payload)
		return eventlog.Event{TS: t0.Add(time.Duration(sec) * time.Second), Type: typ, PaneID: pane, Payload: b}
	}
	events := []eventlog.Event{
		ev(0, "pending.created", "w1:p1", map[string]any{"agent": "auth-codex"}),
		ev(1, "supervisor.relay", "", map[string]any{"heard": "approve it"}),
		ev(3, "herdr.send", "w1:p1", map[string]any{"kind": "keys", "keys": []string{"y"}}),
		ev(4, "voice.tool_result", "", map[string]any{"spoken": "I stopped it."}),
	}
	r := Analyze("s", events)
	if r.NorthStar == nil || r.NorthStar.Done.N != 1 || r.NorthStar.Done.K != 0 || r.NorthStar.Safe.K != 0 {
		t.Fatalf("north star = %+v", r.NorthStar)
	}
	var sb strings.Builder
	if err := WriteMarkdown(&sb, []Report{r}, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "### North star") {
		t.Errorf("markdown lacks the block:\n%s", sb.String())
	}
	if Analyze("empty", nil).NorthStar != nil {
		t.Errorf("an empty slice has no block")
	}
}
