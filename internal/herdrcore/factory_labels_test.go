package herdrcore_test

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

func TestFactoryLabelsPlumbedThroughHerdrcore(t *testing.T) {
	b, srv := newBackend(t)
	term := reported("t-worker", "claude-code", "idle")
	term.Labels = map[string]string{
		"sheepdog.project": "demo",
		"sheepdog.role":    "worker",
		"sheepdog.task":    "T1",
	}
	srv.Add(term)

	ch := runWatcher(t, b)
	first := receive(t, ch)
	if first.PaneID != "t-worker" || first.To != herdr.StateIdle {
		t.Fatalf("first transition = %+v", first)
	}
	if first.Labels["sheepdog.project"] != "demo" || first.Labels["sheepdog.role"] != "worker" {
		t.Fatalf("transition labels = %v, want sheepdog.project=demo sheepdog.role=worker", first.Labels)
	}
	awaitSubscribers(t, srv, 2)

	srv.SetStatus("t-worker", &hc.Status{State: "working", App: "claude-code"})
	next := receive(t, ch)
	if next.To != herdr.StateWorking {
		t.Fatalf("next transition = %+v", next)
	}
	if next.Labels["sheepdog.project"] != "demo" || next.Labels["sheepdog.role"] != "worker" {
		t.Fatalf("working transition labels = %v, want sheepdog.project=demo sheepdog.role=worker", next.Labels)
	}
}
