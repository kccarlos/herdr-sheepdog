package web

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

const pairToken = "pair-test-token-0123456789abcdef"

// Without a reachable address /api/pair says why and the QR route
// refuses; both are behind the token.
func TestPairLoopbackServer(t *testing.T) {
	h := newHarness(t, harnessOpts{token: pairToken})
	if code, _ := h.do(h.authedRequest("GET", "/api/pair", "", nil, nil), nil); code != http.StatusUnauthorized {
		t.Errorf("without the token: %d", code)
	}
	var resp PairResponse
	if code, _ := h.do(h.authedRequest("GET", "/api/pair", pairToken, nil, nil), &resp); code != 200 || resp.Error == "" || !resp.HasToken {
		t.Errorf("/api/pair = %d %+v", code, resp)
	}
	if code, _ := h.do(h.authedRequest("GET", "/api/pair/qr.svg", pairToken, nil, nil), nil); code != http.StatusConflict {
		t.Errorf("qr without an address: %d", code)
	}
	if _, _, err := h.srv.pairingFor("127.0.0.1:7777"); !errors.Is(err, pair.ErrLoopback) {
		t.Errorf("loopback: %v", err)
	}
}

// On every interface the code holds each LAN and Tailscale address and
// the token; the JSON lists the addresses but never the token, and no
// event carries it.
func TestPairAllInterfaces(t *testing.T) {
	old := pairSysAddrs
	pairSysAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}, {Iface: "utun4", IP: net.ParseIP("100.101.102.103")}}, nil
	}
	t.Cleanup(func() { pairSysAddrs = old })
	h := newHarness(t, harnessOpts{token: pairToken})
	addrs, link, err := h.srv.pairingFor("0.0.0.0:7777")
	if err != nil {
		t.Fatal(err)
	}
	urls, tok, err := pair.Parse(link)
	if err != nil || tok != pairToken || len(urls) != 2 || urls[0] != "http://100.101.102.103:7777" || urls[1] != "http://192.168.1.20:7777" || len(addrs) != 2 {
		t.Errorf("link = %q %q %v", urls, tok, err)
	}
	req := h.authedRequest("GET", "/api/pair", pairToken, nil, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(string(body), pairToken) {
		t.Errorf("/api/pair carries the token: %s", body)
	}
	for _, ev := range h.log.Tail(10000) {
		if strings.Contains(string(ev.Payload), pairToken) {
			t.Errorf("event %s carries the token", ev.Type)
		}
	}
}
