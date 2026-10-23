// Package pair builds the pairing link the app and browsers scan
// (docs/dev/design/security.md "The QR"): an https link to /voice with
// the daemon id, the computer's name, the TLS pin, a one-time enroll code
// and the daemon's reachable addresses (Tailscale first, then LAN, then
// web.advertise) in the fragment. `sheepdog pair` and the setup page show
// it as a QR code; the daemon mints the code (internal/web/pair.go). The
// code is a secret until used: this package never logs it.
package pair

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Address kinds.
const (
	KindLAN       = "lan"
	KindTailscale = "tailscale"
)

// Address is one base URL the phone may reach the daemon at.
type Address struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

// ErrLoopback: the daemon listens on loopback only; no phone can reach it.
var ErrLoopback = errors.New(`the daemon listens on 127.0.0.1 only, so a phone cannot reach it: turn on phone access in step 4 of the setup page (sheepdog setup), or set web.bind (a LAN or Tailscale address, or "0.0.0.0") yourself and restart`)

// tailnet is Tailscale's IPv4 range (CGNAT 100.64.0.0/10); tailnet6 is
// its IPv6 range (fd7a:115c:a1e0::/48).
var (
	tailnet  = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}
	tailnet6 = &net.IPNet{IP: net.ParseIP("fd7a:115c:a1e0::"), Mask: net.CIDRMask(48, 128)}
)

// Kind classifies an address: Tailscale's ranges or interface, else LAN.
func Kind(ip net.IP, iface string) string {
	if tailnet.Contains(ip) || (ip.To4() == nil && tailnet6.Contains(ip)) || strings.HasPrefix(iface, "tailscale") {
		return KindTailscale
	}
	return KindLAN
}

// IfaceAddr is one interface address (net.Interfaces), so tests can feed
// their own.
type IfaceAddr struct {
	Iface string
	IP    net.IP
}

// SystemAddrs lists this machine's interface addresses (up interfaces
// only).
func SystemAddrs() ([]IfaceAddr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []IfaceAddr
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				out = append(out, IfaceAddr{Iface: ifc.Name, IP: ipnet.IP})
			}
		}
	}
	return out, nil
}

// virtualIfacePrefixes name interfaces that never carry a real LAN: VPN
// and proxy tunnels (utun, unless it holds a Tailscale address), VM and
// container bridges, and Apple's wireless-direct and private links.
var virtualIfacePrefixes = []string{"utun", "bridge", "vmnet", "docker", "br-", "veth", "awdl", "llw", "anpi", "ap1"}

// benchmark is 198.18.0.0/15, which proxy and VPN apps use for their TUN
// address; it is never a LAN.
var benchmark = &net.IPNet{IP: net.IPv4(198, 18, 0, 0).To4(), Mask: net.CIDRMask(15, 32)}

// privateNets are the RFC 1918 ranges, the only addresses a home or
// office router hands out.
var privateNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// isLANAddr reports whether ip on iface is a real LAN address: RFC 1918,
// not 198.18.0.0/15, on an interface that is not a known virtual one.
func isLANAddr(ip net.IP, iface string) bool {
	if benchmark.Contains(ip) {
		return false
	}
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(iface, p) {
			return false
		}
	}
	for _, n := range privateNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// DefaultRouteIP is the local address the OS would use to reach the
// internet (the default route's), or nil. Tests replace it. It "connects"
// a UDP socket, which sends nothing.
var DefaultRouteIP = func() net.IP {
	c, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return nil
	}
	defer func() { _ = c.Close() }()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP
	}
	return nil
}

