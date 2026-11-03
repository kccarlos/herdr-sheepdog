package hc_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func TestLabelWritesOnlyTheKeysGivenAndEmitsTheWholeMap(t *testing.T) {
	server := hctest.New(t)
	client := hc.NewClient(server.Path)
	ctx := context.Background()
	ran, err := client.Run(ctx, hc.RunParams{Argv: []string{"claude"}, Labels: map[string]string{"keep": "me", "herdr.seen": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := client.Subscribe(ctx, hc.SubscribeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	got, err := client.Label(ctx, ran.Address, map[string]string{"herdr.seen": "2", "herdr.pinned": "1"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"keep": "me", "herdr.seen": "2", "herdr.pinned": "1"}
	if !reflect.DeepEqual(got.Labels, want) {
		t.Fatalf("labels after Label = %v, want %v", got.Labels, want)
	}
	assertParams(t, server, "terminal.label", map[string]any{
		"terminal": ran.Address,
		"labels":   map[string]any{"herdr.seen": "2", "herdr.pinned": "1"},
	})
	event := receiveNotification(t, sub.Events())
	var data hc.LabelsData
	if event.Type != "labels" || event.Terminal != ran.Address || event.DecodeData(&data) != nil || !reflect.DeepEqual(data.Labels, want) {
		t.Fatalf("labels event = %+v data %+v, want the whole map %v", event, data, want)
	}

	// An empty value removes the key.
	got, err = client.Label(ctx, ran.Address, map[string]string{"herdr.pinned": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Labels["herdr.pinned"]; ok || got.Labels["keep"] != "me" {
		t.Fatalf("labels after removing a key = %v", got.Labels)
	}

	if _, err := client.Label(ctx, "t99", map[string]string{"a": "b"}); !errors.Is(err, hc.ErrNotFound) {
		t.Fatalf("Label of a missing terminal = %v, want ErrNotFound", err)
	}
}

func TestTurnEventFollowsTheStatusThatLeftWorking(t *testing.T) {
	server := hctest.New(t)
	client := hc.NewClient(server.Path)
	ctx := context.Background()
	ran, err := client.Run(ctx, hc.RunParams{Argv: []string{"claude"}})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := client.Subscribe(ctx, hc.SubscribeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	server.SetStatus(ran.Address, &hc.Status{State: "working", App: "claude-code"})
	server.SetStatus(ran.Address, &hc.Status{State: "done", App: "claude-code"})

	var types []string
	var turn hc.TurnData
	for range 3 {
		n := receiveNotification(t, sub.Events())
		types = append(types, n.Type)
		if n.Type == "turn" {
			if err := n.DecodeData(&turn); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A turn ends with a status event first, then a turn event.
	if !reflect.DeepEqual(types, []string{"status", "status", "turn"}) || turn.State != "done" || turn.Turn != 1 {
		t.Fatalf("events = %v, turn = %+v", types, turn)
	}
}
