package hc

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPaneIDRoundTrip(t *testing.T) {
	cases := map[string]string{
		"t7":        "t7",
		"review":    "review",
		"devbox/t4": "devbox~t4",
		"box/t12b":  "box~t12b",
	}
	for address, pane := range cases {
		if got := PaneID(address); got != pane {
			t.Errorf("PaneID(%q) = %q, want %q", address, got, pane)
		}
		if got := Address(pane); got != address {
			t.Errorf("Address(%q) = %q, want %q", pane, got, address)
		}
		if got := Address(address); got != address {
			t.Errorf("Address(%q) = %q, want it unchanged", address, got)
		}
	}
}

func TestIsRemote(t *testing.T) {
	for id, want := range map[string]bool{"t7": false, "review": false, "box/t1": true, "box~t1": true} {
		if got := IsRemote(id); got != want {
			t.Errorf("IsRemote(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestTerminalUnmarshalMapsRemoteAddress(t *testing.T) {
	var term Terminal
	if err := json.Unmarshal([]byte(`{"address":"devbox/t4","parent":"devbox/t1","state":"live","cwd":"/home/you/work"}`), &term); err != nil {
		t.Fatal(err)
	}
	if term.Address != "devbox~t4" {
		t.Errorf("Address = %q", term.Address)
	}
	if term.Parent == nil || *term.Parent != "devbox~t1" {
		t.Errorf("Parent = %v", term.Parent)
	}
	if term.State != "live" || term.Cwd != "/home/you/work" {
		t.Errorf("other fields lost: %+v", term)
	}

	var local Terminal
	if err := json.Unmarshal([]byte(`{"address":"t7"}`), &local); err != nil {
		t.Fatal(err)
	}
	if local.Address != "t7" || local.Parent != nil {
		t.Errorf("local terminal changed: %+v", local)
	}
}

func TestDecodeEventMapsRemoteTerminal(t *testing.T) {
	n, ok, err := decodeNotification(incomingMessage{
		Method: "event",
		Params: json.RawMessage(`{"seq":3,"type":"status","terminal":"devbox/t4","data":{}}`),
	})
	if err != nil || !ok {
		t.Fatalf("decode: ok=%v err=%v", ok, err)
	}
	if n.Terminal != "devbox~t4" {
		t.Errorf("Terminal = %q", n.Terminal)
	}
}

// Blocking probe (Carbon Probe 1): Client.Remove (terminal.rm) must send the
// raw hc address ("devbox/t4") on the wire and map RemoveResult.Removed back
// to the slash-free pane id ("devbox~t4").
func TestClientRemoveMapsRemotePaneID(t *testing.T) {
	dir, err := os.MkdirTemp("", "hct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "hc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	gotCh := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req struct {
			ID     uint64            `json:"id"`
			Params map[string]string `json:"params"`
		}
		_ = json.NewDecoder(conn).Decode(&req)
		gotCh <- req.Params["terminal"]
		_ = json.NewEncoder(conn).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"removed": "devbox/t4"},
		})
	}()

	res, err := NewClient(sock).Remove(context.Background(), "devbox~t4")
	if err != nil {
		t.Fatal(err)
	}
	gotTerminal := <-gotCh
	if gotTerminal != "devbox/t4" {
		t.Fatalf("wire terminal = %q, want %q", gotTerminal, "devbox/t4")
	}
	if res.Removed != "devbox~t4" {
		t.Fatalf("Removed = %q, want %q", res.Removed, "devbox~t4")
	}
}