// Addresses are the base URLs a phone can use for a daemon bound to bind
// on port. For an unspecified bind (0.0.0.0, ""): Tailscale addresses
// first (they work anywhere the phone is on Tailscale), then real LAN
// addresses (RFC 1918 on non-virtual interfaces; see isLANAddr) as the
// fallback for a phone not yet on Tailscale, the default route's address
// first and the rest sorted. With no Tailscale, LAN only. A specific
// address or host name as bind is used as given. ErrLoopback when bind is
// loopback.
func Addresses(bind string, port int, sys []IfaceAddr) ([]Address, error) {
	base := func(ip net.IP) string { return "https://" + net.JoinHostPort(ip.String(), strconv.Itoa(port)) }
	ip := net.ParseIP(bind)
	switch {
	case bind == "localhost" || ip != nil && ip.IsLoopback():
		return nil, ErrLoopback
	case bind != "" && ip == nil:
		// A host name: the phone resolves it.
		return []Address{{URL: "https://" + net.JoinHostPort(bind, strconv.Itoa(port)), Kind: KindLAN}}, nil
	case ip != nil && !ip.IsUnspecified():
		iface := ""
		for _, a := range sys {
			if a.IP.Equal(ip) {
				iface = a.Iface
			}
		}
		return []Address{{URL: base(ip), Kind: Kind(ip, iface)}}, nil
	}
	var lan, ts []Address
	seen := map[string]bool{}
	for _, a := range sys {
		ip4 := a.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || seen[ip4.String()] {
			continue
		}
		ad := Address{URL: base(ip4), Kind: Kind(ip4, a.Iface)}
		if ad.Kind == KindLAN && !isLANAddr(ip4, a.Iface) {
			continue
		}
		seen[ip4.String()] = true
		if ad.Kind == KindTailscale {
			ts = append(ts, ad)
		} else {
			lan = append(lan, ad)
		}
	}
	byURL := func(s []Address) { sort.Slice(s, func(i, j int) bool { return s[i].URL < s[j].URL }) }
	byURL(lan)
	byURL(ts)
	if len(lan) > 1 && DefaultRouteIP != nil {
		if d := DefaultRouteIP(); d != nil {
			want := base(d.To4())
			for i, l := range lan {
				if l.URL == want {
					lan = append([]Address{l}, append(lan[:i:i], lan[i+1:]...)...)
					break
				}
			}
		}
	}
	out := append(ts, lan...)
	if len(out) == 0 {
		return nil, errors.New("no LAN or Tailscale address found on this machine")
	}
	return out, nil
}

// Explain says in one sentence which address the pairing code leads with
// and what that means for the phone; "" for no addresses. The setup page
// and `sheepdog pair` both show it.
func Explain(addrs []Address) string {
	if len(addrs) == 0 {
		return ""
	}
	host := func(a Address) string {
		u, err := url.Parse(a.URL)
		if err != nil {
			return a.URL
		}
		return u.Hostname()
	}
	first := addrs[0]
	hasLAN := false
	for _, a := range addrs[1:] {
		hasLAN = hasLAN || a.Kind == KindLAN
	}
	if first.Kind == KindTailscale {
		s := "Uses your Tailscale address " + host(first) + ": works anywhere your phone is on Tailscale."
		if hasLAN {
			s += " If the phone is not on Tailscale yet, it falls back to your Wi-Fi address on the same Wi-Fi."
		}
		return s
	}
	return "Uses your Wi-Fi address " + host(first) + ": the phone must be on the same Wi-Fi. To use it away from home, install Tailscale on this computer and your phone, then pair again."
}

// LinkInfo is what a pairing link holds (docs/dev/design/security.md
// "The QR").
type LinkInfo struct {
	DaemonID string
	Name     string
	// Pin is the TLS key's pin, base64url(SHA-256(SPKI)).
	Pin string
	// Code is the one-time enroll code.
	Code      string
	Addresses []Address
	// Host, when set, is the host:port the browser link opens instead of
	// the first address: the *.ts.net name with a real certificate
	// (web.tailscale_https). The app reads only the u= addresses.
	Host string
}

// Link is the pairing link: an https link to /voice at the first address,
// everything else in the fragment, which browsers never send to a server:
//
//	https://<host:port>/voice#pair=1&d=<id>&n=<name>&k=<pin>&e=<code>&u=<host:port>&u=…
//
// A phone camera opens it in the browser (which pairs itself); the app's
// scanner reads the same link.
func Link(in LinkInfo) string {
	q := url.Values{}
	q.Set("d", in.DaemonID)
	q.Set("n", in.Name)
	q.Set("k", in.Pin)
	q.Set("e", in.Code)
	first := ""
	for _, a := range in.Addresses {
		hp := HostPort(a.URL)
		if first == "" {
			first = hp
		}
		q.Add("u", hp)
	}
	if in.Host != "" {
		first = in.Host
	}
	// pair=1 first, so a page tells a pairing link at a glance.
	return "https://" + first + "/voice#pair=1&" + q.Encode()
}

