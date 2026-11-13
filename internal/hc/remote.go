package hc

import (
	"encoding/json"
	"strings"
)

// Terminals on machines added with `hc machine add` have addresses such as
// "devbox/t4". Sheepdog's pane ids travel in URL paths
// (/api/agents/{pane_id}/…), the iPhone app and herdr-app mods, where a "/"
// breaks routing. The client therefore hands the rest of sheepdog a
// slash-free pane id ("devbox~t4") and turns it back into the hc
// address on every request. Local addresses ("t7", names) pass unchanged.

// RemoteSep replaces the "/" between a machine name and its terminal.
const RemoteSep = "~"

// PaneID turns an hc address into sheepdog's pane id.
func PaneID(address string) string { return strings.Replace(address, "/", RemoteSep, 1) }

// Address turns a pane id (or an address) back into the hc address.
func Address(pane string) string { return strings.Replace(pane, RemoteSep, "/", 1) }

// IsRemote reports whether a pane id or address names a terminal on an added
// machine. Its processes and files live on that machine, not this one.
func IsRemote(id string) bool { return strings.ContainsAny(id, "/"+RemoteSep) }

// UnmarshalJSON decodes a terminal record and maps its address and parent
// to pane ids.
func (t *Terminal) UnmarshalJSON(b []byte) error {
	type plain Terminal
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*t = Terminal(p)
	t.Address = PaneID(t.Address)
	if t.Parent != nil {
		parent := PaneID(*t.Parent)
		t.Parent = &parent
	}
	return nil
}

// wire is the filter as herdr-core takes it: the terminal as an hc address.
func (f SubscribeFilter) wire() SubscribeFilter {
	f.Terminal = Address(f.Terminal)
	return f
}
