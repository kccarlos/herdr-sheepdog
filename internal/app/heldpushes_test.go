package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// A push held when the previous daemon stopped is dropped at the next
// start (reason restart) so it never waits for a listener for good; one
// that already has a fate, and a silent one, are left alone.
func TestResolveHeldPushesAtStart(t *testing.T) {
	elog, err := eventlog.Open(t.TempDir(), eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = elog.Close() }()
	a := &App{elog: elog, log: elog}

	held := func(id, text, urgency string) {
		elog.Log("voice", "voice.push_held", "", map[string]any{"push_id": id, "text": text, "urgency": urgency, "agent": "auth", "reason": "no_listener"})
	}
	held("p1", "Auth finished.", "when_idle")
	held("p2", "Docs is stuck.", "interrupt")
	held("p3", "Quiet context.", "silent")
	held("p4", "Build done.", "when_idle")
	held("p5", "Old daemon, no id.", "when_idle")
	elog.Log("voice", "voice.push_released", "", map[string]any{"push_id": "p1", "text": "Auth finished.", "held_ms": 1000})
	elog.Log("voice", "voice.push_dropped", "", map[string]any{"push_id": "p4", "text": "Build done.", "age_ms": 300000})
	elog.Log("daemon", "daemon.start", "", map[string]any{"version": "v9"})

	a.resolveHeldPushes(time.Now())

	var dropped []map[string]any
	evs, err := elog.ReadDay(time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	seenStart := false
	for _, ev := range evs {
		if ev.Type == "daemon.start" {
			seenStart = true
		}
		if ev.Type != "voice.push_dropped" || !seenStart {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		dropped = append(dropped, p)
	}
	if len(dropped) != 2 {
		t.Fatalf("dropped after the start = %v, want p2 and p5", dropped)
	}
	if dropped[0]["push_id"] != "p2" || dropped[0]["reason"] != "restart" || dropped[0]["urgency"] != "interrupt" || dropped[0]["text"] != "Docs is stuck." || dropped[0]["agent"] != "auth" {
		t.Errorf("first drop = %v", dropped[0])
	}
	if dropped[1]["text"] != "Old daemon, no id." || dropped[1]["reason"] != "restart" {
		t.Errorf("second drop = %v", dropped[1])
	}

	// A second start finds nothing left to resolve.
	n := len(evs)
	a.resolveHeldPushes(time.Now())
	if again, _ := elog.ReadDay(time.Now().UTC().Format("2006-01-02")); len(again) != n {
		t.Errorf("second resolve logged %d more events, want 0", len(again)-n)
	}
}