// ParseLink reads a pairing link back (tests, and the CLI's own check).
func ParseLink(link string) (LinkInfo, error) {
	u, err := url.Parse(link)
	if err != nil {
		return LinkInfo{}, err
	}
	q, err := url.ParseQuery(u.EscapedFragment())
	if err != nil {
		return LinkInfo{}, err
	}
	if q.Get("pair") != "1" {
		return LinkInfo{}, errors.New("pair: not a pairing link")
	}
	in := LinkInfo{DaemonID: q.Get("d"), Name: q.Get("n"), Pin: q.Get("k"), Code: q.Get("e")}
	for _, hp := range q["u"] {
		in.Addresses = append(in.Addresses, Address{URL: "https://" + hp, Kind: KindLAN})
	}
	if in.DaemonID == "" || in.Pin == "" || in.Code == "" || len(in.Addresses) == 0 {
		return LinkInfo{}, errors.New("pair: incomplete pairing link")
	}
	return in, nil
}

// HostPort is a base URL's host:port.
func HostPort(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// KindAdvertised marks an address from web.advertise.
const KindAdvertised = "advertised"

// Advertised turns web.advertise entries (host:port, a host alone for
// port 7777, or a URL) into addresses.
func Advertised(list []string) []Address {
	var out []Address
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if i := strings.Index(a, "://"); i >= 0 {
			a = a[i+3:]
		}
		a = strings.TrimSuffix(a, "/")
		if _, _, err := net.SplitHostPort(a); err != nil {
			a = net.JoinHostPort(strings.Trim(a, "[]"), "7777")
		}
		out = append(out, Address{URL: "https://" + a, Kind: KindAdvertised})
	}
	return out
}

// Merge appends extra to addrs, skipping any already there.
func Merge(addrs, extra []Address) []Address {
	out := append([]Address(nil), addrs...)
	for _, e := range extra {
		dup := false
		for _, a := range out {
			dup = dup || a.URL == e.URL
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out
}

// Terminal renders link as a QR code for a terminal: two modules per
// character with half blocks, with the quiet zone. The text colour draws
// the light modules, so on a dark terminal the code is dark on light, as
// scanners expect; invert is for a light terminal (dark text).
func Terminal(link string, invert bool) (string, error) {
	q, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return "", err
	}
	bm := q.Bitmap() // true = dark, quiet zone included
	var b strings.Builder
	for y := 0; y < len(bm); y += 2 {
		for x := range bm[y] {
			top := bm[y][x] != invert
			bottom := (y+1 < len(bm) && bm[y+1][x]) != invert
			switch {
			case !top && !bottom:
				b.WriteString("█") // both light
			case top && bottom:
				b.WriteString(" ")
			case top:
				b.WriteString("▄") // light below
			default:
				b.WriteString("▀") // light above
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ANSI renders link as a QR code with explicit colours (ANSI escape
// codes: each "▀" draws the upper module in the text colour and the lower
// one in the background), so it scans on a light or a dark terminal
// alike. For a terminal only; Terminal is the plain-text fallback.
func ANSI(link string) (string, error) {
	q, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return "", err
	}
	bm := q.Bitmap()
	fg := func(dark bool) string {
		if dark {
			return "30"
		}
		return "97"
	}
	bg := func(dark bool) string {
		if dark {
			return "40"
		}
		return "107"
	}
	var b strings.Builder
	for y := 0; y < len(bm); y += 2 {
		for x := range bm[y] {
			bottom := y+1 < len(bm) && bm[y+1][x]
			b.WriteString("\x1b[" + fg(bm[y][x]) + ";" + bg(bottom) + "m▀")
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String(), nil
}

// SVG renders link as a QR code in SVG (dark modules on white).
func SVG(link string) (string, error) {
	q, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return "", err
	}
	bm := q.Bitmap()
	n := len(bm)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`, n, n, n*6, n*6)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y, row := range bm {
		for x, dark := range row {
			if dark {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}
