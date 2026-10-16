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
		{"http://10.200.0.1:7777", KindTailscale},
		{"http://100.101.102.103:7777", KindTailscale},
		{"http://10.0.0.7:7777", KindLAN},
		{"http://192.168.1.20:7777", KindLAN},
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

// The coloured code draws the same modules as the plain one.
func TestANSIMatchesPlain(t *testing.T) {
	link := URL([]Address{{"http://192.168.1.20:7777", KindLAN}}, "tok")
	ansi, err := ANSI(link)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := Terminal(link, false)
	var fromANSI, fromPlain []string
	for _, line := range strings.Split(strings.TrimSuffix(ansi, "\n"), "\n") {
		var row strings.Builder
		for _, cell := range strings.Split(line, "\x1b[")[1:] {
			if strings.HasPrefix(cell, "0m") {
				continue
			}
			top, bottom := strings.HasPrefix(cell, "30;"), strings.Contains(cell, ";40m")
			row.WriteString(map[[2]bool]string{{false, false}: "█", {true, true}: " ", {true, false}: "▄", {false, true}: "▀"}[[2]bool{top, bottom}])
		}
		fromANSI = append(fromANSI, row.String())
	}
	fromPlain = strings.Split(strings.TrimSuffix(plain, "\n"), "\n")
	if strings.Join(fromANSI, "\n") != strings.Join(fromPlain, "\n") {
		t.Errorf("ANSI and plain codes differ")
	}
}

func ia(iface, ip string) IfaceAddr { return IfaceAddr{Iface: iface, IP: net.ParseIP(ip)} }

func urls(t *testing.T, sys []IfaceAddr, route string) []string {
	t.Helper()
	old := DefaultRouteIP
	DefaultRouteIP = func() net.IP { return net.ParseIP(route) }
	defer func() { DefaultRouteIP = old }()
	got, err := Addresses("0.0.0.0", 7777, sys)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range got {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(a.URL, "http://"), ":7777")+" "+a.Kind)
	}
	return out
}

func TestAddressRules(t *testing.T) {
	lo := ia("lo0", "127.0.0.1")
	cases := []struct {
		name  string
		sys   []IfaceAddr
		route string
		want  []string
	}{
		{"wifi only", []IfaceAddr{lo, ia("en0", "192.168.1.20")}, "192.168.1.20", []string{"192.168.1.20 lan"}},
		{"wifi and tailscale", []IfaceAddr{lo, ia("en0", "192.168.1.20"), ia("utun5", "100.101.102.103")}, "192.168.1.20",
			[]string{"100.101.102.103 tailscale", "192.168.1.20 lan"}},
		{"proxy tun and docker", []IfaceAddr{lo, ia("en0", "192.168.1.20"), ia("utun3", "198.18.0.1"), ia("bridge100", "192.168.64.1"),
			ia("docker0", "172.17.0.1"), ia("br-ab12", "172.18.0.1"), ia("vmnet8", "172.16.5.1"), ia("utun4", "10.9.0.2"), ia("en7", "203.0.113.5"), ia("en8", "198.19.0.4")},
			"192.168.1.20", []string{"192.168.1.20 lan"}},
		{"ethernet and wifi, route on ethernet", []IfaceAddr{ia("en0", "192.168.1.20"), ia("en5", "10.0.0.7")}, "10.0.0.7",
			[]string{"10.0.0.7 lan", "192.168.1.20 lan"}},
		{"ethernet and wifi, no route", []IfaceAddr{ia("en0", "192.168.1.20"), ia("en5", "10.0.0.7")}, "",
			[]string{"10.0.0.7 lan", "192.168.1.20 lan"}},
		{"172.16/12 edges", []IfaceAddr{ia("en0", "172.31.1.1"), ia("en1", "172.32.1.1")}, "", []string{"172.31.1.1 lan"}},
		{"tailscale interface", []IfaceAddr{ia("tailscale0", "100.64.0.9"), ia("eth0", "192.168.0.5")}, "192.168.0.5",
			[]string{"100.64.0.9 tailscale", "192.168.0.5 lan"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := urls(t, c.sys, c.route); strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
	for _, sys := range [][]IfaceAddr{
		{lo}, {lo, ia("utun3", "198.18.0.1")}, {ia("docker0", "172.17.0.1"), ia("en0", "169.254.1.1")},
	} {
		old := DefaultRouteIP
		DefaultRouteIP = func() net.IP { return nil }
		_, err := Addresses("0.0.0.0", 7777, sys)
		DefaultRouteIP = old
		if err == nil || !strings.Contains(err.Error(), "no LAN or Tailscale address") {
			t.Errorf("%v: err = %v", sys, err)
		}
	}
}

func TestExplain(t *testing.T) {
	ts := Address{"http://100.101.102.103:7777", KindTailscale}
	lan := Address{"http://192.168.1.20:7777", KindLAN}
	if got := Explain([]Address{ts, lan}); !strings.Contains(got, "Tailscale address 100.101.102.103: works anywhere your phone is on Tailscale") || !strings.Contains(got, "Wi-Fi") {
		t.Errorf("both: %q", got)
	}
	if got := Explain([]Address{ts}); strings.Contains(got, "falls back") {
		t.Errorf("tailscale only: %q", got)
	}
	got := Explain([]Address{lan})
	for _, w := range []string{"Wi-Fi address 192.168.1.20", "same Wi-Fi", "install Tailscale"} {
		if !strings.Contains(got, w) {
			t.Errorf("lan: %q lacks %q", got, w)
		}
	}
	if Explain(nil) != "" {
		t.Error("empty")
	}
}
