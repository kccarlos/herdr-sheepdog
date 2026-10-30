package web

import (
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// Without a reachable address /api/pair says why and the QR route
// refuses; both answer this computer only.
func TestPairLoopbackServer(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if err := h.srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	var resp PairResponse
	if code := h.getJSON("/api/pair", &resp); code != 200 || resp.Error == "" || resp.Link != "" {
		t.Errorf("/api/pair = %d %+v", code, resp)
	}
	if code, _ := h.do(h.authedRequest("GET", "/api/pair/qr.svg", "", nil, nil), nil); code != http.StatusConflict {
		t.Errorf("qr without an address: %d", code)
	}
	if code, _ := h.do(h.authedRequest("GET", "/api/pair/qr.json", "", nil, nil), nil); code != http.StatusConflict {
		t.Errorf("qr JSON without an address: %d", code)
	}
	if _, err := h.srv.pairAddresses("127.0.0.1:7777"); !errors.Is(err, pair.ErrLoopback) {
		t.Errorf("loopback: %v", err)
	}
	h.asRemote()
	if code := h.getJSON("/api/pair", nil); code != http.StatusForbidden {
		t.Errorf("/api/pair from the network = %d", code)
	}
}

// On every interface the link holds each Tailscale and LAN address, the
// daemon id, name and pin and a one-time code; reading it again keeps the
// code, ?new=1 replaces it; no event carries the code.
func TestPairAllInterfaces(t *testing.T) {
	old := pairSysAddrs
	pairSysAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}, {Iface: "utun4", IP: net.ParseIP("100.101.102.103")}}, nil
	}
	t.Cleanup(func() { pairSysAddrs = old })
	h := newHarness(t, harnessOpts{})
	if err := h.srv.Listen("0.0.0.0:0"); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(h.srv.Addr())
	var resp PairResponse
	if code := h.getJSON("/api/pair", &resp); code != 200 || resp.Link == "" || resp.Expires == nil {
		t.Fatalf("/api/pair = %d %+v", code, resp)
	}
	in, err := pair.ParseLink(resp.Link)
	if err != nil {
		t.Fatal(err)
	}
	if in.DaemonID != h.srv.deps.Computer.ID || in.Name != "Test Mac" || in.Pin != h.srv.deps.TLS.Pin() || in.Code == "" {
		t.Errorf("link = %+v", in)
	}
	if len(in.Addresses) != 2 || pair.HostPort(in.Addresses[0].URL) != "100.101.102.103:"+port || pair.HostPort(in.Addresses[1].URL) != "192.168.1.20:"+port {
		t.Errorf("addresses = %+v", in.Addresses)
	}
	if !strings.HasPrefix(resp.Link, "https://100.101.102.103:"+port+"/voice#pair=1&") {
		t.Errorf("link = %s", resp.Link)
	}
	var matrix pair.Matrix
	if code := h.getJSON("/api/pair/qr.json", &matrix); code != http.StatusOK {
		t.Fatalf("/api/pair/qr.json = %d", code)
	}
	want, err := pair.RenderMatrix(resp.Link)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(matrix, want) {
		t.Error("/api/pair/qr.json does not hold the current pairing link's matrix")
	}
	var again PairResponse
	h.getJSON("/api/pair", &again)
	if again.Link != resp.Link {
		t.Error("reading the code again made a new one")
	}
	var fresh PairResponse
	h.getJSON("/api/pair?new=1", &fresh)
	next, _ := pair.ParseLink(fresh.Link)
	if next.Code == in.Code {
		t.Error("?new=1 kept the code")
	}
	for _, ev := range h.log.Tail(10000) {
		for _, code := range []string{in.Code, next.Code} {
			if strings.Contains(string(ev.Payload), code) {
				t.Errorf("event %s carries the code", ev.Type)
			}
		}
	}
}
