package hc_test

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// Blocking probe (NeonH §1.1): Client.Remove (terminal.rm, added after
// 2026-11-13 in #627) must map pane id -> address on send and address -> pane
// id on RemoveResult.Removed.
func TestClientRemoveRemoteTerminal(t *testing.T) {
	srv := hctest.New(t)
	srv.Add(hc.Terminal{Address: "devbox/t4", State: "finished", Argv: []string{"claude"}})

	res, err := hc.NewClient(srv.Path).Remove(context.Background(), "devbox~t4")
	if err != nil {
		t.Fatalf("Remove(devbox~t4) error = %v, want nil", err)
	}
	if res.Removed != "devbox~t4" {
		t.Fatalf("Removed = %q, want %q", res.Removed, "devbox~t4")
	}
	assertParams(t, srv, "terminal.rm", map[string]any{"terminal": "devbox/t4"})
}

// Blocking probe (Carbon §1.1): passing a raw remote address to Client.Remove
// must still return a mapped slash-free pane id in RemoveResult.Removed.
func TestClientRemoveRemoteTerminalRawAddress(t *testing.T) {
	srv := hctest.New(t)
	srv.Add(hc.Terminal{Address: "devbox/t4", State: "finished", Argv: []string{"claude"}})

	res, err := hc.NewClient(srv.Path).Remove(context.Background(), "devbox/t4")
	if err != nil {
		t.Fatalf("Remove(devbox/t4) error = %v, want nil", err)
	}
	if res.Removed != "devbox~t4" {
		t.Fatalf("Removed = %q, want %q", res.Removed, "devbox~t4")
	}
	assertParams(t, srv, "terminal.rm", map[string]any{"terminal": "devbox/t4"})
}

// Regression test (category 3): existing remote hc RPCs and streams work
// end-to-end against hctest.Server (list, get, read, send, keys, interrupt,
// label, subscribe, missed, framed attach, kill).
func TestClientRemoteEndToEnd(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv.Add(hc.Terminal{
		Address:  "box/t4",
		State:    "live",
		Argv:     []string{"claude"},
		Cwd:      "/remote/work",
		Reported: true,
		Status:   &hc.Status{State: "idle", App: "claude-code"},
	})
	srv.SetScreen("box/t4", "remote line 1\nremote line 2\n")
	srv.SetPaint("box/t4", "remote paint")

	list, err := client.List(ctx, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Terminals) != 1 || list.Terminals[0].Address != "box~t4" {
		t.Fatalf("List = %+v, want [box~t4]", list.Terminals)
	}

	got, err := client.Get(ctx, "box~t4")
	if err != nil || got.Address != "box~t4" {
		t.Fatalf("Get(box~t4) = (%+v, %v)", got, err)
	}
	assertParams(t, srv, "terminal.get", map[string]any{"terminal": "box/t4"})

	rd, err := client.Read(ctx, "box~t4", 10)
	if err != nil || rd.Text != "remote line 1\nremote line 2\n" {
		t.Fatalf("Read(box~t4) = (%+v, %v)", rd, err)
	}
	assertParams(t, srv, "terminal.read", map[string]any{"terminal": "box/t4", "lines": float64(10)})

	if _, err := client.Send(ctx, "box~t4", "run tests", false); err != nil {
		t.Fatalf("Send(box~t4): %v", err)
	}
	assertParams(t, srv, "terminal.send", map[string]any{"terminal": "box/t4", "text": "run tests", "force": false})

	if _, err := client.Keys(ctx, "box~t4", "enter"); err != nil {
		t.Fatalf("Keys(box~t4): %v", err)
	}
	assertParams(t, srv, "terminal.keys", map[string]any{"terminal": "box/t4", "keys": []any{"enter"}})

	if _, err := client.Interrupt(ctx, "box~t4", time.Second); err != nil {
		t.Fatalf("Interrupt(box~t4): %v", err)
	}
	assertParams(t, srv, "terminal.interrupt", map[string]any{"terminal": "box/t4", "timeout": float64(1)})

	labeled, err := client.Label(ctx, "box~t4", map[string]string{"herdr.pinned": "1"})
	if err != nil || labeled.Address != "box~t4" || labeled.Labels["herdr.pinned"] != "1" {
		t.Fatalf("Label(box~t4) = (%+v, %v)", labeled, err)
	}
	assertParams(t, srv, "terminal.label", map[string]any{"terminal": "box/t4", "labels": map[string]any{"herdr.pinned": "1"}})

	sub, err := client.Subscribe(ctx, hc.SubscribeFilter{Terminal: "box~t4"})
	if err != nil {
		t.Fatalf("Subscribe(box~t4): %v", err)
	}
	defer func() { _ = sub.Close() }()
	_, snap := sub.Snapshot()
	if len(snap) != 1 || snap[0].Address != "box~t4" {
		t.Fatalf("Subscribe snapshot = %+v, want [box~t4]", snap)
	}
	srv.SetStatus("box/t4", &hc.Status{State: "working", App: "claude-code"})
	select {
	case n := <-sub.Events():
		if n.Method != "event" || n.Type != "status" || n.Terminal != "box~t4" {
			t.Fatalf("event = %+v, want status for box~t4", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for remote status event")
	}
	srv.Missed()
	select {
	case n := <-sub.Events():
		if n.Method != "missed" || len(n.Terminals) != 1 || n.Terminals[0].Address != "box~t4" {
			t.Fatalf("missed = %+v, want [box~t4]", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for missed notification")
	}

	framed, err := client.Framed(ctx)
	if err != nil {
		t.Fatalf("Framed: %v", err)
	}
	defer framed.Close()
	paintCh := make(chan string, 1)
	stream, attached, err := framed.Attach(ctx, hc.AttachParams{Terminal: "box~t4", Cols: 80, Rows: 24}, hc.StreamHandlers{
		OnData: func(p []byte) {
			select {
			case paintCh <- string(p):
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Attach(box~t4): %v", err)
	}
	if attached.Address != "box~t4" {
		t.Fatalf("Attach terminal = %+v, want box~t4", attached)
	}
	select {
	case p := <-paintCh:
		if p != "remote paint" {
			t.Fatalf("paint = %q, want %q", p, "remote paint")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for remote attach paint")
	}
	_ = stream.Detach()

	killed, err := client.Kill(ctx, "box~t4")
	if err != nil || killed.Address != "box~t4" || killed.State != "finished" {
		t.Fatalf("Kill(box~t4) = (%+v, %v)", killed, err)
	}
	assertParams(t, srv, "terminal.kill", map[string]any{"terminal": "box/t4"})
}
