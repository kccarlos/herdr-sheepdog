package hc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func TestSubscriptionSnapshotFiltersAndNotifications(t *testing.T) {
	server := hctest.New(t)
	server.Add(hc.Terminal{
		Address: "t1",
		Name:    "one",
		State:   "live",
		Labels:  map[string]string{"role": "worker"},
		Argv:    []string{"claude"},
		Turn:    1,
		Cols:    80,
		Rows:    24,
	})
	server.Add(hc.Terminal{
		Address: "t2",
		Name:    "two",
		State:   "live",
		Labels:  map[string]string{"role": "other"},
		Argv:    []string{"codex"},
		Cols:    80,
		Rows:    24,
	})

	client := hc.NewClient(server.Path)
	sub, err := client.Subscribe(context.Background(), hc.SubscribeFilter{
		Terminal: "t1",
		Labels:   map[string]string{"role": "worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	assertParams(t, server, "events.subscribe", map[string]any{
		"terminal": "t1",
		"labels":   map[string]any{"role": "worker"},
	})

	seq, terminals := sub.Snapshot()
	if len(terminals) != 1 || terminals[0].Address != "t1" {
		t.Fatalf("snapshot = %+v, want only t1", terminals)
	}

	status := &hc.Status{State: "blocked", App: "claude-code", Msg: "approve Bash"}
	server.SetStatus("t1", status)
	notification := receiveNotification(t, sub.Events())
	if notification.Method != "event" || notification.Type != "status" ||
		notification.Terminal != "t1" || notification.Seq <= seq {
		t.Fatalf("unexpected status notification: %+v", notification)
	}
	var statusData hc.StatusData
	if err := notification.DecodeData(&statusData); err != nil {
		t.Fatal(err)
	}
	if statusData.Status == nil || statusData.Status.State != "blocked" ||
		statusData.Status.App != "claude-code" || !statusData.Reported {
		t.Fatalf("unexpected status data: %+v", statusData)
	}

	server.SetStatus("t2", &hc.Status{State: "working"})
	server.Heartbeat()
	heartbeat := receiveNotification(t, sub.Events())
	if heartbeat.Method != "heartbeat" || heartbeat.Seq <= notification.Seq {
		t.Fatalf("filtered stream did not reach heartbeat: %+v", heartbeat)
	}

	server.Finish("t1", "exited")
	finished := receiveNotification(t, sub.Events())
	if finished.Type != "finished" {
		t.Fatalf("notification type = %q, want finished", finished.Type)
	}
	var finishedData hc.FinishedData
	if err := finished.DecodeData(&finishedData); err != nil {
		t.Fatal(err)
	}
	if finishedData.Reason != "exited" {
		t.Fatalf("finished data = %+v", finishedData)
	}

	server.Missed()
	missed := receiveNotification(t, sub.Events())
	if missed.Method != "missed" || missed.Seq != finished.Seq || len(missed.Terminals) != 1 ||
		missed.Terminals[0].State != "finished" {
		t.Fatalf("unexpected missed notification: %+v", missed)
	}
}

func TestSubscriptionCreatedAndTypedData(t *testing.T) {
	server := hctest.New(t)
	client := hc.NewClient(server.Path)
	sub, err := client.Subscribe(context.Background(), hc.SubscribeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	ran, err := client.Run(context.Background(), hc.RunParams{Argv: []string{"claude"}, Name: "new"})
	if err != nil {
		t.Fatal(err)
	}
	created := receiveNotification(t, sub.Events())
	if created.Type != "created" || created.Terminal != ran.Address {
		t.Fatalf("unexpected created notification: %+v", created)
	}
	var decoded hc.Terminal
	if err := created.DecodeData(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Address != ran.Address || decoded.Name != "new" {
		t.Fatalf("created terminal = %+v", decoded)
	}

	var title hc.TitleData
	if err := (hc.Notification{Type: "title", Data: json.RawMessage(`{"title":"Planner"}`)}).
		DecodeData(&title); err != nil {
		t.Fatal(err)
	}
	if title.Title != "Planner" {
		t.Fatalf("title = %q", title.Title)
	}
}

func TestSubscriptionContextCancelClosesEvents(t *testing.T) {
	server := hctest.New(t)
	client := hc.NewClient(server.Path)
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := client.Subscribe(ctx, hc.SubscribeFilter{})
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	waitClosed(t, sub.Events())
	if err := sub.Err(); err != nil {
		t.Fatalf("Err after context cancellation = %v, want nil", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("Close after context cancellation = %v", err)
	}
}

func TestSubscriptionDropClosesEventsAndAllowsReconnect(t *testing.T) {
	server := hctest.New(t)
	client := hc.NewClient(server.Path)
	sub, err := client.Subscribe(context.Background(), hc.SubscribeFilter{})
	if err != nil {
		t.Fatal(err)
	}

	server.DropSubscribers()
	waitClosed(t, sub.Events())
	if sub.Err() == nil {
		t.Fatal("Err after subscriber drop = nil, want connection error")
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("Close after drop = %v", err)
	}

	reconnected, err := client.Subscribe(context.Background(), hc.SubscribeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconnected.Close(); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, reconnected.Events())
}

func TestRawEventSamples(t *testing.T) {
	dir, err := os.MkdirTemp("", "hct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "raw.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.Close() }()

		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			serverErr <- err
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			serverErr <- err
			return
		}
		if request.Method != "events.subscribe" {
			serverErr <- errors.New("unexpected method " + request.Method)
			return
		}
		lines := []string{
			`{"jsonrpc":"2.0","id":1,"result":{"seq":101,"terminals":[]}}`,
			`{"jsonrpc":"2.0","method":"event","params":{"data":{"records":{},"reported":true,"status":{"app":"claude-code","state":"working"}},"seq":102,"terminal":"t1","type":"status"}}`,
			`{"jsonrpc":"2.0","method":"event","params":{"data":{"state":"done","turn":2},"seq":104,"terminal":"t1","type":"turn"}}`,
			`{"jsonrpc":"2.0","method":"event","params":{"data":{"records":{},"reported":true,"status":{"app":"claude-code","msg":"replied 'ok' as requested; nothing else pending","state":"done"}},"seq":105,"terminal":"t1","type":"status"}}`,
			`{"jsonrpc":"2.0","method":"heartbeat","params":{"seq":105}}`,
		}
		for _, message := range lines {
			if _, err := conn.Write([]byte(message + "\n")); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	sub, err := hc.NewClient(path).Subscribe(context.Background(), hc.SubscribeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	if seq, terminals := sub.Snapshot(); seq != 101 || len(terminals) != 0 {
		t.Fatalf("snapshot = %d, %+v", seq, terminals)
	}

	first := receiveNotification(t, sub.Events())
	if first.Seq != 102 || first.Type != "status" || first.Terminal != "t1" {
		t.Fatalf("first event = %+v", first)
	}
	var firstData hc.StatusData
	if err := first.DecodeData(&firstData); err != nil {
		t.Fatal(err)
	}
	if firstData.Status == nil || firstData.Status.State != "working" || !firstData.Reported {
		t.Fatalf("first data = %+v", firstData)
	}

	turn := receiveNotification(t, sub.Events())
	if turn.Seq != 104 || turn.Type != "turn" || turn.Terminal != "t1" {
		t.Fatalf("turn event = %+v", turn)
	}
	var turnData struct {
		State string `json:"state"`
		Turn  uint64 `json:"turn"`
	}
	if err := turn.DecodeData(&turnData); err != nil {
		t.Fatal(err)
	}
	if turnData.State != "done" || turnData.Turn != 2 {
		t.Fatalf("turn data = %+v", turnData)
	}

	last := receiveNotification(t, sub.Events())
	var lastData hc.StatusData
	if last.Seq != 105 || last.DecodeData(&lastData) != nil ||
		lastData.Status == nil || lastData.Status.State != "done" ||
		lastData.Status.Msg != "replied 'ok' as requested; nothing else pending" {
		t.Fatalf("last status = %+v (%+v)", last, lastData)
	}

	heartbeat := receiveNotification(t, sub.Events())
	if heartbeat.Method != "heartbeat" || heartbeat.Seq != 105 {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func receiveNotification(t *testing.T, events <-chan hc.Notification) hc.Notification {
	t.Helper()
	select {
	case notification, ok := <-events:
		if !ok {
			t.Fatal("events channel closed")
		}
		return notification
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification")
		return hc.Notification{}
	}
}

func waitClosed(t *testing.T, events <-chan hc.Notification) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("events channel did not close")
		}
	}
}
