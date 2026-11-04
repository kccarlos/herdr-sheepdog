package web

import (
	"net"
	"net/http"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// A page on another site (or another port of this computer) can make the
// browser send GET /api/pair?new=1; it must not retire the code the user
// is about to scan. The browser says where a request comes from in
// Sec-Fetch-Site; a request without it (the CLI, the panel, curl) and a
// same-origin or typed-in one may still ask for a new code.
func TestFreshPairCodeNeedsASameOriginRequest(t *testing.T) {
	old := pairSysAddrs
	pairSysAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}}, nil
	}
	t.Cleanup(func() { pairSysAddrs = old })
	h := newHarness(t, harnessOpts{})
	if err := h.srv.Listen("0.0.0.0:0"); err != nil {
		t.Fatal(err)
	}
	current := func(path, site string) string {
		t.Helper()
		req := h.authedRequest("GET", path, "", nil, nil)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		var resp PairResponse
		if code, _ := h.do(req, &resp); code != http.StatusOK || resp.Link == "" {
			t.Fatalf("GET %s (%q) = %d %+v", path, site, code, resp)
		}
		return resp.Link
	}
	for _, site := range []string{"cross-site", "same-site"} {
		before := current("/api/pair", "")
		if got := current("/api/pair?new=1", site); got != before {
			t.Errorf("%s ?new=1 replaced the code", site)
		}
		if got := current("/api/pair", ""); got != before {
			t.Errorf("%s ?new=1 retired the code", site)
		}
	}
	for _, site := range []string{"", "same-origin", "none"} {
		before := current("/api/pair", "")
		if got := current("/api/pair?new=1", site); got == before {
			t.Errorf("Sec-Fetch-Site %q ?new=1 kept the code", site)
		}
	}
	// The QR routes and the setup page's QR take the same rule.
	for _, path := range []string{"/api/pair/qr.json?new=1", "/api/pair/qr.svg?new=1"} {
		before := current("/api/pair", "")
		req := h.authedRequest("GET", path, "", nil, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		h.do(req, nil)
		if got := current("/api/pair", ""); got != before {
			t.Errorf("cross-site %s retired the code", path)
		}
	}
}
