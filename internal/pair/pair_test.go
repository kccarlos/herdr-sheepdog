package pair

import (
	"errors"
	"net"
	"strings"
	"testing"
)

var sys = []IfaceAddr{
	{Iface: "lo0", IP: net.ParseIP("127.0.0.1")},
	{Iface: "lo0", IP: net.ParseIP("::1")},
	{Iface: "en0", IP: net.ParseIP("fe80::1")},
	{Iface: "en0", IP: net.ParseIP("192.168.1.20")},
	{Iface: "en5", IP: net.ParseIP("169.254.3.4")},
	{Iface: "utun4", IP: net.ParseIP("100.101.102.103")},
	{Iface: "en1", IP: net.ParseIP("10.0.0.7")},
	{Iface: "tailscale0", IP: net.ParseIP("10.200.0.1")},
}

func TestAddresses(t *testing.T) {
	got, err := Addresses("0.0.0.0", 7777, sys)
	if err != nil {
		t.Fatal(err)
	}
	want := []Address{
		{"http://10.0.0.7:7777", KindLAN},
		{"http://192.168.1.20:7777", KindLAN},
		{"http://10.200.0.1:7777", KindTailscale},
		{"http://100.101.102.103:7777", KindTailscale},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got, _ := Addresses("", 7777, sys); len(got) != 4 {
		t.Errorf("empty bind: %+v", got)
	}
	if got, err := Addresses("100.101.102.103", 7000, sys); err != nil || len(got) != 1 || got[0] != (Address{"http://100.101.102.103:7000", KindTailscale}) {
		t.Errorf("specific bind: %+v, %v", got, err)
	}
	if got, err := Addresses("mac.tailnet.ts.net", 7777, sys); err != nil || got[0].URL != "http://mac.tailnet.ts.net:7777" {
		t.Errorf("host bind: %+v, %v", got, err)
	}
	for _, b := range []string{"127.0.0.1", "localhost", "::1"} {
		if _, err := Addresses(b, 7777, sys); !errors.Is(err, ErrLoopback) {
			t.Errorf("%s: %v", b, err)
		}
	}
	if _, err := Addresses("0.0.0.0", 7777, sys[:3]); err == nil {
		t.Error("no address and no error")
	}
}

func TestURLRoundTrip(t *testing.T) {
	addrs := []Address{{"http://192.168.1.20:7777", KindLAN}, {"http://100.101.102.103:7777", KindTailscale}}
	link := URL(addrs, "s3cr&t=x y")
	if !strings.HasPrefix(link, "sheepdog://pair?") {
		t.Fatalf("link = %s", link)
	}
	urls, tok, err := Parse(link)
	if err != nil || tok != "s3cr&t=x y" || len(urls) != 2 || urls[0] != addrs[0].URL || urls[1] != addrs[1].URL {
		t.Errorf("parse = %q %q %v", urls, tok, err)
	}
	if _, tok, _ := Parse(URL(addrs, "")); tok != "" {
		t.Errorf("token without one: %q", tok)
	}
	for _, bad := range []string{"https://pair?u=x", "sheepdog://other?u=x", "sheepdog://pair?t=x"} {
		if _, _, err := Parse(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestRenderings(t *testing.T) {
	link := URL([]Address{{"http://192.168.1.20:7777", KindLAN}}, "tok")
	term, err := Terminal(link, false)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(term, "\n"), "\n")
	if len(lines) < 10 || strings.Trim(lines[0], "█") != "" {
		t.Errorf("terminal code: first line %q (want the light quiet zone)", lines[0])
	}
	inv, _ := Terminal(link, true)
	if inv == term || strings.Trim(strings.Split(inv, "\n")[0], " ") != "" {
		t.Error("inverted code is not inverted")
	}
	svg, err := SVG(link)
	if err != nil || !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "M") {
		t.Errorf("svg = %.80s, %v", svg, err)
	}
}
