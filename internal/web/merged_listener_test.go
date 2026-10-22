package web

import (
	"net"
	"testing"
	"time"
)

// A web.bind of one LAN address still answers on loopback (the CLI's
// status, open and pair, and the setup page, which need plain HTTP and
// get it only from loopback).
func TestListenOnALANAddressAlsoListensOnLoopback(t *testing.T) {
	var lan net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			lan = n.IP
			break
		}
	}
	if lan == nil {
		t.Skip("no non-loopback IPv4 address on this machine")
	}
	s := &Server{}
	if err := s.Listen(net.JoinHostPort(lan.String(), "0")); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.ln.Close() }()
	_, port, _ := net.SplitHostPort(s.Addr())
	if host, _, _ := net.SplitHostPort(s.Addr()); host != lan.String() {
		t.Fatalf("Addr = %s, want the bind address", s.Addr())
	}
	for _, host := range []string{"127.0.0.1", lan.String()} {
		accepted := make(chan net.Conn, 1)
		go func() {
			c, err := s.ln.Accept()
			if err == nil {
				accepted <- c
			}
		}()
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", host, err)
		}
		select {
		case got := <-accepted:
			_ = got.Close()
		case <-time.After(2 * time.Second):
			t.Fatalf("nothing accepted from %s", host)
		}
		_ = c.Close()
	}
}

// Loopback and every-address binds need no second listener.
func TestListenOnLoopbackOrEverywhereIsOneListener(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "0.0.0.0:0"} {
		s := &Server{}
		if err := s.Listen(addr); err != nil {
			t.Fatal(err)
		}
		if _, merged := s.ln.(*mergedListener); merged {
			t.Errorf("%s: got a merged listener", addr)
		}
		_ = s.ln.Close()
	}
}
