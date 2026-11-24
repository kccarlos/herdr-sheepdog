package hc

import (
	"encoding/json"
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
